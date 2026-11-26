package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/cloudflypeng/bbcore/internal/jsonrpc"
)

func (s *Service) registerLivePlay() {
	s.add("live.play", "Prepare a live room playback session (params: room_id, qn, format, account)", s.livePlay)
}

type liveH5Payload struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    struct {
		RoomInfo   liveH5RoomInfo   `json:"room_info"`
		AnchorInfo liveH5AnchorInfo `json:"anchor_info"`
		Watched    struct {
			Num int64 `json:"num"`
		} `json:"watched_show"`
	} `json:"data"`
}

type liveH5RoomInfo struct {
	RoomID         int64  `json:"room_id"`
	ShortID        int64  `json:"short_id"`
	UID            int64  `json:"uid"`
	Title          string `json:"title"`
	Cover          string `json:"cover"`
	Keyframe       string `json:"keyframe"`
	Description    string `json:"description"`
	AreaID         int64  `json:"area_id"`
	AreaName       string `json:"area_name"`
	ParentAreaID   int64  `json:"parent_area_id"`
	ParentAreaName string `json:"parent_area_name"`
	Online         int64  `json:"online"`
	LiveStatus     int    `json:"live_status"`
	LiveStartTime  int64  `json:"live_start_time"`
}

type liveH5AnchorInfo struct {
	BaseInfo struct {
		UName string `json:"uname"`
		Face  string `json:"face"`
	} `json:"base_info"`
	RelationInfo struct {
		Attention int64 `json:"attention"`
	} `json:"relation_info"`
}

type liveQNDesc struct {
	QN   int    `json:"qn"`
	Desc string `json:"desc"`
}

type livePlayURLPayload struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    struct {
		RoomID     int64 `json:"room_id"`
		UID        int64 `json:"uid"`
		LiveStatus int   `json:"live_status"`
		LiveTime   int64 `json:"live_time"`
		PlayURL    *struct {
			PlayURL struct {
				GQNDesc []liveQNDesc     `json:"g_qn_desc"`
				Stream  []livePlayStream `json:"stream"`
			} `json:"playurl"`
		} `json:"playurl_info"`
	} `json:"data"`
}

type livePlayStream struct {
	ProtocolName string           `json:"protocol_name"`
	Format       []livePlayFormat `json:"format"`
}

type livePlayFormat struct {
	FormatName string          `json:"format_name"`
	Codec      []livePlayCodec `json:"codec"`
}

type livePlayCodec struct {
	CodecName string            `json:"codec_name"`
	CurrentQN int               `json:"current_qn"`
	AcceptQN  []int             `json:"accept_qn"`
	BaseURL   string            `json:"base_url"`
	URLInfo   []livePlayURLInfo `json:"url_info"`
}

type livePlayURLInfo struct {
	Host      string `json:"host"`
	Extra     string `json:"extra"`
	StreamTTL int    `json:"stream_ttl"`
}

func (s *Service) livePlay(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	return s.livePlayOn(ctx, raw, nil)
}

