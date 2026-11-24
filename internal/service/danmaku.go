package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/cloudflypeng/bbcore/internal/biligrpc"
	"github.com/cloudflypeng/bbcore/internal/jsonrpc"
)

func (s *Service) registerDanmaku() {
	s.add("danmaku.seg", "Danmaku segment via gRPC, decoded (params: cid, segment_index, type)", s.danmakuSeg)
	s.add("danmaku.seg_raw", "Danmaku segment via gRPC, raw protobuf (params: cid, segment_index, type)", s.danmakuSegRaw)
	s.add("danmaku.send", "Post a video danmaku (params: bvid, cid, msg, mode, progress, color)", s.danmakuSend)
	s.add("danmaku.like", "Like/unlike a danmaku (params: cid, id, like)", s.danmakuLike)
	s.add("danmaku.report", "Report a danmaku (params: cid, id, reason, content)", s.danmakuReport)
	s.add("danmaku.recall", "Recall your danmaku (params: cid, id)", s.danmakuRecall)
}

func (s *Service) danmakuSeg(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	if s.opt.Grpc == nil {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInternalError, "grpc client is not available")
	}
	cid, ok := p.ID("cid")
	if !ok || cid <= 0 {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "cid is required")
	}
	segment := p.IntDefault("segment_index", 1)
	danmakuType := p.IntDefault("type", 1)
	cred, credErr := s.credential(accountRef(p))
	if credErr != nil {
		return nil, credErr
	}
	res, err := s.opt.Grpc.Call(ctx, biligrpc.CallSpec{
		Path:       biligrpc.DmSegMobilePath,
		Payload:    biligrpc.EncodeDmSegMobileReq(cid, segment, danmakuType),
		Credential: cred,
	})
	if err != nil {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInternalError, "%v", err)
	}
	if res.GrpcStatus != 0 {
		return map[string]any{
			"grpc_status":  res.GrpcStatus,
			"grpc_message": res.GrpcMessage,
			"cid":          cid,
			"segment":      segment,
		}, nil
	}
	decoded, decodeErr := biligrpc.DecodeDmSegMobileReply(res.Message)
	if decodeErr != nil {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInternalError, "decode danmaku reply: %v", decodeErr)
	}
	return decoded, nil
}

func (s *Service) danmakuSegRaw(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	if s.opt.Grpc == nil {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInternalError, "grpc client is not available")
	}
	cid, ok := p.ID("cid")
	if !ok || cid <= 0 {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "cid is required")
	}
	cred, credErr := s.credential(accountRef(p))
	if credErr != nil {
		return nil, credErr
	}
	segment := p.IntDefault("segment_index", 1)
	res, err := s.opt.Grpc.Call(ctx, biligrpc.CallSpec{
		Path:       biligrpc.DmSegMobilePath,
		Payload:    biligrpc.EncodeDmSegMobileReq(cid, segment, p.IntDefault("type", 1)),
		Credential: cred,
	})
	if err != nil {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInternalError, "%v", err)
	}
	return map[string]any{
		"status":         res.Status,
		"grpc_status":    res.GrpcStatus,
		"grpc_message":   res.GrpcMessage,
		"message_base64": base64.StdEncoding.EncodeToString(res.Message),
	}, nil
}

func (s *Service) danmakuSend(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	cid, ok := p.ID("cid")
	bvid := p.String("bvid")
	msg := p.String("msg")
	if !ok || cid <= 0 || bvid == "" || msg == "" {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "cid, bvid and msg are required")
	}
	color := p.IntDefault("color", 16777215)
	colorful := p.Bool("colorful", false)
	if colorful {
		color = 16777215
	}
	form := values(map[string]any{
		"type": 1, "oid": cid, "msg": msg,
		"mode":     p.IntDefault("mode", 1),
		"bvid":     bvid,
		"progress": p.IntDefault("progress", 0),
		"color":    color,
		"fontsize": p.IntDefault("fontsize", 25),
		"pool":     p.IntDefault("pool", 0),
		"rnd":      time.Now().UnixMicro(),
	})
	if colorful {
		form.Set("colorful", "60001")
	}
	if p.Has("checkbox_type") {
		form.Set("checkbox_type", strconv.FormatInt(p.IntDefault("checkbox_type", 0), 10))
	}
	if aid, ok := p.ID("aid"); ok && aid > 0 {
		form.Set("aid", strconv.FormatInt(aid, 10))
	}
	return s.endpointRequest(ctx, "shootDanmaku", requestOptions{
		account: accountRef(p),
		method:  http.MethodPost,
		form:    form,
	})
}

func (s *Service) danmakuLike(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	cid, ok := p.ID("cid")
	id, idOK := p.ID("id")
	if !ok || cid <= 0 || !idOK || id <= 0 {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "cid and id are required")
	}
	op := int64(1)
	if !p.Bool("like", true) {
		op = 2
	}
	return s.endpointRequest(ctx, "danmakuLike", requestOptions{
		account: accountRef(p),
		method:  http.MethodPost,
		form: url.Values{
			"op":               {strconv.FormatInt(op, 10)},
			"dmid":             {strconv.FormatInt(id, 10)},
			"oid":              {strconv.FormatInt(cid, 10)},
			"platform":         {"web_player"},
			"polaris_app_id":   {"100"},
			"polaris_platform": {"5"},
			"spmid":            {"333.788.0.0"},
			"from_spmid":       {"333.788.0.0"},
			"statistics":       {`{"appId":100,"platform":5,"abtest":"","version":""}`},
		},
	})
}

func (s *Service) danmakuReport(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	cid, ok := p.ID("cid")
	id, idOK := p.ID("id")
	if !ok || cid <= 0 || !idOK || id <= 0 {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "cid and id are required")
	}
	form := url.Values{
		"cid":              {strconv.FormatInt(cid, 10)},
		"dmid":             {strconv.FormatInt(id, 10)},
		"reason":           {strconv.FormatInt(p.IntDefault("reason", 0), 10)},
		"block":            {strconv.FormatBool(p.Bool("block", false))},
		"originCid":        {strconv.FormatInt(cid, 10)},
		"polaris_app_id":   {"100"},
		"polaris_platform": {"5"},
		"spmid":            {"333.788.0.0"},
		"from_spmid":       {"333.788.0.0"},
		"statistics":       {`{"appId":100,"platform":5,"abtest":"","version":""}`},
	}
	if content := p.String("content"); content != "" {
		form.Set("content", content)
	}
	return s.endpointRequest(ctx, "danmakuReport", requestOptions{
		account: accountRef(p),
		method:  http.MethodPost,
		form:    form,
	})
}

func (s *Service) danmakuRecall(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	cid, ok := p.ID("cid")
	id, idOK := p.ID("id")
	if !ok || cid <= 0 || !idOK || id <= 0 {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "cid and id are required")
	}
	return s.endpointRequest(ctx, "danmakuRecall", requestOptions{
		account: accountRef(p),
		method:  http.MethodPost,
		form: url.Values{
			"dmid": {strconv.FormatInt(id, 10)},
			"cid":  {strconv.FormatInt(cid, 10)},
			"type": {"1"},
		},
	})
}
