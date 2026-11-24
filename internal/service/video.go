package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/cloudflypeng/bbcore/internal/bili"
	"github.com/cloudflypeng/bbcore/internal/jsonrpc"
)

func (s *Service) registerVideo() {
	s.add("video.recommend", "Web home feed (params: ps, fresh_idx, desktop)", s.videoRecommend)
	s.add("video.recommend_app", "App home feed (params: fresh_idx)", s.videoRecommendApp)
	s.add("video.hot", "Popular videos (params: pn, ps)", s.videoHot)
	s.add("video.rank", "Ranking list (params: rid, type)", s.videoRank)
	s.add("video.info", "Video detail (params: bvid or aid)", s.videoInfo)
	s.add("video.playurl", "UGC play URL (params: bvid/aid, cid, qn)", s.videoPlayURL)
	s.add("video.play_info", "Player info incl. subtitles (params: bvid/aid, cid, season_id, ep_id)", s.videoPlayInfo)
	s.add("video.related", "Related videos (params: bvid or aid)", s.videoRelated)
	s.add("video.relation", "Like/coin/fav state (params: bvid/aid)", s.videoRelation)
	s.add("video.tags", "Video tags (params: bvid)", s.videoTags)
	s.add("video.online", "Online viewer count (params: bvid/aid, cid)", s.videoOnline)
	s.add("video.like", "Like/unlike (params: bvid/aid, like)", s.videoLike)
	s.add("video.coin", "Add coins (params: bvid/aid, multiply, select_like)", s.videoCoin)
	s.add("video.triple", "One-click triple (params: bvid/aid)", s.videoTriple)
	s.add("video.dislike", "Dislike/undo (params: bvid/aid, dislike)", s.videoDislike)
	s.add("video.ai_conclusion", "AI summary (params: bvid/aid, cid, up_mid)", s.videoAIConclusion)
}

func (s *Service) videoRecommend(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	ps := p.IntDefault("ps", 15)
	freshIdx := p.IntDefault("fresh_idx", 0)
	var query url.Values
	if p.Bool("desktop", false) {
		freshType := int64(4)
		if freshIdx == 0 {
			freshType = 0
		}
		query = values(map[string]any{
			"plat": 1, "feed_version": "V1", "ps": ps, "fresh_idx": freshIdx,
			"fresh_type": freshType, "x_num": 3, "y_num": 4,
			"pc_wx_install": 0, "pc_wx_gm_install": 0,
		})
	} else {
		query = values(map[string]any{
			"version": 1, "feed_version": "V8", "homepage_ver": 1,
			"ps": ps, "fresh_idx": freshIdx, "brush": freshIdx, "fresh_type": 4,
		})
	}
	return s.endpointQuery(ctx, "recommendListWeb", p, query)
}

func (s *Service) videoRecommendApp(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	freshIdx := p.IntDefault("fresh_idx", 0)
	pull := "false"
	if freshIdx == 0 {
		pull = "true"
	}
	query := values(map[string]any{
		"build": 8430300, "c_locale": "zh_CN", "channel": "master", "column": 2,
		"device": "phone", "device_name": "android", "device_type": 0,
		"disable_rcmd": 0, "flush": 8, "fnval": 976, "fnver": 0, "force_host": 2,
		"fourk": 1, "guidance": 1, "https_url_req": 1, "idx": freshIdx,
		"mobi_app": "android_i", "network": "wifi", "platform": "android",
		"player_net": 1, "pull": pull, "qn": 32, "recsys_mode": 0,
		"s_locale": "zh_CN", "splash_id": "", "statistics": bili.StatisticsApp,
		"voice_balance": 0,
	})
	cred, credErr := s.credential(accountRef(p))
	if credErr != nil {
		return nil, credErr
	}
	fp := "1111111111111111111111111111111111111111111111111111111111111111"
	return s.endpointRequest(ctx, "recommendListApp", requestOptions{
		account: accountRef(p),
		query:   query,
		headers: map[string]string{
			"buvid":            cred.Buvid(),
			"fp_local":         fp,
			"fp_remote":        fp,
			"session_id":       "11111111",
			"bili-http-engine": "cronet",
			"User-Agent":       bili.UserAgentApp,
		},
	})
}

func (s *Service) videoHot(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	return s.endpointQuery(ctx, "hotList", p, values(map[string]any{
		"pn": p.IntDefault("pn", 1), "ps": p.IntDefault("ps", 20),
	}))
}

func (s *Service) videoRank(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	return s.endpointQuery(ctx, "getRankApi", p, values(map[string]any{
		"rid": p.IntDefault("rid", 0), "type": p.StringDefault("type", "all"),
	}))
}

func (s *Service) videoInfo(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	query := url.Values{}
	if bvid := p.String("bvid"); bvid != "" {
		query.Set("bvid", bvid)
	} else if aid, ok := p.ID("aid"); ok {
		query.Set("aid", stringify(aid))
	} else {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "bvid or aid is required")
	}
	return s.endpointQuery(ctx, "videoIntro", p, query)
}

