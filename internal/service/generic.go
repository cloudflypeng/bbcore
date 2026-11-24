package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/cloudflypeng/bbcore/internal/bili"
	"github.com/cloudflypeng/bbcore/internal/jsonrpc"
)

func (s *Service) registerAPI() {
	s.add("api.call", "Generic signed HTTP call (base/url/path + query/form/json)", s.apiCall)
	s.add("api.endpoint", "Call a named endpoint from the registry", s.apiEndpoint)
}

func (s *Service) apiEndpoint(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	name := strings.TrimPrefix(strings.TrimSpace(p.String("name")), "bili.")
	if name == "" {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "name is required")
	}
	return s.handleEndpointCall(ctx, name, p)
}

func (s *Service) apiCall(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	path := p.String("path")
	target := p.String("url")
	if target == "" && path == "" {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "path or url is required")
	}
	base := strings.ToLower(p.StringDefault("base", bili.BaseAPI))
	spec := bili.Spec{
		Base:          base,
		Path:          target,
		Method:        p.String("method"),
		Query:         p.Query("query"),
		Form:          p.Query("form"),
		JSON:          p.Value("json"),
		Headers:       p.StringMap("headers"),
		Sign:          p.String("sign"),
		NoCookies:     p.Bool("no_cookies", false),
		SkipAccessKey: p.Bool("skip_access_key", false),
	}
	if spec.Path == "" {
		spec.Path = path
	}
	if spec.Sign == "" {
		spec.Sign = endpointSign(base, path)
	}
	if body := p.String("body_base64"); body != "" {
		decoded, err := base64.StdEncoding.DecodeString(body)
		if err != nil {
			return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "invalid body_base64: %v", err)
		}
		spec.Body = decoded
	}
	if ct := p.String("content_type"); ct != "" {
		spec.ContentType = ct
	}
	if spec.Method == "" {
		if spec.Form != nil || spec.JSON != nil || spec.Body != nil {
			spec.Method = http.MethodPost
		} else {
			spec.Method = http.MethodGet
		}
	}
	return s.rawRequest(ctx, spec, accountRef(p), p.Bool("meta", false))
}

// handleEndpoint implements the auto-generated bili.<name> methods.
func (s *Service) handleEndpoint(ctx context.Context, ep bili.Endpoint, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	return s.handleEndpointCall(ctx, ep.Name, p)
}

func (s *Service) handleEndpointCall(ctx context.Context, name string, p jsonrpc.Params) (any, *jsonrpc.Error) {
	if _, ok := bili.Endpoints[name]; !ok {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "unknown endpoint %q", name)
	}
	o := requestOptions{
		account:       accountRef(p),
		method:        p.String("method"),
		query:         p.Query("query"),
		form:          p.Query("form"),
		json:          p.Value("json"),
		headers:       p.StringMap("headers"),
		sign:          p.String("sign"),
		noCookies:     p.Bool("no_cookies", false),
		skipAccessKey: p.Bool("skip_access_key", false),
		meta:          p.Bool("meta", false),
	}
	if body := p.String("body_base64"); body != "" {
		decoded, err := base64.StdEncoding.DecodeString(body)
		if err != nil {
			return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "invalid body_base64: %v", err)
		}
		o.body = decoded
	}
	if ct := p.String("content_type"); ct != "" {
		o.headers = ensureMap(o.headers)
		o.headers["Content-Type"] = ct
	}
	return s.endpointRequest(ctx, name, o)
}

func ensureMap(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}
