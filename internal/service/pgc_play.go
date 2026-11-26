package service

import (
	"context"
	"encoding/json"
	"net/url"
	"sort"
	"strings"

	"github.com/cloudflypeng/bbcore/internal/jsonrpc"
)

func (s *Service) registerPGCPlay() {
	s.add("pgc.play", "Resolve a PGC episode to an AVPlayer HLS session (params: season_id|ep_id, cid, qn, codec, try_look)", s.pgcPlay)
}

type pgcPlayPayload struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    struct {
		UserStatus struct {
			IsLogin int `json:"isLogin"`
			VIP     int `json:"vip"`
		} `json:"user_status"`
		VideoInfo struct {
			Timelength        int64             `json:"timelength"`
			AcceptQuality     []int64           `json:"accept_quality"`
			AcceptDescription []string          `json:"accept_description"`
			VideoCodecid      int               `json:"video_codecid"`
			IsPreview         any               `json:"is_preview"`
			Dash              *pgcPlayDashBlock `json:"dash"`
		} `json:"video_info"`
	} `json:"data"`
}

type pgcPlayDashBlock struct {
	Duration float64      `json:"duration"`
	Video    []dashStream `json:"video"`
	Audio    []dashStream `json:"audio"`
}

func (s *Service) pgcPlay(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	return s.pgcPlayOn(ctx, raw, nil)
}

func (s *Service) pgcPlayOn(ctx context.Context, raw json.RawMessage, gateway *streamGateway) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	seasonID, _ := p.ID("season_id")
	epID, _ := p.ID("ep_id")
	cid, _ := p.ID("cid")
	if seasonID <= 0 && epID <= 0 && cid <= 0 {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "season_id, ep_id or cid is required")
	}

	season, ep := s.resolvePGCEpisode(ctx, p, seasonID, epID, cid)
	if ep == nil {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "no playable episode found")
	}
	if cid <= 0 {
		cid = numberField(ep, "cid")
	}
	if cid <= 0 {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInternalError, "episode has no cid")
	}

	playParams, _ := json.Marshal(map[string]any{
		"season_id": numberField(season, "season_id"),
		"ep_id":     numberField(ep, "ep_id"),
		"cid":       cid,
		"qn":        p.IntDefault("qn", 80),
		"try_look":  p.Bool("try_look", false),
		"account":   accountRef(p),
	})
	playResult, rpcErr := s.pgcPlayurl(ctx, playParams)
	if rpcErr != nil {
		return nil, rpcErr
	}
	payloadBytes, err := json.Marshal(playResult)
	if err != nil {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInternalError, "encode PGC playurl: %v", err)
	}
	var payload pgcPlayPayload
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInternalError, "decode PGC playurl: %v", err)
	}
	if payload.Code != 0 {
		return pgcPlayUnavailable(payload.Code, payload.Message), nil
	}

	isPreview := pgcFlag(payload.Data.VideoInfo.IsPreview)
	out := map[string]any{
		"playable":   false,
		"season_id":  numberField(season, "season_id"),
		"media_id":   numberField(season, "media_id"),
		"season":     pgcSeasonBrief(season),
		"episode":    pgcEpisodeBrief(ep),
		"episodes":   pgcEpisodeList(season),
		"is_preview": isPreview,
	}

	dash := payload.Data.VideoInfo.Dash
	if dash == nil || len(dash.Video) == 0 {
		if strings.Contains(stringField(ep, "badge"), "会员") {
			out["reason"] = "entitlement_required"
			out["message"] = "该剧集需要大会员，当前账号没有播放权益"
		} else {
			out["reason"] = "hls_unavailable"
			out["message"] = "该剧集没有可用的 DASH 流"
		}
		return out, nil
	}

	video, err := chooseVideoStream(dash.Video, p.IntDefault("qn", 80), p.StringDefault("codec", "hevc"))
	if err != nil {
		out["reason"] = "hls_unavailable"
		out["message"] = err.Error()
		return out, nil
	}
	audio, err := chooseAudioStream(dash.Audio, p.IntDefault("audio_id", 0))
	if err != nil {
		out["reason"] = "hls_unavailable"
		out["message"] = err.Error()
		return out, nil
	}
	if gateway == nil {
		var gatewayErr *jsonrpc.Error
		gateway, gatewayErr = s.streamGateway()
		if gatewayErr != nil {
			return nil, gatewayErr
		}
	}
	session, err := gateway.start(ctx, video, audio)
	if err != nil {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInternalError, "prepare PGC HLS stream: %v", err)
	}

	durationMS := payload.Data.VideoInfo.Timelength
	if durationMS <= 0 && dash.Duration > 0 {
		durationMS = int64(dash.Duration * 1000)
	}
	out["playable"] = true
	out["session_id"] = session.id
	out["playback_url"] = gateway.baseURL + "/p/" + session.id + "/master.m3u8"
	out["expires_at"] = session.expiresAt.Unix()
	out["duration_ms"] = durationMS
	out["quality"] = pgcQualityLabel(payload.Data.VideoInfo.AcceptQuality, payload.Data.VideoInfo.AcceptDescription, int64(video.id))
	out["qualities"] = pgcQualities(payload.Data.VideoInfo.AcceptQuality, payload.Data.VideoInfo.AcceptDescription, dash.Video)
	out["video"] = map[string]any{
		"id":        video.id,
		"codecid":   video.codecID,
		"codecs":    video.codecs,
		"width":     video.width,
		"height":    video.height,
		"bandwidth": video.bandwidth,
	}
	out["audio"] = map[string]any{
		"id":        audio.id,
		"codecs":    audio.codecs,
		"bandwidth": audio.bandwidth,
	}
	return out, nil
}