func (s *Service) videoPlayURL(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	cid, ok := p.ID("cid")
	if !ok || cid <= 0 {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "cid is required")
	}
	query := values(map[string]any{
		"cid": cid, "qn": p.IntDefault("qn", 80), "fnval": p.IntDefault("fnval", 4048),
		"fourk": p.IntDefault("fourk", 1), "fnver": p.IntDefault("fnver", 0),
		"voice_balance": 0, "gaia_source": "pre-load", "isGaiaAvoided": "true",
		"web_location": 1315873, "try_look": 1, "dm_img_list": "[]",
		"dm_img_str": randomB64(16), "dm_cover_img_str": randomB64(32),
		"dm_img_inter": `{"ds":[],"wh":[0,0,0],"of":[0,0,0]}`,
	})
	if bvid := p.String("bvid"); bvid != "" {
		query.Set("bvid", bvid)
	}
	if aid, ok := p.ID("aid"); ok && aid > 0 {
		query.Set("avid", stringify(aid))
	}
	if seasonID, ok := p.ID("season_id"); ok && seasonID > 0 {
		query.Set("season_id", stringify(seasonID))
	}
	if epID, ok := p.ID("ep_id"); ok && epID > 0 {
		query.Set("ep_id", stringify(epID))
	}
	if language := p.String("language"); language != "" {
		query.Set("cur_language", language)
	}
	return s.endpointQuery(ctx, "ugcUrl", p, query)
}

func (s *Service) videoPlayInfo(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	query := url.Values{}
	for _, key := range []string{"aid", "bvid", "cid", "season_id", "ep_id"} {
		if v, ok := p.Raw(key); ok && v != nil {
			query.Set(key, stringify(v))
		}
	}
	if !query.Has("cid") {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "cid is required")
	}
	return s.endpointQuery(ctx, "playInfo", p, query)
}

func (s *Service) videoRelated(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	query := url.Values{}
	if bvid := p.String("bvid"); bvid != "" {
		query.Set("bvid", bvid)
	} else if aid, ok := p.ID("aid"); ok && aid > 0 {
		query.Set("aid", stringify(aid))
	} else {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "bvid or aid is required")
	}
	return s.endpointQuery(ctx, "relatedList", p, query)
}

func (s *Service) videoRelation(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	bvid := p.String("bvid")
	aid, ok := p.ID("aid")
	if bvid == "" && (!ok || aid <= 0) {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "bvid or aid is required")
	}
	if aid <= 0 && bvid != "" {
		resolved, err := s.resolveAid(ctx, accountRef(p), bvid, 0)
		if err != nil {
			return nil, err
		}
		aid = resolved
	}
	query := url.Values{"aid": {stringify(aid)}}
	if bvid != "" {
		query.Set("bvid", bvid)
	}
	return s.endpointQuery(ctx, "videoRelation", p, query)
}

func (s *Service) videoTags(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	bvid := p.String("bvid")
	if bvid == "" {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "bvid is required")
	}
	return s.endpointQuery(ctx, "videoTags", p, url.Values{"bvid": {bvid}})
}

func (s *Service) videoOnline(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	query := url.Values{}
	for _, key := range []string{"aid", "bvid", "cid"} {
		if v, ok := p.Raw(key); ok && v != nil {
			query.Set(key, stringify(v))
		}
	}
	return s.endpointQuery(ctx, "onlineTotal", p, query)
}

func (s *Service) videoLike(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	aid, err := s.requireAid(ctx, p)
	if err != nil {
		return nil, err
	}
	like := "0"
	if p.Bool("like", true) {
		like = "1"
	}
	return s.endpointRequest(ctx, "likeVideo", requestOptions{
		account: accountRef(p),
		method:  http.MethodPost,
		form:    url.Values{"aid": {stringify(aid)}, "like": {like}},
	})
}

func (s *Service) videoCoin(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	aid, err := s.requireAid(ctx, p)
	if err != nil {
		return nil, err
	}
	return s.endpointRequest(ctx, "coinVideo", requestOptions{
		account: accountRef(p),
		method:  http.MethodPost,
		form: url.Values{
			"aid":         {stringify(aid)},
			"multiply":    {stringify(p.IntDefault("multiply", 1))},
			"select_like": {stringify(p.IntDefault("select_like", 0))},
		},
	})
}

func (s *Service) videoTriple(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	aid, err := s.requireAid(ctx, p)
	if err != nil {
		return nil, err
	}
	return s.endpointRequest(ctx, "ugcTriple", requestOptions{
		account: accountRef(p),
		method:  http.MethodPost,
		form: url.Values{
			"aid":        {stringify(aid)},
			"eab_x":      {"2"},
			"ramval":     {"0"},
			"source":     {"web_normal"},
			"ga":         {"1"},
			"spmid":      {"333.788.0.0"},
			"statistics": {`{"appId":100,"platform":5}`},
		},
	})
}

func (s *Service) videoDislike(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	aid, err := s.requireAid(ctx, p)
	if err != nil {
		return nil, err
	}
	dislike := "0"
	if p.Bool("dislike", true) {
		dislike = "1"
	}
	return s.endpointRequest(ctx, "dislikeVideo", requestOptions{
		account: accountRef(p),
		method:  http.MethodPost,
		form:    url.Values{"aid": {stringify(aid)}, "dislike": {dislike}},
	})
}

func (s *Service) videoAIConclusion(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	aid, err := s.requireAid(ctx, p)
	if err != nil {
		return nil, err
	}
	query := url.Values{"aid": {stringify(aid)}}
	if cid, ok := p.ID("cid"); ok && cid > 0 {
		query.Set("cid", stringify(cid))
	}
	if upMid, ok := p.ID("up_mid"); ok && upMid > 0 {
		query.Set("up_mid", stringify(upMid))
	}
	return s.endpointQuery(ctx, "aiConclusion", p, query)
}

func (s *Service) requireAid(ctx context.Context, p jsonrpc.Params) (int64, *jsonrpc.Error) {
	aid, ok := p.ID("aid")
	bvid := p.String("bvid")
	if ok && aid > 0 {
		return aid, nil
	}
	if bvid == "" {
		return 0, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "bvid or aid is required")
	}
	return s.resolveAid(ctx, accountRef(p), bvid, 0)
}
