// Package mobile exposes bbcore to iOS through gomobile. Start spins up the
// same service used by the stdio and HTTP transports inside the host process;
// Call invokes one JSON-RPC method synchronously; events are delivered to the
// registered handler as JSON-RPC notification strings.
package mobile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/cloudflypeng/bbcore/internal/bili"
	"github.com/cloudflypeng/bbcore/internal/biligrpc"
	"github.com/cloudflypeng/bbcore/internal/events"
	"github.com/cloudflypeng/bbcore/internal/jsonrpc"
	"github.com/cloudflypeng/bbcore/internal/live"
	"github.com/cloudflypeng/bbcore/internal/service"
	"github.com/cloudflypeng/bbcore/internal/state"
)

// EventHandler receives one JSON-RPC notification per event, in the same shape
// the stdio transport writes: {"jsonrpc":"2.0","method":"event.<topic>","params":{...}}.
type EventHandler interface {
	OnEvent(payload string)
}

var (
	mu           sync.Mutex
	running      *runtime
	handlerMu    sync.RWMutex
	eventHandler EventHandler
)

type runtime struct {
	logger  *slog.Logger
	logFile *os.File
	store   *state.Store
	server  *jsonrpc.Server
	service *service.Service
	hub     *events.Hub
	cancel  context.CancelFunc
	calls   sync.RWMutex
	stopped bool
	wg      sync.WaitGroup
}

// SetEventHandler installs the event callback. It may be called before or
// after Start; passing nil clears it.
func SetEventHandler(handler EventHandler) {
	handlerMu.Lock()
	eventHandler = handler
	handlerMu.Unlock()
}

func currentHandler() EventHandler {
	handlerMu.RLock()
	defer handlerMu.RUnlock()
	return eventHandler
}

// Version returns the bbcore version compiled into the framework.
func Version() string {
	return service.Version
}

// Start opens the data directory and prepares the in-process service. Buvid
// bootstrap runs in the background so the host is not blocked by the network.
func Start(dataDir string) error {
	dir := strings.TrimSpace(dataDir)
	if dir == "" {
		return errors.New("data directory is required")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if running != nil {
		return errors.New("bbcore is already running")
	}

	store, err := state.Open(dir)
	if err != nil {
		return err
	}

	logFile, logErr := os.OpenFile(filepath.Join(dir, "bbcore.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	var logOut io.Writer = os.Stderr
	if logErr == nil {
		logOut = io.MultiWriter(os.Stderr, logFile)
	}
	logger := slog.New(slog.NewTextHandler(logOut, &slog.HandlerOptions{Level: slog.LevelInfo}))

	hub := events.New(events.Options{})
	server := jsonrpc.NewServer(io.Discard, io.Discard, 16)
	client := bili.NewClient(bili.Config{
		CacheDir: dir,
		Logger:   logger,
		OnCookies: func(mid int64, cookies []*http.Cookie) {
			if err := store.SetCookies(mid, cookies); err != nil {
				logger.Warn("persist cookies", "mid", mid, "error", err)
			}
		},
		Anon: func() *bili.Credential {
			cred := bili.AnonymousCredential()
			for k, v := range store.AnonymousCookies() {
				cred.Cookies[k] = v
			}
			return cred
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	emit := func(method string, params any) {
		hub.Publish(strings.TrimPrefix(method, "event."), params)
	}
	liveManager := live.NewManager(client, logger, emit)
	svc := service.New(service.Options{
		Client:  client,
		Store:   store,
		Live:    liveManager,
		Grpc:    biligrpc.New(logger),
		Events:  hub,
		Logger:  logger,
		DataDir: dir,
		Stop:    cancel,
	})
	svc.SetNotifier(emit)
	svc.Register(server)

	rt := &runtime{
		logger:  logger,
		logFile: logFile,
		store:   store,
		server:  server,
		service: svc,
		hub:     hub,
		cancel:  cancel,
	}

	ch, unsubscribe := hub.Subscribe(nil, 0)
	rt.wg.Add(1)
	go func() {
		defer rt.wg.Done()
		defer unsubscribe()
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-ch:
				if !ok {
					return
				}
				payload, err := json.Marshal(map[string]any{
					"jsonrpc": "2.0",
					"method":  "event." + ev.Topic,
					"params": map[string]any{
						"seq":  ev.Seq,
						"ts":   ev.TS,
						"data": ev.Data,
					},
				})
				if err != nil {
					continue
				}
				if handler := currentHandler(); handler != nil {
					handler.OnEvent(string(payload))
				}
			}
		}
	}()

	go func() {
		bootCtx, cancelBoot := context.WithTimeout(ctx, 15*time.Second)
		defer cancelBoot()
		if err := client.EnsureBuvid(bootCtx); err != nil && ctx.Err() == nil {
			logger.Warn("buvid bootstrap failed; using a local buvid", "error", err)
		}
	}()

	logger.Info("bbcore embedded ready", "version", service.Version, "data_dir", dir)
	running = rt
	return nil
}

// Stop shuts the service down, flushes state and releases the data directory.
func Stop() {
	mu.Lock()
	rt := running
	running = nil
	mu.Unlock()
	if rt == nil {
		return
	}
	rt.calls.Lock()
	defer rt.calls.Unlock()
	rt.stopped = true
	rt.cancel()
	rt.service.Stop()
	rt.wg.Wait()
	if err := rt.store.Save(); err != nil {
		rt.logger.Warn("flush state", "error", err)
	}
	if rt.logFile != nil {
		_ = rt.logFile.Close()
	}
}

// Call invokes one method and returns its JSON-encoded result. An RPC error is
// returned as an error whose text starts with the numeric JSON-RPC code.
func Call(method string, paramsJSON string) (string, error) {
	mu.Lock()
	rt := running
	mu.Unlock()
	if rt == nil {
		return "", errors.New("bbcore is not running")
	}

	rt.calls.RLock()
	defer rt.calls.RUnlock()
	if rt.stopped {
		return "", errors.New("bbcore is stopping")
	}

	var params json.RawMessage
	if trimmed := strings.TrimSpace(paramsJSON); trimmed != "" {
		if !json.Valid([]byte(trimmed)) {
			return "", errors.New("params are not valid JSON")
		}
		params = json.RawMessage(trimmed)
	}

	result, rpcErr := rt.server.Call(context.Background(), method, params)
	if rpcErr != nil {
		return "", fmt.Errorf("%d: %s", rpcErr.Code, rpcErr.Message)
	}
	payload, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("encode result: %w", err)
	}
	return string(payload), nil
}
