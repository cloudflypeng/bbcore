// Command bbcore is an embeddable sidecar that exposes Bilibili's HTTP, live
// danmaku WebSocket and gRPC interfaces over stdio JSON-RPC.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/cloudflypeng/bbcore/internal/bili"
	"github.com/cloudflypeng/bbcore/internal/biligrpc"
	"github.com/cloudflypeng/bbcore/internal/jsonrpc"
	"github.com/cloudflypeng/bbcore/internal/live"
	"github.com/cloudflypeng/bbcore/internal/service"
	"github.com/cloudflypeng/bbcore/internal/state"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "bbcore: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		dataDir    = flag.String("data-dir", "", "state directory (default $BBCORE_DATA_DIR or ~/.bbcore)")
		debug      = flag.Bool("debug", false, "log every upstream request (also env BBCORE_DEBUG)")
		maxWorkers = flag.Int("max-concurrent", 32, "maximum concurrent RPC handlers")
		showVer    = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()
	if *showVer {
		fmt.Println(service.Version)
		return nil
	}

	dir := *dataDir
	if dir == "" {
		dir = os.Getenv("BBCORE_DATA_DIR")
	}
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("resolve home dir: %w", err)
		}
		dir = filepath.Join(home, ".bbcore")
	}

	debugEnabled := *debug || os.Getenv("BBCORE_DEBUG") != ""
	level := slog.LevelInfo
	if debugEnabled {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	store, err := state.Open(dir)
	if err != nil {
		return err
	}
	server := jsonrpc.NewServer(os.Stdout, os.Stderr, *maxWorkers)

	client := bili.NewClient(bili.Config{
		CacheDir: dir,
		Logger:   logger,
		Debug:    debugEnabled,
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

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()

	bootCtx, cancelBoot := context.WithTimeout(ctx, 15*time.Second)
	if err := client.EnsureBuvid(bootCtx); err != nil {
		logger.Warn("buvid bootstrap failed; using a local buvid", "error", err)
	}
	cancelBoot()

	emit := func(method string, params any) { server.WriteNotification(method, params) }
	grpcClient := biligrpc.New(logger)
	liveManager := live.NewManager(client, logger, emit)
	svc := service.New(service.Options{
		Client:  client,
		Store:   store,
		Live:    liveManager,
		Grpc:    grpcClient,
		Logger:  logger,
		DataDir: dir,
		Stop: func() {
			cancelRun()
		},
	})
	svc.SetNotifier(emit)
	svc.Register(server)

	logger.Info("bbcore ready",
		"version", service.Version,
		"data_dir", dir,
		"methods", len(server.Registered()),
	)
	serveErr := server.Serve(runCtx, os.Stdin)
	svc.Stop()
	if err := store.Save(); err != nil {
		logger.Warn("flush state", "error", err)
	}
	return serveErr
}
