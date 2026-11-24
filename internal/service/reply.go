package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/cloudflypeng/bbcore/internal/bili"
	"github.com/cloudflypeng/bbcore/internal/jsonrpc"
)

func (s *Service) registerReply() {
	s.add("reply.list", "Comment list (params: oid=aid, type=1, pn, ps, sort) or main/pagination mode with next", s.replyList)
	s.add("reply.replies", "Replies to a comment (params: oid, root, type, pn, sort)", s.replyReplies)
	s.add("reply.add", "Post a comment (params: oid, type, message, root, parent)", s.replyAdd)
	s.add("reply.delete", "Delete a comment (params: oid, type, rpid)", s.replyDelete)
	s.add("reply.like", "Like/unlike a comment (params: oid, type, rpid, action)", s.replyLike)
	s.add("reply.hate", "Dislike/undo a comment (params: oid, type, rpid, action)", s.replyHate)
	s.add("reply.top", "Pin/unpin a comment (params: oid, type, rpid)", s.replyTop)
	s.add("reply.report", "Report a comment (params: oid, type, rpid, reason)", s.replyReport)
}

func (s *Service) replyList(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	oid, ok := p.ID("oid")
	if !ok || oid <= 0 {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "oid is required")
	}
	replyType := p.StringDefault("type", "1")
	mode := p.IntDefault("mode", 0)
	if p.Has("next") || p.Has("pagination_str") || p.Bool("main", false) || mode > 0 {
		if mode == 0 {
			mode = p.IntDefault("sort", 0) + 2
		}
		pagination := p.String("pagination_str")
		if pagination == "" {
			next := p.String("next")
			encoded, _ := json.Marshal(next)
			pagination = `{"offset":` + string(encoded) + `}`
		}
		spec := bili.Spec{
			Base:   bili.BaseAPI,
			Method: http.MethodGet,
			Path:   "/x/v2/reply/wbi/main",
			Query: url.Values{
				"oid":            {stringify(oid)},
				"type":           {replyType},
				"pagination_str": {pagination},
				"mode":           {stringify(mode)},
			},
			Sign: bili.SignWBI,
		}
		return s.rawRequest(ctx, spec, accountRef(p), p.Bool("meta", false))
	}
	query := url.Values{
		"oid":  {stringify(oid)},
		"type": {replyType},
		"sort": {stringify(p.IntDefault("sort", 0))},
		"pn":   {stringify(p.IntDefault("pn", 1))},
		"ps":   {stringify(p.IntDefault("ps", 20))},
	}
	return s.endpointQuery(ctx, "replyList", p, query)
}

func (s *Service) replyReplies(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	oid, ok := p.ID("oid")
	root, rootOK := p.ID("root")
	if !ok || oid <= 0 || !rootOK || root <= 0 {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "oid and root are required")
	}
	query := url.Values{
		"oid":  {stringify(oid)},
		"root": {stringify(root)},
		"type": {p.StringDefault("type", "1")},
		"pn":   {stringify(p.IntDefault("pn", 1))},
		"sort": {stringify(p.IntDefault("sort", 1))},
	}
	if csrf := s.csrfFor(accountRef(p)); csrf != "" {
		query.Set("csrf", csrf)
	}
	return s.endpointQuery(ctx, "replyReplyList", p, query)
}

func (s *Service) replyAdd(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	oid, ok := p.ID("oid")
	message := p.String("message")
	if !ok || oid <= 0 || message == "" {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "oid and message are required")
	}
	return s.endpointRequest(ctx, "replyAdd", requestOptions{
		account: accountRef(p),
		method:  http.MethodPost,
		form: url.Values{
			"oid":     {stringify(oid)},
			"type":    {p.StringDefault("type", "1")},
			"message": {message},
			"root":    {stringify(p.IntDefault("root", 0))},
			"parent":  {stringify(p.IntDefault("parent", 0))},
		},
	})
}

func (s *Service) replyDelete(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	oid, ok := p.ID("oid")
	rpid, rpidOK := p.ID("rpid")
	if !ok || oid <= 0 || !rpidOK || rpid <= 0 {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "oid and rpid are required")
	}
	return s.endpointRequest(ctx, "replyDel", requestOptions{
		account: accountRef(p),
		method:  http.MethodPost,
		form: url.Values{
			"oid":  {stringify(oid)},
			"type": {p.StringDefault("type", "1")},
			"rpid": {stringify(rpid)},
		},
	})
}

func (s *Service) replyLike(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	return s.replyAction(ctx, raw, "likeReply", "action")
}

func (s *Service) replyHate(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	return s.replyAction(ctx, raw, "hateReply", "action")
}

func (s *Service) replyAction(ctx context.Context, raw json.RawMessage, endpoint, actionKey string) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	oid, ok := p.ID("oid")
	rpid, rpidOK := p.ID("rpid")
	if !ok || oid <= 0 || !rpidOK || rpid <= 0 {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "oid and rpid are required")
	}
	action := p.IntDefault(actionKey, 1)
	return s.endpointRequest(ctx, endpoint, requestOptions{
		account: accountRef(p),
		method:  http.MethodPost,
		form: url.Values{
			"oid":    {stringify(oid)},
			"type":   {p.StringDefault("type", "1")},
			"rpid":   {stringify(rpid)},
			"action": {stringify(action)},
		},
	})
}

func (s *Service) replyTop(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	oid, ok := p.ID("oid")
	rpid, rpidOK := p.ID("rpid")
	if !ok || oid <= 0 || !rpidOK || rpid <= 0 {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "oid and rpid are required")
	}
	return s.endpointRequest(ctx, "replyTop", requestOptions{
		account: accountRef(p),
		method:  http.MethodPost,
		form: url.Values{
			"oid":  {stringify(oid)},
			"type": {p.StringDefault("type", "1")},
			"rpid": {stringify(rpid)},
		},
	})
}

func (s *Service) replyReport(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	oid, ok := p.ID("oid")
	rpid, rpidOK := p.ID("rpid")
	if !ok || oid <= 0 || !rpidOK || rpid <= 0 {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "oid and rpid are required")
	}
	return s.endpointRequest(ctx, "replyReport", requestOptions{
		account: accountRef(p),
		method:  http.MethodPost,
		form: url.Values{
			"oid":     {stringify(oid)},
			"type":    {p.StringDefault("type", "1")},
			"rpid":    {stringify(rpid)},
			"reason":  {stringify(p.IntDefault("reason", 0))},
			"content": {p.StringDefault("content", "")},
		},
	})
}
