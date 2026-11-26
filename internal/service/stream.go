package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/cloudflypeng/bbcore/internal/jsonrpc"
)

func (s *Service) registerStream() {
	s.add("stream.start", "Prepare an AVPlayer-compatible HLS stream (params: bvid, cid, page, qn, codec)", s.streamStart)
	s.add("stream.stop", "Stop an HLS stream session (params: session_id)", s.streamStop)
}

type playURLPayload struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    struct {
		Timelength int64 `json:"timelength"`
		Dash       *struct {
			Video []dashStream `json:"video"`
			Audio []dashStream `json:"audio"`
		} `json:"dash"`
	} `json:"data"`
}

type dashStream struct {
	ID               int         `json:"id"`
	Codecid          int         `json:"codecid"`
	Codecs           string      `json:"codecs"`
	Width            int         `json:"width"`
	Height           int         `json:"height"`
	FrameRate        string      `json:"frameRate"`
	Bandwidth        int64       `json:"bandwidth"`
	MimeType         string      `json:"mimeType"`
	BaseURL          string      `json:"baseUrl"`
	BaseURLOld       string      `json:"base_url"`
	BackupURL        []string    `json:"backupUrl"`
	BackupURLOld     []string    `json:"backup_url"`
	SegmentBase      segmentBase `json:"segment_base"`
	SegmentBaseCamel segmentBase `json:"SegmentBase"`
}

type segmentBase struct {
	Initialization  string `json:"initialization"`
	IndexRange      string `json:"index_range"`
	IndexRangeCamel string `json:"indexRange"`
}

type streamSource struct {
	id         int
	codecID    int
	codecs     string
	width      int
	height     int
	bandwidth  int64
	frameRate  float64
	mimeType   string
	urls       []string
	initStart  int64
	initEnd    int64
	indexStart int64
	indexEnd   int64
}

func (s *Service) streamStart(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	return s.streamStartOn(ctx, raw, nil)
}

// streamStartOn prepares a DASH stream on the given gateway. A nil gateway
// means the loopback gateway hosts playback on this device; casting passes its
// LAN-bound gateway instead.
func (s *Service) streamStartOn(ctx context.Context, raw json.RawMessage, gateway *streamGateway) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}

	bvid := strings.TrimSpace(p.String("bvid"))
	if bvid == "" {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "bvid is required")
	}
	cid, ok := p.ID("cid")
	if !ok || cid <= 0 {
		cid, rpcErr = s.resolveStreamCID(ctx, bvid, p.IntDefault("page", 1))
		if rpcErr != nil {
			return nil, rpcErr
		}
	}

	playParams := map[string]any{
		"bvid":  bvid,
		"cid":   cid,
		"qn":    p.IntDefault("qn", 80),
		"fnval": p.IntDefault("fnval", 4048),
	}
	if account, ok := p.Raw("account"); ok {
		playParams["account"] = account
	}
	encoded, err := json.Marshal(playParams)
	if err != nil {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInternalError, "encode playurl params: %v", err)
	}
	result, rpcErr := s.videoPlayURL(ctx, encoded)
	if rpcErr != nil {
		return nil, rpcErr
	}
	payloadBytes, err := json.Marshal(result)
	if err != nil {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInternalError, "encode playurl response: %v", err)
	}
	var payload playURLPayload
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInternalError, "decode playurl response: %v", err)
	}
	if payload.Code != 0 {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInternalError, "playurl: %s (code %d)", payload.Message, payload.Code)
	}
	if payload.Data.Dash == nil {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInternalError, "playurl returned no DASH streams")
	}

	video, err := chooseVideoStream(payload.Data.Dash.Video, p.IntDefault("qn", 80), p.StringDefault("codec", "hevc"))
	if err != nil {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInternalError, "%v", err)
	}
	audio, err := chooseAudioStream(payload.Data.Dash.Audio, p.IntDefault("audio_id", 0))
	if err != nil {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInternalError, "%v", err)
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
		return nil, jsonrpc.Errorf(jsonrpc.CodeInternalError, "prepare HLS stream: %v", err)
	}

	out := map[string]any{
		"session_id":  session.id,
		"master_url":  gateway.baseURL + "/s/" + session.id + "/master.m3u8",
		"expires_at":  session.expiresAt.Unix(),
		"duration_ms": payload.Data.Timelength,
		"video": map[string]any{
			"id":        video.id,
			"codecid":   video.codecID,
			"codecs":    video.codecs,
			"width":     video.width,
			"height":    video.height,
			"bandwidth": video.bandwidth,
			"segments":  len(session.videoSegments),
		},
	}
	if audio != nil {
		out["audio"] = map[string]any{
			"id":        audio.id,
			"codecs":    audio.codecs,
			"bandwidth": audio.bandwidth,
			"segments":  len(session.audioSegments),
		}
	}
	return out, nil
}

