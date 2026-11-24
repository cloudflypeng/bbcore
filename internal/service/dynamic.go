package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/cloudflypeng/bbcore/internal/bili"
	"github.com/cloudflypeng/bbcore/internal/jsonrpc"
)

func (s *Service) registerDynamic() {
	s.add("dynamic.feed", "Following feed (params: type=all|video|pgc|up, offset, host_mid)", s.dynamicFeed)
	s.add("dynamic.space", "User dynamics (params: mid, offset)", s.dynamicSpace)
	s.add("dynamic.detail", "Dynamic detail (params: id, rid, type)", s.dynamicDetail)
	s.add("dynamic.like", "Like/unlike a dynamic (params: id, up)", s.dynamicLike)
	s.add("dynamic.opus", "Opus detail (params: id)", s.dynamicOpus)
}

func (s *Service) dynamicFeed(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	query := url.Values{"features": {bili.DynFeatures}}
	if hostMid, ok := p.ID("host_mid"); ok && hostMid > 0 {
		query.Set("host_mid", stringify(hostMid))
	} else {
		query.Set("type", p.StringDefault("type", "all"))
	}
	if offset := p.String("offset"); offset != "" {
		query.Set("offset", offset)
	}
	return s.endpointQuery(ctx, "followDynamic", p, query)
}

func (s *Service) dynamicSpace(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	mid, ok := p.ID("mid")
	if !ok || mid <= 0 {
		mid, ok = p.ID("host_mid")
	}
	if !ok || mid <= 0 {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "mid is required")
	}
	query := values(map[string]any{
		"offset": p.StringDefault("offset", ""), "host_mid": mid,
		"timezone_offset": -480, "features": bili.DynFeatures, "platform": "web",
		"web_location": "333.1387", "dm_img_list": "[]",
		"dm_img_str": randomB64(16), "dm_cover_img_str": randomB64(32),
		"dm_img_inter":           `{"ds":[],"wh":[0,0,0],"of":[0,0,0]}`,
		"x-bili-device-req-json": `{"platform":"web","device":"pc","spmid":"333.1387"}`,
	})
	return s.endpointRequest(ctx, "memberDynamic", requestOptions{
		account: accountRef(p),
		query:   query,
		headers: map[string]string{
			"origin":  "https://space.bilibili.com",
			"referer": "https://space.bilibili.com/" + stringify(mid) + "/dynamic",
		},
	})
}

func (s *Service) dynamicDetail(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	query := values(map[string]any{
		"timezone_offset": -480, "features": bili.DynDetailFeatures,
		"gaia_source": "Athena", "web_location": "333.1330",
		"x-bili-device-req-json": `{"platform":"web","device":"pc","spmid":"333.1330"}`,
	})
	for _, key := range []string{"id", "rid", "type"} {
		if v, ok := p.Raw(key); ok && v != nil {
			query.Set(key, stringify(v))
		}
	}
	if !query.Has("id") {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "id is required")
	}
	if csrf := s.csrfFor(accountRef(p)); csrf != "" {
		query.Set("csrf", csrf)
	}
	return s.endpointQuery(ctx, "dynamicDetail", p, query)
}

func (s *Service) dynamicLike(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	id := p.String("id")
	if id == "" {
		if v, ok := p.ID("id"); ok {
			id = stringify(v)
		}
	}
	if id == "" {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "id is required")
	}
	up := p.IntDefault("up", 1)
	if p.Has("like") && !p.Bool("like", true) {
		up = 2
	}
	query := url.Values{}
	if csrf := s.csrfFor(accountRef(p)); csrf != "" {
		query.Set("csrf", csrf)
	}
	return s.endpointRequest(ctx, "thumbDynamic", requestOptions{
		account: accountRef(p),
		method:  http.MethodPost,
		query:   query,
		form: url.Values{
			"dyn_id_str": {id},
			"up":         {stringify(up)},
			"spmid":      {"333.1365.0.0"},
		},
		headers: map[string]string{"referer": "https://t.bilibili.com"},
	})
}

func (s *Service) dynamicOpus(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	query := values(map[string]any{
		"timezone_offset": -480, "features": bili.OpusDetailFeatures,
		"web_location":           "333.1387",
		"x-bili-device-req-json": `{"platform":"web","device":"pc","spmid":"333.1387"}`,
	})
	for _, key := range []string{"id", "rid", "type"} {
		if v, ok := p.Raw(key); ok && v != nil {
			query.Set(key, stringify(v))
		}
	}
	if !query.Has("id") {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "id is required")
	}
	return s.endpointQuery(ctx, "opusDetail", p, query)
}
