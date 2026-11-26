// Package service exposes the RPC method surface that hosts call over stdio.
package service

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"sync"

	"github.com/cloudflypeng/bbcore/internal/bili"
	"github.com/cloudflypeng/bbcore/internal/biligrpc"
	"github.com/cloudflypeng/bbcore/internal/download"
	"github.com/cloudflypeng/bbcore/internal/events"
	"github.com/cloudflypeng/bbcore/internal/jsonrpc"
	"github.com/cloudflypeng/bbcore/internal/live"
	"github.com/cloudflypeng/bbcore/internal/state"
)

// Version is stamped at build time via -ldflags (see README).
var Version = "0.1.0-dev"

// Method is one registered RPC method.
type Method struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Handler     jsonrpc.Handler `json:"-"`
}

// Options are the dependencies of the service.
type Options struct {
	Client  *bili.Client
	Store   *state.Store
	Live    *live.Manager
	Grpc    *biligrpc.Client
	Events  *events.Hub
	Logger  *slog.Logger
	DataDir string
	Stop    func()
}

// Service implements the RPC surface.
type Service struct {
	opt     Options
	mu      sync.RWMutex
	methods map[string]Method
	notify  func(method string, params any)

	streamMu sync.Mutex
	streams  *streamGateway

	castMu     sync.Mutex
	activeCast *castSession

	downloadMu sync.Mutex
	downloads  *download.Manager
}

// New builds a service and registers all static methods.
func New(opt Options) *Service {
	if opt.Logger == nil {
		opt.Logger = slog.Default()
	}
	s := &Service{opt: opt, methods: map[string]Method{}}
	s.registerCore()
	s.registerAuth()
	s.registerAPI()
	s.registerVideo()
	s.registerUser()
	s.registerSearch()
	s.registerDiscover()
	s.registerDynamic()
	s.registerLive()
	s.registerLivePlay()
	s.registerReply()
	s.registerDanmaku()
	s.registerFav()
	s.registerMsg()
	s.registerPGC()
	s.registerPGCPlay()
	s.registerMisc()
	s.registerGRPC()
	s.registerStream()
	s.registerPlayback()
	s.registerDownload()
	s.registerCast()
	return s
}

// SetNotifier installs the server notification callback.
func (s *Service) SetNotifier(fn func(method string, params any)) {
	s.notify = fn
}

// Notify sends a notification to the host (no-op before the server is ready).
func (s *Service) Notify(method string, params any) {
	if s.notify != nil {
		s.notify(method, params)
	}
}

func (s *Service) add(name, description string, handler jsonrpc.Handler) {
	s.methods[name] = Method{Name: name, Description: description, Handler: handler}
}

// Register adds every method (including one raw method per extracted endpoint)
// to the JSON-RPC server.
func (s *Service) Register(server *jsonrpc.Server) {
	s.mu.RLock()
	methods := make([]Method, 0, len(s.methods))
	for _, m := range s.methods {
		methods = append(methods, m)
	}
	s.mu.RUnlock()
	for _, m := range methods {
		server.Register(m.Name, m.Handler)
	}
	for _, ep := range bili.EndpointList {
		endpoint := ep
		server.Register("bili."+endpoint.Name, func(ctx context.Context, params json.RawMessage) (any, *jsonrpc.Error) {
			return s.handleEndpoint(ctx, endpoint, params)
		})
	}
}

// Methods returns the static method registry sorted by name.
func (s *Service) Methods() []Method {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Method, 0, len(s.methods))
	for _, m := range s.methods {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Stop shuts down background work.
func (s *Service) Stop() {
	s.streamMu.Lock()
	if s.streams != nil {
		s.streams.Close()
		s.streams = nil
	}
	s.streamMu.Unlock()
	s.downloadMu.Lock()
	if s.downloads != nil {
		s.downloads.Close()
		s.downloads = nil
	}
	s.downloadMu.Unlock()
	if s.opt.Live != nil {
		s.opt.Live.Close()
	}
	if s.opt.Stop != nil {
		s.opt.Stop()
	}
}