func (s *Service) streamStop(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	sessionID := strings.TrimSpace(p.String("session_id"))
	if sessionID == "" {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "session_id is required")
	}
	s.streamMu.Lock()
	gateway := s.streams
	s.streamMu.Unlock()
	stopped := false
	if gateway != nil {
		stopped = gateway.stop(sessionID)
	}
	return map[string]any{"stopped": stopped}, nil
}

func (s *Service) resolveStreamCID(ctx context.Context, bvid string, page int64) (int64, *jsonrpc.Error) {
	result, rpcErr := s.endpointRequest(ctx, "videoIntro", requestOptions{
		query: url.Values{"bvid": {bvid}},
	})
	if rpcErr != nil {
		return 0, rpcErr
	}
	m, _ := result.(map[string]any)
	if code := numberField(m, "code"); code != 0 {
		return 0, jsonrpc.Errorf(jsonrpc.CodeInternalError, "resolve bvid %s: %s (code %d)", bvid, stringField(m, "message"), code)
	}
	data, _ := m["data"].(map[string]any)
	if page < 1 {
		page = 1
	}
	if pages, ok := data["pages"].([]any); ok && int(page) <= len(pages) && page > 0 {
		if pageMap, ok := pages[int(page)-1].(map[string]any); ok {
			if cid := numberField(pageMap, "cid"); cid > 0 {
				return cid, nil
			}
		}
	}
	if cid := numberField(data, "cid"); cid > 0 {
		return cid, nil
	}
	return 0, jsonrpc.Errorf(jsonrpc.CodeInternalError, "resolve bvid %s: no cid in response", bvid)
}

func chooseVideoStream(streams []dashStream, qn int64, codec string) (*streamSource, error) {
	codec = strings.ToLower(strings.TrimSpace(codec))
	if codec == "" {
		codec = "hevc"
	}
	if codec != "hevc" && codec != "avc" {
		return nil, fmt.Errorf("unsupported codec %q: use hevc or avc", codec)
	}

	avail := make([]dashStream, 0, len(streams))
	for _, stream := range streams {
		if stream.ID <= 0 || len(streamURLs(stream)) == 0 {
			continue
		}
		if _, _, _, _, err := streamRanges(stream); err != nil {
			continue
		}
		avail = append(avail, stream)
	}
	if len(avail) == 0 {
		return nil, fmt.Errorf("no byte-range video streams available")
	}

	target := int64(0)
	for _, stream := range avail {
		id := int64(stream.ID)
		if id <= qn && id > target {
			target = id
		}
	}
	if target == 0 {
		for _, stream := range avail {
			id := int64(stream.ID)
			if id > target {
				target = id
			}
		}
	}

	preferredCID := 12
	if codec == "avc" {
		preferredCID = 7
	}
	var chosen *dashStream
	for i := range avail {
		stream := &avail[i]
		if int64(stream.ID) != target {
			continue
		}
		if stream.Codecid == preferredCID {
			chosen = stream
			break
		}
		if chosen == nil && stream.Codecid != 13 {
			chosen = stream
		}
	}
	if chosen == nil {
		return nil, fmt.Errorf("no %s stream at quality %d (AV1 is not enabled for AVPlayer)", codec, target)
	}
	return toStreamSource(*chosen)
}

