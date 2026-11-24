package service

import (
	"context"
	"encoding/json"

	"github.com/cloudflypeng/bbcore/internal/jsonrpc"
)

func (s *Service) registerMsg() {
	s.add("msg.replies", "Received replies/mentions feed (params: cursor, cursor_time)", s.msgReplies)
	s.add("msg.ats", "At-me feed (params: cursor, cursor_time)", s.msgAts)
	s.add("msg.likes", "Likes-me feed (params: cursor, cursor_time)", s.msgLikes)
	s.add("msg.like_detail", "Users who liked one item (params: card_id, pn, last_mid)", s.msgLikeDetail)
	s.add("msg.unread", "Unread counters", s.msgUnread)
	s.add("msg.system", "System notifications (params: cursor, page_size)", s.msgSystem)
	s.add("msg.update_cursor", "Mark system notifications read (params: cursor)", s.msgUpdateCursor)
}

func (s *Service) msgReplies(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	return s.endpointQuery(ctx, "msgFeedReply", p, values(map[string]any{
		"id": p.IntDefault("cursor", 0), "reply_time": p.IntDefault("cursor_time", 0),
		"platform": "web", "mobi_app": "web", "build": 0, "web_location": 333.40164,
	}))
}

func (s *Service) msgAts(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	return s.endpointQuery(ctx, "msgFeedAt", p, values(map[string]any{
		"id": p.IntDefault("cursor", 0), "at_time": p.IntDefault("cursor_time", 0),
		"platform": "web", "mobi_app": "web", "build": 0, "web_location": 333.40164,
	}))
}

func (s *Service) msgLikes(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	return s.endpointQuery(ctx, "msgFeedLike", p, values(map[string]any{
		"id": p.IntDefault("cursor", 0), "like_time": p.IntDefault("cursor_time", 0),
		"platform": "web", "mobi_app": "web", "build": 0, "web_location": 333.40164,
	}))
}

func (s *Service) msgLikeDetail(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	cardID := p.String("card_id")
	if cardID == "" {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "card_id is required")
	}
	return s.endpointQuery(ctx, "msgLikeDetail", p, values(map[string]any{
		"card_id": cardID, "pn": p.IntDefault("pn", 1),
		"last_mid": p.IntDefault("last_mid", 0),
		"platform": "web", "build": 0, "mobi_app": "web", "web_location": 333.40164,
	}))
}

func (s *Service) msgUnread(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	return s.endpointQuery(ctx, "msgFeedUnread", p, values(map[string]any{
		"build": 0, "mobi_app": "web", "web_location": 333.1365,
	}))
}

func (s *Service) msgSystem(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	return s.endpointQuery(ctx, "msgSysNotify", p, values(map[string]any{
		"cursor": p.IntDefault("cursor", 0), "page_size": p.IntDefault("page_size", 20),
		"mobi_app": "web", "build": 0, "web_location": 333.40164,
	}))
}

func (s *Service) msgUpdateCursor(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	query := values(map[string]any{"cursor": p.IntDefault("cursor", 0)})
	if csrf := s.csrfFor(accountRef(p)); csrf != "" {
		query.Set("csrf", csrf)
	}
	return s.endpointQuery(ctx, "msgSysUpdateCursor", p, query)
}
