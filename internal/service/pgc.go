package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/cloudflypeng/bbcore/internal/jsonrpc"
)

func (s *Service) registerPGC() {
	s.add("pgc.timeline", "Bangumi release calendar (params: types=1|3|4, before, after)", s.pgcTimeline)
	s.add("pgc.info", "Season/episode detail (params: season_id or ep_id)", s.pgcInfo)
	s.add("pgc.playurl", "Season/episode play URL (params: season_id|ep_id, cid, qn, try_look)", s.pgcPlayurl)
	s.add("pgc.rank", "PGC rank (params: day, season_type)", s.pgcRank)
	s.add("pgc.season_rank", "PGC season rank (params: day, season_type)", s.pgcSeasonRank)
	s.add("pgc.status", "Season watch status (params: season_id)", s.pgcStatus)
	s.add("pgc.review", "Short/long reviews (params: media_id, type=short|long, sort, cursor)", s.pgcReview)
	s.add("pgc.review_action", "Like/dislike a review (params: media_id, review_id, action=like|dislike)", s.pgcReviewAction)
	s.add("pgc.review_post", "Post a review (params: media_id, score, content, share_feed)", s.pgcReviewPost)
	s.add("pgc.review_modify", "Modify a review (params: media_id, review_id, score, content)", s.pgcReviewModify)
	s.add("pgc.review_delete", "Delete a review (params: media_id, review_id)", s.pgcReviewDelete)
	s.add("pgc.follow", "Follow/unfollow a season (params: season_id, follow)", s.pgcFollow)
}

func (s *Service) pgcTimeline(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	return s.endpointQuery(ctx, "pgcTimeline", p, values(map[string]any{
		"types":  p.IntDefault("types", 1),
		"before": p.IntDefault("before", 0),
		"after":  p.IntDefault("after", 0),
	}))
}

func (s *Service) pgcInfo(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	query := url.Values{}
	if seasonID, ok := p.ID("season_id"); ok && seasonID > 0 {
		query.Set("season_id", stringify(seasonID))
	}
	if epID, ok := p.ID("ep_id"); ok && epID > 0 {
		query.Set("ep_id", stringify(epID))
	}
	if len(query) == 0 {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "season_id or ep_id is required")
	}
	return s.endpointQuery(ctx, "pgcInfo", p, query)
}

func (s *Service) pgcPlayurl(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	query := values(map[string]any{
		"cid": p.IntDefault("cid", 0), "qn": p.IntDefault("qn", 64),
		"fnval": 4048, "fourk": 1, "fnver": 0,
		"voice_balance": 0, "gaia_source": "pre-load", "isGaiaAvoided": "true",
		"web_location": 1315873,
		"biz_type":     p.StringDefault("biz_type", "ogv"),
		"scene":        "normal", "from_client": "BROWSER",
		"drm_tech_type": 0, "app_id": 100,
		"dm_img_list":      "[]",
		"dm_img_str":       randomB64(16),
		"dm_cover_img_str": randomB64(32),
		"dm_img_inter":     `{"ds":[],"wh":[0,0,0],"of":[0,0,0]}`,
	})
	if p.Bool("try_look", false) {
		query.Set("try_look", "1")
	}
	if seasonID, ok := p.ID("season_id"); ok && seasonID > 0 {
		query.Set("season_id", stringify(seasonID))
	}
	if epID, ok := p.ID("ep_id"); ok && epID > 0 {
		query.Set("ep_id", stringify(epID))
	}
	if aid, ok := p.ID("aid"); ok && aid > 0 {
		query.Set("avid", stringify(aid))
	}
	if bvid := p.String("bvid"); bvid != "" {
		query.Set("bvid", bvid)
	}
	if lang := p.String("language"); lang != "" {
		query.Set("cur_language", lang)
	}
	if !query.Has("season_id") && !query.Has("ep_id") && !query.Has("avid") && !query.Has("bvid") {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "season_id, ep_id, aid or bvid is required")
	}
	return s.endpointRequest(ctx, "ogvUrl", requestOptions{
		account: accountRef(p),
		query:   query,
		headers: map[string]string{"referer": "https://www.bilibili.com/bangumi/"},
	})
}