func pgcPlayUnavailable(code int, message string) map[string]any {
	reason := "upstream_error"
	switch code {
	case -10403, -10402:
		reason = "region_limited"
	case 87007, 87008, 6002001:
		reason = "entitlement_required"
	case -101:
		reason = "auth_required"
	case -404:
		reason = "not_found"
	}
	if message == "" || message == "0" {
		message = "剧集暂不可播放"
	}
	return map[string]any{
		"playable": false,
		"reason":   reason,
		"message":  message,
		"code":     code,
	}
}

func pgcFlag(value any) bool {
	switch v := value.(type) {
	case bool:
		return v
	case float64:
		return v != 0
	case json.Number:
		number, err := v.Float64()
		return err == nil && number != 0
	case string:
		return v != "" && v != "0" && v != "false"
	default:
		return false
	}
}

func (s *Service) resolvePGCEpisode(ctx context.Context, p jsonrpc.Params, seasonID, epID, cid int64) (map[string]any, map[string]any) {
	query := url.Values{}
	if seasonID > 0 {
		query.Set("season_id", stringify(seasonID))
	}
	if epID > 0 {
		query.Set("ep_id", stringify(epID))
	}
	if len(query) == 0 {
		return map[string]any{}, map[string]any{"cid": cid, "ep_id": epID}
	}
	result, rpcErr := s.endpointQuery(ctx, "pgcInfo", p, query)
	if rpcErr != nil {
		return map[string]any{}, nil
	}
	root, _ := result.(map[string]any)
	if upstreamCode(root) != 0 {
		return map[string]any{}, nil
	}
	season, _ := root["result"].(map[string]any)
	if season == nil {
		return map[string]any{}, nil
	}
	episodes := pgcEpisodes(season)
	var chosen map[string]any
	if cid > 0 {
		chosen = pgcFindEpisode(episodes, "cid", cid)
	}
	if chosen == nil && epID > 0 {
		chosen = pgcFindEpisode(episodes, "ep_id", epID)
	}
	if chosen == nil && len(episodes) > 0 {
		chosen = episodes[0]
	}
	return season, chosen
}

