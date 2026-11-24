package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/cloudflypeng/bbcore/internal/bili"
	"github.com/cloudflypeng/bbcore/internal/jsonrpc"
)

func (s *Service) registerMisc() {
	s.add("misc.sponsor_segments", "SponsorBlock segments (params: bvid, cid)", s.miscSponsorSegments)
	s.add("misc.match_info", "Esports match info for a video (params: cid)", s.miscMatchInfo)
	s.add("misc.music_detail", "BGM detail (params: music_id)", s.miscMusicDetail)
	s.add("misc.music_recommend", "BGM recommendations (params: music_id)", s.miscMusicRecommend)
	s.add("misc.music_wish", "Like/unlike a BGM (params: music_id, like)", s.miscMusicWish)
	s.add("misc.video_note", "Video notes (params: oid=aid, pn, ps, uper_mid)", s.miscVideoNote)
}

func (s *Service) miscSponsorSegments(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	bvid := p.String("bvid")
	cid, cidOK := p.ID("cid")
	if bvid == "" || !cidOK || cid <= 0 {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "bvid and cid are required")
	}
	spec := bili.Spec{
		Base:      bili.BaseSponsorBlock,
		Path:      "/api/skipSegments",
		Method:    http.MethodGet,
		Sign:      bili.SignNone,
		NoCookies: true,
		Query: url.Values{
			"videoID": {bvid},
			"cid":     {stringify(cid)},
		},
		Headers: map[string]string{
			"origin":        "bbcore",
			"x-ext-version": Version,
		},
	}
	return s.rawRequest(ctx, spec, "anon", p.Bool("meta", false))
}

func (s *Service) miscMatchInfo(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	cid, ok := p.ID("cid")
	if !ok || cid <= 0 {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "cid is required")
	}
	return s.endpointQuery(ctx, "matchInfo", p, values(map[string]any{
		"cid": cid, "platform": 2,
	}))
}

func (s *Service) miscMusicDetail(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	musicID := p.String("music_id")
	if musicID == "" {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "music_id is required")
	}
	return s.endpointQuery(ctx, "bgmDetail", p, url.Values{
		"music_id": {musicID}, "relation_from": {"bgm_page"},
	})
}

func (s *Service) miscMusicRecommend(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	musicID := p.String("music_id")
	if musicID == "" {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "music_id is required")
	}
	return s.endpointQuery(ctx, "bgmRecommend", p, url.Values{"music_id": {musicID}})
}

func (s *Service) miscMusicWish(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	musicID := p.String("music_id")
	if musicID == "" {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "music_id is required")
	}
	state := int64(1)
	if p.Bool("like", true) {
		state = 2
	}
	return s.endpointRequest(ctx, "wishUpdate", requestOptions{
		account: accountRef(p),
		method:  http.MethodPost,
		form: url.Values{
			"music_id": {musicID},
			"state":    {stringify(state)},
		},
	})
}

func (s *Service) miscVideoNote(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	oid, ok := p.ID("oid")
	if !ok || oid <= 0 {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "oid is required")
	}
	query := values(map[string]any{
		"oid": oid, "oid_type": 0, "pn": p.IntDefault("pn", 1), "ps": p.IntDefault("ps", 10),
	})
	if upMid, ok := p.ID("uper_mid"); ok && upMid > 0 {
		query.Set("uper_mid", stringify(upMid))
	}
	if csrf := s.csrfFor(accountRef(p)); csrf != "" {
		query.Set("csrf", csrf)
	}
	return s.endpointQuery(ctx, "archiveNoteList", p, query)
}