func (s *Service) livePlayOn(ctx context.Context, raw json.RawMessage, gateway *streamGateway) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	roomID, ok := p.ID("room_id")
	if !ok || roomID <= 0 {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "room_id is required")
	}
	qn := p.IntDefault("qn", 0)
	format := strings.ToLower(strings.TrimSpace(p.StringDefault("format", "")))
	if format != "" && format != "fmp4" && format != "ts" {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "format must be fmp4 or ts")
	}
	codec := strings.ToLower(strings.TrimSpace(p.StringDefault("codec", "")))
	switch codec {
	case "", "avc", "hevc", "av1":
	default:
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "codec must be avc, hevc or av1")
	}

	room := s.livePlayRoom(ctx, roomID, accountRef(p))
	roomInfo, rpcErr := s.livePlayInfo(ctx, roomID, qn, accountRef(p))
	if rpcErr != nil && room == nil {
		return nil, rpcErr
	}

	if roomInfo == nil {
		return map[string]any{
			"playable": false,
			"reason":   "upstream_error",
			"message":  "room play info is unavailable",
			"room":     room,
		}, nil
	}
	if roomInfo.Data.LiveStatus != 1 {
		return map[string]any{
			"playable": false,
			"reason":   "not_live",
			"room":     room,
		}, nil
	}
	if roomInfo.Code != 0 {
		reason := "upstream_error"
		if roomInfo.Code == -352 {
			reason = "risk_control"
		}
		return map[string]any{
			"playable": false,
			"reason":   reason,
			"message":  fmt.Sprintf("%s (code %d)", roomInfo.Message, roomInfo.Code),
			"room":     room,
		}, nil
	}
	if roomInfo.Data.PlayURL == nil {
		return map[string]any{
			"playable": false,
			"reason":   "hls_unavailable",
			"message":  "room returned no play URLs",
			"room":     room,
		}, nil
	}

	playURL := roomInfo.Data.PlayURL.PlayURL
	qualities := liveQualities(playURL.GQNDesc, chosenLiveQN(playURL.Stream))
	candidates := liveHLSCandidates(playURL.Stream, format, codec)
	if len(candidates) == 0 {
		return map[string]any{
			"playable":  false,
			"reason":    "hls_unavailable",
			"message":   "no HTTP HLS stream is available for this room",
			"room":      room,
			"qualities": qualities,
		}, nil
	}

	if gateway == nil {
		var gatewayErr *jsonrpc.Error
		gateway, gatewayErr = s.streamGateway()
		if gatewayErr != nil {
			return nil, gatewayErr
		}
	}
	var session *liveSession
	var chosen liveHLSCandidate
	var failures []string
	for _, candidate := range candidates {
		started, err := gateway.startLive(ctx, candidate.url)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s/%s: %v", candidate.format, candidate.codec, err))
			continue
		}
		session = started
		chosen = candidate
		break
	}
	if session == nil {
		message := "no playable HLS stream"
		if len(failures) > 0 {
			message = failures[0]
		}
		return map[string]any{
			"playable":  false,
			"reason":    "hls_unavailable",
			"message":   message,
			"room":      room,
			"qualities": qualities,
		}, nil
	}

	currentQN := chosen.qn
	if currentQN <= 0 {
		currentQN = chosenLiveQN(playURL.Stream)
	}
	qualities = liveQualities(playURL.GQNDesc, currentQN)
	quality := map[string]any{"qn": currentQN, "desc": liveQualityDesc(playURL.GQNDesc, currentQN)}
	return map[string]any{
		"playable":     true,
		"session_id":   session.id,
		"playback_url": gateway.baseURL + "/l/" + session.id + "/playlist.m3u8",
		"expires_at":   session.expiresAt.Unix(),
		"format":       chosen.format,
		"codec":        chosen.codec,
		"quality":      quality,
		"qualities":    qualities,
		"room":         room,
	}, nil
}

func (s *Service) livePlayRoom(ctx context.Context, roomID int64, account string) map[string]any {
	result, rpcErr := s.endpointRequest(ctx, "liveRoomInfoH5", requestOptions{
		account: account,
		query:   url.Values{"room_id": {stringify(roomID)}},
		headers: map[string]string{"referer": fmt.Sprintf("https://live.bilibili.com/%d", roomID)},
	})
	if rpcErr != nil {
		return nil
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return nil
	}
	var payload liveH5Payload
	if err := json.Unmarshal(encoded, &payload); err != nil {
		return nil
	}
	if payload.Code != 0 {
		return nil
	}
	info := payload.Data.RoomInfo
	if info.RoomID <= 0 {
		return nil
	}
	return map[string]any{
		"room_id":          info.RoomID,
		"short_id":         info.ShortID,
		"uid":              info.UID,
		"title":            info.Title,
		"cover":            info.Cover,
		"keyframe":         info.Keyframe,
		"description":      info.Description,
		"area_id":          info.AreaID,
		"area_name":        info.AreaName,
		"parent_area_id":   info.ParentAreaID,
		"parent_area_name": info.ParentAreaName,
		"online":           info.Online,
		"watched":          payload.Data.Watched.Num,
		"live_status":      info.LiveStatus,
		"live_start_time":  info.LiveStartTime,
		"anchor_name":      payload.Data.AnchorInfo.BaseInfo.UName,
		"anchor_face":      payload.Data.AnchorInfo.BaseInfo.Face,
		"attention":        payload.Data.AnchorInfo.RelationInfo.Attention,
	}
}

