package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/cloudflypeng/bbcore/internal/bili"
	"github.com/cloudflypeng/bbcore/internal/jsonrpc"
)

func (s *Service) registerPlayback() {
	s.add("player.resolve", "Resolve video metadata, qualities, audio, subtitles and resume point (params: bvid/aid, cid, page, qn, codec)", s.playerResolve)
	s.add("player.progress", "Report playback progress (params: bvid/aid, cid, progress_ms, duration_ms, event)", s.playerProgress)
	s.add("video.subtitle_vtt", "Fetch a Bilibili subtitle JSON URL and return WebVTT (params: url)", s.videoSubtitleVTT)
	s.add("history.delete", "Delete watch history entries by kid (params: kid or kids)", s.historyDelete)
	s.add("history.clear", "Clear watch history", s.historyClear)
	s.add("history.pause", "Pause/resume watch history (params: pause)", s.historyPause)
	s.add("history.status", "Watch history pause status", s.historyStatus)
}

var qualityLabels = map[int64]string{
	127: "8K",
	126: "杜比视界",
	125: "HDR",
	120: "4K",
	116: "1080P60",
	112: "1080P+",
	80:  "1080P",
	74:  "720P60",
	64:  "720P",
	32:  "480P",
	16:  "360P",
	6:   "240P",
}

