package service

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/cloudflypeng/bbcore/internal/bili"
	"github.com/cloudflypeng/bbcore/internal/jsonrpc"
)

func (s *Service) registerCore() {
	s.add("core.ping", "Health check", func(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
		return map[string]any{"pong": true, "time": time.Now().UnixMilli()}, nil
	})
	s.add("core.version", "Protocol and build version", func(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
		return map[string]any{
			"name":     "bbcore",
			"version":  Version,
			"protocol": 1,
		}, nil
	})
	s.add("core.info", "Runtime info (accounts, live rooms, counts)", s.coreInfo)
	s.add("core.methods", "List RPC methods (params: prefix, include_endpoints)", s.coreMethods)
	s.add("core.endpoints", "List extracted endpoint constants (params: q, base)", s.coreEndpoints)
	s.add("core.endpoint", "Describe one endpoint (params: name)", s.coreEndpoint)
	s.add("core.shutdown", "Stop background work and exit", s.coreShutdown)
}

func (s *Service) coreInfo(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	accounts := s.opt.Store.List()
	activeMid := s.opt.Store.ActiveMid()
	activeName := ""
	for _, acc := range accounts {
		if acc.Mid == activeMid {
			activeName = acc.Name
		}
	}
	out := map[string]any{
		"name":        "bbcore",
		"version":     Version,
		"protocol":    1,
		"data_dir":    s.opt.DataDir,
		"accounts":    len(accounts),
		"active_mid":  activeMid,
		"active_name": activeName,
		"endpoints":   len(bili.EndpointList),
		"methods":     len(s.Methods()) + len(bili.EndpointList),
		"notifications": []string{
			"event.live.connected",
			"event.live.disconnected",
			"event.live.popularity",
			"event.live.message",
			"event.account.login",
			"event.account.logout",
			"event.account.removed",
		},
	}
	if s.opt.Live != nil {
		out["live_rooms"] = s.opt.Live.Rooms()
	}
	return out, nil
}

func (s *Service) coreMethods(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	prefix := strings.ToLower(p.String("prefix"))
	out := []map[string]any{}
	for _, m := range s.Methods() {
		if prefix != "" && !strings.HasPrefix(strings.ToLower(m.Name), prefix) {
			continue
		}
		out = append(out, map[string]any{"name": m.Name, "description": m.Description})
	}
	if p.Bool("include_endpoints", false) {
		for _, ep := range bili.EndpointList {
			name := "bili." + ep.Name
			if prefix != "" && !strings.HasPrefix(strings.ToLower(name), prefix) {
				continue
			}
			out = append(out, map[string]any{
				"name":        name,
				"description": "Raw endpoint " + ep.URL,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return stringify(out[i]["name"]) < stringify(out[j]["name"])
	})
	return out, nil
}

func (s *Service) coreEndpoints(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	query := strings.ToLower(p.String("q"))
	base := strings.ToLower(p.String("base"))
	out := []bili.Endpoint{}
	for _, ep := range bili.EndpointList {
		if base != "" && strings.ToLower(ep.Base) != base {
			continue
		}
		if query != "" {
			haystack := strings.ToLower(ep.Name + " " + ep.URL)
			if !strings.Contains(haystack, query) {
				continue
			}
		}
		out = append(out, ep)
	}
	return out, nil
}

func (s *Service) coreEndpoint(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	name := strings.TrimPrefix(strings.TrimSpace(p.String("name")), "bili.")
	ep, ok := bili.Endpoints[name]
	if !ok {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "unknown endpoint %q", name)
	}
	return ep, nil
}

func (s *Service) coreShutdown(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	go s.Stop()
	return map[string]any{"ok": true}, nil
}