func (s *Service) livePlayInfo(ctx context.Context, roomID, qn int64, account string) (*livePlayURLPayload, *jsonrpc.Error) {
	if qn <= 0 {
		qn = 10000
	}
	result, rpcErr := s.endpointRequest(ctx, "liveRoomInfo", requestOptions{
		account: account,
		query: values(map[string]any{
			"room_id": roomID, "protocol": "0,1", "format": "0,1,2", "codec": "0,1,2",
			"qn": qn, "platform": "web", "ptype": 8,
			"dolby": 5, "panorama": 1, "web_location": 444.8,
		}),
		headers: map[string]string{"referer": fmt.Sprintf("https://live.bilibili.com/%d", roomID)},
	})
	if rpcErr != nil {
		return nil, rpcErr
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInternalError, "encode room info: %v", err)
	}
	var payload livePlayURLPayload
	if err := json.Unmarshal(encoded, &payload); err != nil {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInternalError, "decode room info: %v", err)
	}
	return &payload, nil
}

type liveHLSCandidate struct {
	format string
	codec  string
	qn     int
	url    string
	rank   int
}

func liveHLSCandidates(streams []livePlayStream, preferredFormat, preferredCodec string) []liveHLSCandidate {
	formatRank := map[string]int{"fmp4": 0, "ts": 1}
	if preferredFormat != "" {
		formatRank = map[string]int{preferredFormat: 0}
	}
	// AVPlayer needs AVC or HEVC; AV1 support is spotty and Bilibili can report
	// an AV1 fallback with a higher qn than the requested quality, so the codec
	// family decides before quality does.
	codecRank := map[string]int{"avc": 0, "hevc": 1, "av1": 2}
	var candidates []liveHLSCandidate
	for _, stream := range streams {
		if stream.ProtocolName != "http_hls" {
			continue
		}
		for _, item := range stream.Format {
			rank, ok := formatRank[strings.ToLower(item.FormatName)]
			if !ok {
				continue
			}
			for _, codec := range item.Codec {
				if strings.TrimSpace(codec.BaseURL) == "" {
					continue
				}
				codecName := strings.ToLower(codec.CodecName)
				if preferredCodec != "" && codecName != preferredCodec {
					continue
				}
				for _, source := range codec.URLInfo {
					host := strings.TrimSpace(source.Host)
					if host == "" {
						continue
					}
					candidates = append(candidates, liveHLSCandidate{
						format: strings.ToLower(item.FormatName),
						codec:  codecName,
						qn:     codec.CurrentQN,
						url:    host + codec.BaseURL + source.Extra,
						rank:   rank,
					})
				}
			}
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		ri, iok := codecRank[candidates[i].codec]
		if !iok {
			ri = len(codecRank)
		}
		rj, jok := codecRank[candidates[j].codec]
		if !jok {
			rj = len(codecRank)
		}
		if ri != rj {
			return ri < rj
		}
		if candidates[i].qn != candidates[j].qn {
			return candidates[i].qn > candidates[j].qn
		}
		if candidates[i].rank != candidates[j].rank {
			return candidates[i].rank < candidates[j].rank
		}
		return candidates[i].codec < candidates[j].codec
	})
	return candidates
}

func chosenLiveQN(streams []livePlayStream) int {
	best := 0
	for _, stream := range streams {
		if stream.ProtocolName != "http_hls" {
			continue
		}
		for _, format := range stream.Format {
			for _, codec := range format.Codec {
				if codec.CurrentQN > best {
					best = codec.CurrentQN
				}
			}
		}
	}
	return best
}

func liveQualities(desc []liveQNDesc, current int) []map[string]any {
	var out []map[string]any
	seen := map[int]bool{}
	for _, item := range desc {
		if item.QN <= 0 || seen[item.QN] {
			continue
		}
		seen[item.QN] = true
		out = append(out, map[string]any{
			"qn":      item.QN,
			"desc":    item.Desc,
			"current": item.QN == current,
		})
	}
	if len(out) == 0 && current > 0 {
		out = append(out, map[string]any{"qn": current, "desc": liveQualityDesc(desc, current), "current": true})
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i]["qn"].(int) > out[j]["qn"].(int)
	})
	return out
}

func liveQualityDesc(desc []liveQNDesc, qn int) string {
	for _, item := range desc {
		if item.QN == qn && item.Desc != "" {
			return item.Desc
		}
	}
	switch qn {
	case 30000:
		return "杜比"
	case 20000:
		return "4K"
	case 15000:
		return "2K"
	case 10000:
		return "原画"
	case 400:
		return "蓝光"
	case 250:
		return "超清"
	case 150:
		return "高清"
	case 80:
		return "流畅"
	default:
		return fmt.Sprintf("%dP", qn)
	}
}