func (s *Service) playerResolve(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	bvid := p.String("bvid")
	aid, _ := p.ID("aid")
	if bvid == "" && aid <= 0 {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "bvid or aid is required")
	}

	infoParams, _ := json.Marshal(map[string]any{
		"bvid": bvid,
		"aid":  aid,
	})
	infoResult, rpcErr := s.videoInfo(ctx, infoParams)
	if rpcErr != nil {
		return nil, rpcErr
	}
	root, _ := infoResult.(map[string]any)
	if code := upstreamCode(root); code != 0 {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInternalError, "video.info: %s (code %d)", stringField(root, "message"), code)
	}
	data, _ := root["data"].(map[string]any)
	if data == nil {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInternalError, "video.info returned no data")
	}

	resolvedBVID := stringField(data, "bvid")
	if resolvedBVID == "" {
		resolvedBVID = bvid
	}
	resolvedAID := numberField(data, "aid")
	if resolvedAID <= 0 {
		resolvedAID = aid
	}
	title := stringField(data, "title")
	durationMS := numberField(data, "duration") * 1000
	pages := parsePages(data["pages"])

	pageNo := int(p.IntDefault("page", 1))
	if pageNo < 1 {
		pageNo = 1
	}
	var page map[string]any
	if pageNo <= len(pages) {
		page = pages[pageNo-1]
	}
	cid, _ := p.ID("cid")
	if cid <= 0 && page != nil {
		cid = numberField(page, "cid")
	}
	if cid <= 0 && resolvedBVID != "" {
		resolved, cidErr := s.resolveStreamCID(ctx, resolvedBVID, int64(pageNo))
		if cidErr != nil {
			return nil, cidErr
		}
		cid = resolved
	}
	if cid <= 0 {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "cid is required")
	}
	if page != nil && numberField(page, "duration") > 0 {
		durationMS = numberField(page, "duration") * 1000
	}

	account := accountRef(p)
	playParams, _ := json.Marshal(map[string]any{
		"bvid":    resolvedBVID,
		"aid":     resolvedAID,
		"cid":     cid,
		"qn":      p.IntDefault("qn", 127),
		"fnval":   4048,
		"account": account,
	})
	playResult, rpcErr := s.videoPlayURL(ctx, playParams)
	if rpcErr != nil {
		return nil, rpcErr
	}
	playBytes, err := json.Marshal(playResult)
	if err != nil {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInternalError, "encode playurl response: %v", err)
	}
	var play playURLPayload
	if err := json.Unmarshal(playBytes, &play); err != nil {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInternalError, "decode playurl response: %v", err)
	}
	if play.Code != 0 {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInternalError, "playurl: %s (code %d)", play.Message, play.Code)
	}

	qualities := make([]map[string]any, 0)
	audios := make([]map[string]any, 0)
	if play.Data.Dash != nil {
		for _, stream := range play.Data.Dash.Video {
			qualities = append(qualities, map[string]any{
				"qn":         stream.ID,
				"label":      qualityLabel(int64(stream.ID)),
				"codecid":    stream.Codecid,
				"codec":      codecLabel(stream.Codecid),
				"codecs":     stream.Codecs,
				"width":      stream.Width,
				"height":     stream.Height,
				"bandwidth":  stream.Bandwidth,
				"frame_rate": stream.FrameRate,
				"mime_type":  stream.MimeType,
			})
		}
		sort.SliceStable(qualities, func(i, j int) bool {
			left, right := toInt64(qualities[i]["qn"]), toInt64(qualities[j]["qn"])
			if left == right {
				return codecRank(qualities[i]["codec"].(string)) < codecRank(qualities[j]["codec"].(string))
			}
			return left > right
		})
		for _, stream := range play.Data.Dash.Audio {
			audios = append(audios, map[string]any{
				"id":        stream.ID,
				"codecs":    stream.Codecs,
				"bandwidth": stream.Bandwidth,
			})
		}
		sort.SliceStable(audios, func(i, j int) bool {
			return toInt64(audios[i]["bandwidth"]) > toInt64(audios[j]["bandwidth"])
		})
	}

	playInfoParams, _ := json.Marshal(map[string]any{
		"bvid":    resolvedBVID,
		"aid":     resolvedAID,
		"cid":     cid,
		"account": account,
	})
	playInfo := map[string]any{}
	if result, infoErr := s.videoPlayInfo(ctx, playInfoParams); infoErr == nil {
		if infoRoot, ok := result.(map[string]any); ok && upstreamCode(infoRoot) == 0 {
			playInfo, _ = infoRoot["data"].(map[string]any)
		}
	}

	subtitles := parseSubtitles(playInfo)
	lastPlayCID := numberField(playInfo, "last_play_cid")
	lastPlayMS := numberField(playInfo, "last_play_time")
	resumeMS := int64(0)
	if lastPlayCID == cid {
		resumeMS = lastPlayMS
	}

	requestedQN := p.IntDefault("qn", 80)
	defaultQN := int64(0)
	for _, quality := range qualities {
		qn := toInt64(quality["qn"])
		if qn <= requestedQN && qn > defaultQN {
			defaultQN = qn
		}
	}
	if defaultQN == 0 && len(qualities) > 0 {
		defaultQN = toInt64(qualities[0]["qn"])
	}
	defaultCodec := p.StringDefault("codec", "hevc")
	if defaultCodec == "" {
		defaultCodec = "hevc"
	}

	out := map[string]any{
		"bvid":          resolvedBVID,
		"aid":           resolvedAID,
		"cid":           cid,
		"page":          pageNo,
		"title":         title,
		"duration_ms":   durationMS,
		"pages":         pages,
		"qualities":     qualities,
		"audio":         audios,
		"subtitles":     subtitles,
		"resume_ms":     resumeMS,
		"last_play_cid": lastPlayCID,
		"default_qn":    defaultQN,
		"default_codec": defaultCodec,
		"playable":      len(qualities) > 0,
		"is_preview":    boolField(data, "is_ugc_pay_preview"),
	}
	if page != nil {
		out["page_title"] = stringField(page, "part")
	}
	if permission := playInfo["permission"]; permission != nil {
		out["permission"] = permission
	}
	return out, nil
}

