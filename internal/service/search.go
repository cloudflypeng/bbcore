package service

import (
	"context"
	"encoding/json"
	"net/url"

	"github.com/cloudflypeng/bbcore/internal/jsonrpc"
)

func (s *Service) registerSearch() {
	s.add("search.default", "Default search keyword", s.searchDefault)
	s.add("search.all", "Comprehensive search (params: keyword, page)", s.searchAll)
	s.add("search.type", "Typed search (params: keyword, search_type, page)", s.searchType)
	s.add("search.suggest", "Search suggestions (params: term)", s.searchSuggest)
	s.add("search.trending", "Trending search list (params: limit)", s.searchTrending)
	s.add("search.hot", "Hot search words", s.searchHot)
	s.add("search.recommend", "App search recommendations", s.searchRecommend)
}

func (s *Service) searchDefault(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	return s.endpointQuery(ctx, "searchDefault", p, url.Values{"web_location": {"333.1365"}})
}

func (s *Service) searchAll(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	keyword := p.String("keyword")
	if keyword == "" {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "keyword is required")
	}
	query := values(map[string]any{
		"keyword": keyword, "page": p.IntDefault("page", 1),
		"page_size": p.IntDefault("page_size", 20), "platform": "pc",
		"web_location": 1430654,
	})
	if order := p.String("order"); order != "" {
		query.Set("order", order)
	}
	return s.endpointQuery(ctx, "searchAll", p, query)
}

func (s *Service) searchType(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	keyword := p.String("keyword")
	if keyword == "" {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "keyword is required")
	}
	searchType := p.StringDefault("search_type", "video")
	query := values(map[string]any{
		"search_type": searchType, "keyword": keyword,
		"page": p.IntDefault("page", 1), "page_size": p.IntDefault("page_size", 20),
		"platform": "pc", "web_location": 1430654,
	})
	for _, key := range []string{"order", "duration", "tids", "order_sort", "user_type", "category_id", "pubtime_begin_s", "pubtime_end_s", "gaia_vtoken"} {
		if v, ok := p.Raw(key); ok && v != nil {
			query.Set(key, stringify(v))
		}
	}
	res, err := s.endpointRequest(ctx, "searchByType", requestOptions{
		account: accountRef(p),
		query:   query,
		headers: map[string]string{
			"origin":  "https://search.bilibili.com",
			"referer": "https://search.bilibili.com/" + searchType + "?keyword=" + url.QueryEscape(keyword),
		},
	})
	return res, err
}

func (s *Service) searchSuggest(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	term := p.String("term")
	if term == "" {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "term is required")
	}
	return s.endpointQuery(ctx, "searchSuggest", p, values(map[string]any{
		"term": term, "highlight": 0, "spmid": 333.1365, "web_location": 333.1365,
	}))
}

func (s *Service) searchTrending(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	return s.endpointQuery(ctx, "searchTrending", p, url.Values{
		"limit": {stringify(p.IntDefault("limit", 30))},
	})
}

func (s *Service) searchHot(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	return s.endpointQuery(ctx, "hotSearchList", p, nil)
}

func (s *Service) searchRecommend(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	return s.endpointRequest(ctx, "searchRecommend", requestOptions{
		account: accountRef(p),
		query: values(map[string]any{
			"build": 8430300, "channel": "master", "version": "8.43.0",
			"c_locale": "zh_CN", "mobi_app": "android", "platform": "android",
			"s_locale": "zh_CN", "from": 2,
		}),
	})
}