func chooseAudioStream(streams []dashStream, audioID int64) (*streamSource, error) {
	var chosen *dashStream
	for i := range streams {
		stream := &streams[i]
		if len(streamURLs(*stream)) == 0 {
			continue
		}
		if _, _, _, _, err := streamRanges(*stream); err != nil {
			continue
		}
		if audioID > 0 {
			if int64(stream.ID) == audioID {
				chosen = stream
				break
			}
			continue
		}
		if chosen == nil {
			chosen = stream
			continue
		}
		chosenAAC := isAACStream(*chosen)
		streamAAC := isAACStream(*stream)
		if streamAAC != chosenAAC {
			if streamAAC {
				chosen = stream
			}
			continue
		}
		if stream.Bandwidth > chosen.Bandwidth {
			chosen = stream
		}
	}
	if chosen == nil {
		return nil, fmt.Errorf("no byte-range audio stream available")
	}
	return toStreamSource(*chosen)
}

func isAACStream(stream dashStream) bool {
	codecs := strings.ToLower(strings.TrimSpace(stream.Codecs))
	if strings.HasPrefix(codecs, "mp4a.") {
		return true
	}
	switch stream.ID {
	case 30216, 30232, 30280:
		return true
	default:
		return false
	}
}

func toStreamSource(stream dashStream) (*streamSource, error) {
	urls := streamURLs(stream)
	initStart, initEnd, indexStart, indexEnd, err := streamRanges(stream)
	if err != nil {
		return nil, err
	}
	frameRate := 0.0
	if stream.FrameRate != "" {
		fmt.Sscanf(stream.FrameRate, "%f", &frameRate)
	}
	mimeType := stream.MimeType
	if mimeType == "" {
		if stream.Width > 0 {
			mimeType = "video/mp4"
		} else {
			mimeType = "audio/mp4"
		}
	}
	return &streamSource{
		id:         stream.ID,
		codecID:    stream.Codecid,
		codecs:     stream.Codecs,
		width:      stream.Width,
		height:     stream.Height,
		bandwidth:  stream.Bandwidth,
		frameRate:  frameRate,
		mimeType:   mimeType,
		urls:       urls,
		initStart:  initStart,
		initEnd:    initEnd,
		indexStart: indexStart,
		indexEnd:   indexEnd,
	}, nil
}

func streamURLs(stream dashStream) []string {
	primary := strings.TrimSpace(stream.BaseURL)
	if primary == "" {
		primary = strings.TrimSpace(stream.BaseURLOld)
	}
	backups := stream.BackupURL
	if len(backups) == 0 {
		backups = stream.BackupURLOld
	}
	urls := make([]string, 0, 1+len(backups))
	if primary != "" {
		urls = append(urls, primary)
	}
	for _, backup := range backups {
		backup = strings.TrimSpace(backup)
		if backup != "" && !containsString(urls, backup) {
			urls = append(urls, backup)
		}
	}
	return urls
}

func streamRanges(stream dashStream) (int64, int64, int64, int64, error) {
	base := stream.SegmentBase
	if base.Initialization == "" && base.IndexRange == "" {
		base = stream.SegmentBaseCamel
	}
	initStart, initEnd, err := parseByteRange(base.Initialization)
	if err != nil {
		return 0, 0, 0, 0, fmt.Errorf("invalid initialization range: %w", err)
	}
	indexRange := base.IndexRange
	if indexRange == "" {
		indexRange = base.IndexRangeCamel
	}
	indexStart, indexEnd, err := parseByteRange(indexRange)
	if err != nil {
		return 0, 0, 0, 0, fmt.Errorf("invalid index range: %w", err)
	}
	return initStart, initEnd, indexStart, indexEnd, nil
}

func parseByteRange(raw string) (int64, int64, error) {
	raw = strings.TrimSpace(raw)
	parts := strings.Split(raw, "-")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("expected start-end")
	}
	var start, end int64
	if _, err := fmt.Sscanf(parts[0], "%d", &start); err != nil {
		return 0, 0, err
	}
	if _, err := fmt.Sscanf(parts[1], "%d", &end); err != nil {
		return 0, 0, err
	}
	if start < 0 || end < start {
		return 0, 0, fmt.Errorf("invalid range %s", raw)
	}
	return start, end, nil
}

func containsString(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}