func (s *Service) pgcRank(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	return s.endpointQuery(ctx, "pgcRank", p, values(map[string]any{
		"day": p.IntDefault("day", 3), "season_type": p.IntDefault("season_type", 1),
	}))
}

func (s *Service) pgcSeasonRank(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	return s.endpointQuery(ctx, "pgcSeasonRank", p, values(map[string]any{
		"day": p.IntDefault("day", 3), "season_type": p.IntDefault("season_type", 1),
	}))
}

func (s *Service) pgcStatus(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	seasonID, ok := p.ID("season_id")
	if !ok || seasonID <= 0 {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "season_id is required")
	}
	return s.endpointQuery(ctx, "seasonStatus", p, url.Values{"season_id": {stringify(seasonID)}})
}

func (s *Service) pgcReview(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	mediaID, ok := p.ID("media_id")
	if !ok || mediaID <= 0 {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "media_id is required")
	}
	endpoint := "pgcReviewS"
	if p.StringDefault("type", "short") == "long" {
		endpoint = "pgcReviewL"
	}
	query := values(map[string]any{
		"media_id": mediaID, "ps": p.IntDefault("ps", 20),
		"sort": p.IntDefault("sort", 0), "web_location": 666.19,
	})
	if cursor := p.String("cursor"); cursor != "" {
		query.Set("cursor", cursor)
	}
	return s.endpointQuery(ctx, endpoint, p, query)
}

func (s *Service) pgcReviewAction(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	mediaID, ok := p.ID("media_id")
	reviewID := p.String("review_id")
	if !ok || mediaID <= 0 || reviewID == "" {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "media_id and review_id are required")
	}
	endpoint := "pgcReviewLike"
	if p.StringDefault("action", "like") == "dislike" {
		endpoint = "pgcReviewDislike"
	}
	return s.endpointRequest(ctx, endpoint, requestOptions{
		account: accountRef(p),
		method:  http.MethodPost,
		form: url.Values{
			"media_id":    {stringify(mediaID)},
			"review_type": {"2"},
			"review_id":   {reviewID},
		},
	})
}

func (s *Service) pgcReviewPost(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	mediaID, ok := p.ID("media_id")
	content := p.String("content")
	if !ok || mediaID <= 0 || content == "" {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "media_id and content are required")
	}
	form := url.Values{
		"media_id": {stringify(mediaID)},
		"score":    {stringify(p.IntDefault("score", 10))},
		"content":  {content},
	}
	if p.Bool("share_feed", false) {
		form.Set("share_feed", "1")
	}
	return s.endpointRequest(ctx, "pgcReviewPost", requestOptions{
		account: accountRef(p),
		method:  http.MethodPost,
		form:    form,
	})
}

func (s *Service) pgcReviewModify(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	mediaID, ok := p.ID("media_id")
	reviewID := p.String("review_id")
	content := p.String("content")
	if !ok || mediaID <= 0 || reviewID == "" || content == "" {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "media_id, review_id and content are required")
	}
	return s.endpointRequest(ctx, "pgcReviewMod", requestOptions{
		account: accountRef(p),
		method:  http.MethodPost,
		form: url.Values{
			"media_id":  {stringify(mediaID)},
			"score":     {stringify(p.IntDefault("score", 10))},
			"content":   {content},
			"review_id": {reviewID},
		},
	})
}

func (s *Service) pgcReviewDelete(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	mediaID, ok := p.ID("media_id")
	reviewID := p.String("review_id")
	if !ok || mediaID <= 0 || reviewID == "" {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "media_id and review_id are required")
	}
	return s.endpointRequest(ctx, "pgcReviewDel", requestOptions{
		account: accountRef(p),
		method:  http.MethodPost,
		form: url.Values{
			"media_id":  {stringify(mediaID)},
			"review_id": {reviewID},
		},
	})
}

func (s *Service) pgcFollow(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	seasonID, ok := p.ID("season_id")
	if !ok || seasonID <= 0 {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "season_id is required")
	}
	endpoint := "pgcAdd"
	if !p.Bool("follow", true) {
		endpoint = "pgcDel"
	}
	return s.endpointRequest(ctx, endpoint, requestOptions{
		account: accountRef(p),
		method:  http.MethodPost,
		form:    url.Values{"season_id": {stringify(seasonID)}},
	})
}
