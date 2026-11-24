package service

import (
	"context"
	"encoding/base64"
	"encoding/json"

	"github.com/cloudflypeng/bbcore/internal/biligrpc"
	"github.com/cloudflypeng/bbcore/internal/jsonrpc"
)

func (s *Service) registerGRPC() {
	s.add("grpc.call", "Raw unary gRPC call (params: path, payload_base64, headers, compress)", s.grpcCall)
	s.add("grpc.reply_main", "Main comment list over gRPC (params: oid, type, mode, offset, page_size)", s.grpcReplyMain)
}

func (s *Service) grpcCall(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	if s.opt.Grpc == nil {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInternalError, "grpc client is not available")
	}
	path := p.String("path")
	if path == "" {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "path is required")
	}
	payloadB64 := p.String("payload_base64")
	payload, err := base64.StdEncoding.DecodeString(payloadB64)
	if err != nil {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "invalid payload_base64: %v", err)
	}
	cred, credErr := s.credential(accountRef(p))
	if credErr != nil {
		return nil, credErr
	}
	spec := biligrpc.CallSpec{
		Path:       path,
		Payload:    payload,
		Credential: cred,
		Headers:    p.StringMap("headers"),
	}
	if p.Has("compress") {
		compress := p.Bool("compress", false)
		spec.Compress = &compress
	}
	res, callErr := s.opt.Grpc.Call(ctx, spec)
	if callErr != nil {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInternalError, "%v", callErr)
	}
	return grpcResult(res, p.Bool("meta", false)), nil
}

func (s *Service) grpcReplyMain(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	if s.opt.Grpc == nil {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInternalError, "grpc client is not available")
	}
	oid, ok := p.ID("oid")
	if !ok || oid <= 0 {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "oid is required")
	}
	cred, credErr := s.credential(accountRef(p))
	if credErr != nil {
		return nil, credErr
	}
	res, err := s.opt.Grpc.Call(ctx, biligrpc.CallSpec{
		Path: biligrpc.MainListPath,
		Payload: biligrpc.EncodeMainListReq(
			oid,
			p.IntDefault("type", 1),
			p.IntDefault("mode", 3),
			p.String("offset"),
			p.IntDefault("page_size", 20),
		),
		Credential: cred,
	})
	if err != nil {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInternalError, "%v", err)
	}
	return grpcResult(res, p.Bool("meta", false)), nil
}

func grpcResult(res *biligrpc.Result, meta bool) map[string]any {
	out := map[string]any{
		"status":         res.Status,
		"grpc_status":    res.GrpcStatus,
		"grpc_message":   res.GrpcMessage,
		"message_base64": base64.StdEncoding.EncodeToString(res.Message),
	}
	if meta {
		out["headers"] = flattenHeader(res.Header)
		out["trailers"] = flattenHeader(res.Trailers)
	}
	return out
}
