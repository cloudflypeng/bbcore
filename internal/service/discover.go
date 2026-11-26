package service

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"

	"github.com/cloudflypeng/bbcore/internal/bili"
	"github.com/cloudflypeng/bbcore/internal/jsonrpc"
)

func (s *Service) registerDiscover() {
	s.add("video.weekly", "Weekly picks (params: number; omit for the issue list)", s.videoWeekly)
	s.add("article.rank", "Column article ranking (params: category_id, type, page, page_size)", s.articleRank)
	s.add("article.view", "Read a column article (params: article_id)", s.articleView)
	s.add("article.info", "Column article stats (params: article_id)", s.articleInfo)
	s.add("topic.detail", "Topic detail (params: topic_id)", s.topicDetail)
	s.add("topic.feed", "Topic dynamic feed (params: topic_id, offset, page_size)", s.topicFeed)
	s.add("topic.square", "Recommended topics (params: page, page_size)", s.topicSquare)
}

func (s *Service) videoWeekly(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	number, _ := p.ID("number")
	if number <= 0 {
		return s.endpointRequest(ctx, "popularSeriesList", requestOptions{account: accountRef(p)})
	}
	return s.endpointQuery(ctx, "popularSeriesOne", p, url.Values{
		"number": {strconv.FormatInt(number, 10)},
	})
}

func articleID(p jsonrpc.Params) int64 {
	if id, ok := p.ID("article_id"); ok && id > 0 {
		return id
	}
	id, _ := p.ID("id")
	return id
}

func (s *Service) articleRank(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	query := url.Values{
		"cid":  {stringify(p.IntDefault("category_id", 0))},
		"type": {stringify(p.IntDefault("type", 0))},
		"pn":   {stringify(p.IntDefault("page", 1))},
		"ps":   {stringify(p.IntDefault("page_size", 20))},
	}
	return s.rawRequest(ctx, bili.Spec{
		Base:  "api",
		Path:  "/x/article/rank/list",
		Sign:  bili.SignWBI,
		Query: query,
	}, accountRef(p), false)
}

func (s *Service) articleView(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	id := articleID(p)
	if id <= 0 {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "article_id is required")
	}
	return s.endpointQuery(ctx, "articleView", p, url.Values{
		"id": {strconv.FormatInt(id, 10)},
	})
}

func (s *Service) articleInfo(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	id := articleID(p)
	if id <= 0 {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "article_id is required")
	}
	return s.endpointQuery(ctx, "articleInfo", p, url.Values{
		"id": {strconv.FormatInt(id, 10)},
	})
}

func (s *Service) topicDetail(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	topicID, _ := p.ID("topic_id")
	if topicID <= 0 {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "topic_id is required")
	}
	return s.endpointQuery(ctx, "topicTop", p, url.Values{
		"topic_id": {strconv.FormatInt(topicID, 10)},
	})
}

func (s *Service) topicFeed(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	topicID, _ := p.ID("topic_id")
	if topicID <= 0 {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "topic_id is required")
	}
	query := url.Values{
		"topic_id":  {strconv.FormatInt(topicID, 10)},
		"page_size": {stringify(p.IntDefault("page_size", 20))},
	}
	if offset := strings.TrimSpace(p.String("offset")); offset != "" {
		query.Set("offset", offset)
	}
	if topicName := strings.TrimSpace(p.String("topic_name")); topicName != "" {
		query.Set("topic_name", topicName)
	}
	return s.endpointQuery(ctx, "topicFeed", p, query)
}

func (s *Service) topicSquare(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	query := url.Values{
		"page_size": {stringify(p.IntDefault("page_size", 20))},
	}
	if page := p.IntDefault("page", 1); page > 1 {
		query.Set("page", stringify(page))
	}
	return s.endpointQuery(ctx, "topicPubSearch", p, query)
}