func (s *Service) playerProgress(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	bvid := p.String("bvid")
	aid, _ := p.ID("aid")
	cid, ok := p.ID("cid")
	if cid <= 0 || (bvid == "" && aid <= 0) {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "cid and bvid/aid are required")
	}
	progressMS, ok := p.ID("progress_ms")
	if !ok {
		seconds := p.IntDefault("progress", 0)
		progressMS = seconds * 1000
	}
	if progressMS < 0 {
		progressMS = -1
	}

	cred, credErr := s.credential(accountRef(p))
	if credErr != nil {
		return nil, credErr
	}
	if cred == nil || !cred.LoggedIn {
		return map[string]any{"reported": false, "reason": "auth_required"}, nil
	}

	form := url.Values{}
	if bvid != "" {
		form.Set("bvid", bvid)
	} else {
		form.Set("aid", strconv.FormatInt(aid, 10))
	}
	form.Set("cid", strconv.FormatInt(cid, 10))
	playedTime := progressMS / 1000
	if progressMS < 0 || p.String("event") == "end" {
		playedTime = -1
	}
	form.Set("played_time", strconv.FormatInt(playedTime, 10))
	form.Set("type", "3")
	form.Set("sub_type", "0")

	result, rpcErr := s.endpointRequest(ctx, "heartBeat", requestOptions{
		account: accountRef(p),
		method:  http.MethodPost,
		form:    form,
	})
	if rpcErr != nil {
		return nil, rpcErr
	}
	root, _ := result.(map[string]any)
	return map[string]any{
		"reported": upstreamCode(result) == 0,
		"code":     upstreamCode(result),
		"message":  stringField(root, "message"),
	}, nil
}

func (s *Service) videoSubtitleVTT(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	target, ok := normalizeSubtitleURL(p.String("url"))
	if !ok {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "url must be an https Bilibili subtitle URL")
	}
	cred, credErr := s.credential(accountRef(p))
	if credErr != nil {
		return nil, credErr
	}
	result, err := s.opt.Client.Do(ctx, bili.Spec{
		Path:       target,
		Method:     http.MethodGet,
		Sign:       bili.SignNone,
		Credential: cred,
		Headers:    map[string]string{"Referer": "https://www.bilibili.com"},
	})
	if err != nil {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInternalError, "fetch subtitle: %v", err)
	}
	if result.Status != http.StatusOK {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInternalError, "fetch subtitle: status %d", result.Status)
	}
	root, _ := result.JSON.(map[string]any)
	if root == nil {
		if err := json.Unmarshal(result.Body, &root); err != nil {
			return nil, jsonrpc.Errorf(jsonrpc.CodeInternalError, "decode subtitle: %v", err)
		}
	}
	vtt, cues := jsonToVTT(root["body"])
	return map[string]any{
		"content_type": "text/vtt",
		"cue_count":    cues,
		"vtt":          vtt,
	}, nil
}

func (s *Service) historyDelete(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	kid := strings.TrimSpace(p.String("kid"))
	if kid == "" {
		kid = joinList(p.Value("kids"))
	}
	if kid == "" {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "kid is required")
	}
	return s.endpointRequest(ctx, "delHistory", requestOptions{
		account: accountRef(p),
		method:  http.MethodPost,
		form:    url.Values{"kid": {kid}, "jsonp": {"jsonp"}},
	})
}

func (s *Service) historyClear(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	return s.endpointRequest(ctx, "clearHistory", requestOptions{
		account: accountRef(p),
		method:  http.MethodPost,
		form:    url.Values{"jsonp": {"jsonp"}},
	})
}

func (s *Service) historyPause(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	return s.endpointRequest(ctx, "pauseHistory", requestOptions{
		account: accountRef(p),
		method:  http.MethodPost,
		form: url.Values{
			"switch": {strconv.FormatBool(p.Bool("pause", true))},
			"jsonp":  {"jsonp"},
		},
	})
}

func (s *Service) historyStatus(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	return s.endpointRequest(ctx, "historyStatus", requestOptions{
		account: accountRef(p),
	})
}