func pgcEpisodes(season map[string]any) []map[string]any {
	raw, _ := season["episodes"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		if ep, ok := item.(map[string]any); ok {
			out = append(out, ep)
		}
	}
	main, _ := season["main_section"].(map[string]any)
	if main != nil {
		items, _ := main["episodes"].([]any)
		for _, item := range items {
			if ep, ok := item.(map[string]any); ok {
				out = append(out, ep)
			}
		}
	}
	return out
}

func pgcFindEpisode(episodes []map[string]any, key string, value int64) map[string]any {
	for _, ep := range episodes {
		if numberField(ep, key) == value {
			return ep
		}
	}
	return nil
}

func pgcEpisodeList(season map[string]any) []map[string]any {
	episodes := pgcEpisodes(season)
	out := make([]map[string]any, 0, len(episodes))
	seen := map[int64]bool{}
	for _, ep := range episodes {
		epID := numberField(ep, "ep_id")
		if epID <= 0 || seen[epID] {
			continue
		}
		seen[epID] = true
		out = append(out, pgcEpisodeBrief(ep))
	}
	return out
}

func pgcEpisodeBrief(ep map[string]any) map[string]any {
	epID := numberField(ep, "ep_id")
	if epID <= 0 {
		epID = numberField(ep, "id")
	}
	title := stringField(ep, "title")
	longTitle := stringField(ep, "long_title")
	label := strings.TrimSpace(title)
	badge := stringField(ep, "badge")
	return map[string]any{
		"ep_id":       epID,
		"cid":         numberField(ep, "cid"),
		"aid":         numberField(ep, "aid"),
		"bvid":        stringField(ep, "bvid"),
		"title":       title,
		"long_title":  longTitle,
		"label":       label,
		"cover":       stringField(ep, "cover"),
		"duration_ms": numberField(ep, "duration"),
		"badge":       badge,
		"pub_time":    numberField(ep, "pub_time"),
	}
}

func pgcSeasonBrief(season map[string]any) map[string]any {
	return map[string]any{
		"season_id": numberField(season, "season_id"),
		"media_id":  numberField(season, "media_id"),
		"title":     stringField(season, "title"),
		"cover":     stringField(season, "cover"),
		"type_name": stringField(season, "type_name"),
		"evaluate":  stringField(season, "evaluate"),
		"total":     numberField(season, "total"),
	}
}

func pgcQualityLabel(qns []int64, labels []string, qn int64) map[string]any {
	for i, value := range qns {
		if value == qn {
			label := qualityLabel(value)
			if i < len(labels) && labels[i] != "" {
				label = labels[i]
			}
			return map[string]any{"qn": value, "label": label}
		}
	}
	return map[string]any{"qn": qn, "label": qualityLabel(qn)}
}

func pgcQualities(qns []int64, labels []string, streams []dashStream) []map[string]any {
	available := map[int64][]dashStream{}
	for _, stream := range streams {
		if len(streamURLs(stream)) == 0 {
			continue
		}
		available[int64(stream.ID)] = append(available[int64(stream.ID)], stream)
	}
	out := make([]map[string]any, 0, len(available))
	for qn, group := range available {
		label := qualityLabel(qn)
		for i, value := range qns {
			if value == qn && i < len(labels) && labels[i] != "" {
				label = labels[i]
				break
			}
		}
		codecs := make([]string, 0, len(group))
		for _, stream := range group {
			codec := codecLabel(stream.Codecid)
			if !containsString(codecs, codec) {
				codecs = append(codecs, codec)
			}
		}
		out = append(out, map[string]any{
			"qn":        qn,
			"label":     label,
			"codecs":    codecs,
			"width":     group[0].Width,
			"height":    group[0].Height,
			"bandwidth": group[0].Bandwidth,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		return toInt64(out[i]["qn"]) > toInt64(out[j]["qn"])
	})
	return out
}