func parsePages(raw any) []map[string]any {
	items, _ := raw.([]any)
	pages := make([]map[string]any, 0, len(items))
	for _, item := range items {
		page, ok := item.(map[string]any)
		if !ok {
			continue
		}
		pages = append(pages, map[string]any{
			"page":        numberField(page, "page"),
			"cid":         numberField(page, "cid"),
			"part":        stringField(page, "part"),
			"duration_ms": numberField(page, "duration") * 1000,
		})
	}
	return pages
}

func parseSubtitles(playInfo map[string]any) []map[string]any {
	subtitle, _ := playInfo["subtitle"].(map[string]any)
	items, _ := subtitle["subtitles"].([]any)
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		sub, ok := item.(map[string]any)
		if !ok {
			continue
		}
		rawURL := stringField(sub, "subtitle_url")
		normalized, ok := normalizeSubtitleURL(rawURL)
		if !ok {
			continue
		}
		out = append(out, map[string]any{
			"lan":       stringField(sub, "lan"),
			"label":     stringField(sub, "lan_doc"),
			"url":       normalized,
			"id":        stringField(sub, "id_str"),
			"ai_type":   numberField(sub, "ai_type"),
			"ai_status": numberField(sub, "ai_status"),
		})
	}
	return out
}

func normalizeSubtitleURL(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	if strings.HasPrefix(raw, "//") {
		raw = "https:" + raw
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return "", false
	}
	if parsed.Scheme == "" {
		parsed.Scheme = "https"
	}
	if parsed.Scheme != "https" {
		return "", false
	}
	host := strings.ToLower(parsed.Hostname())
	for _, suffix := range []string{"hdslb.com", "bilibili.com", "bilivideo.com"} {
		if host == suffix || strings.HasSuffix(host, "."+suffix) {
			return parsed.String(), true
		}
	}
	return "", false
}

func jsonToVTT(body any) (string, int) {
	items, _ := body.([]any)
	var out strings.Builder
	out.WriteString("WEBVTT\n\n")
	count := 0
	for _, item := range items {
		cue, ok := item.(map[string]any)
		if !ok {
			continue
		}
		from := floatField(cue, "from")
		to := floatField(cue, "to")
		content := strings.TrimSpace(stringField(cue, "content"))
		if content == "" || to < from {
			continue
		}
		out.WriteString(vttTime(from))
		out.WriteString(" --> ")
		out.WriteString(vttTime(to))
		out.WriteByte('\n')
		out.WriteString(content)
		out.WriteString("\n\n")
		count++
	}
	return out.String(), count
}

func vttTime(seconds float64) string {
	if seconds < 0 {
		seconds = 0
	}
	hours := int(seconds) / 3600
	minutes := (int(seconds) % 3600) / 60
	secs := seconds - float64(hours*3600+minutes*60)
	return fmt.Sprintf("%02d:%02d:%06.3f", hours, minutes, secs)
}

func floatField(m map[string]any, key string) float64 {
	switch value := m[key].(type) {
	case float64:
		return value
	case json.Number:
		number, _ := value.Float64()
		return number
	case int64:
		return float64(value)
	case string:
		number, _ := strconv.ParseFloat(value, 64)
		return number
	default:
		return 0
	}
}

func boolField(m map[string]any, key string) bool {
	value, _ := m[key].(bool)
	return value
}

func qualityLabel(qn int64) string {
	if label, ok := qualityLabels[qn]; ok {
		return label
	}
	return "QN" + strconv.FormatInt(qn, 10)
}

func codecLabel(codecID int) string {
	switch codecID {
	case 7:
		return "avc"
	case 12:
		return "hevc"
	case 13:
		return "av1"
	default:
		return "codec" + strconv.Itoa(codecID)
	}
}

func codecRank(codec string) int {
	switch codec {
	case "hevc":
		return 0
	case "avc":
		return 1
	case "av1":
		return 2
	default:
		return 3
	}
}
