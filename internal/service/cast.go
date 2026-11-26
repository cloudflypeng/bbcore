package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/cloudflypeng/bbcore/internal/cast"
	"github.com/cloudflypeng/bbcore/internal/jsonrpc"
)

func (s *Service) registerCast() {
	s.add("cast.discover", "Discover DLNA media renderers on the local network (params: timeout_ms)", s.castDiscover)
	s.add("cast.play", "Cast a video, PGC episode or live room to a DLNA renderer (params: control_url, bvid|season_id|ep_id|room_id, title, position_ms, mode=auto|direct|proxy)", s.castPlay)
	s.add("cast.stop", "Stop the active cast session", s.castStop)
	s.add("cast.status", "Describe the active cast session", s.castStatus)
}

type castSession struct {
	Device      cast.Device
	Kind        string
	Mode        string
	Title       string
	SessionID   string
	PlaybackURL string
	MIMEType    string
	Gateway     *streamGateway
	StartedAt   time.Time
}

func (s *Service) castDiscover(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	timeout := time.Duration(p.IntDefault("timeout_ms", 3000)) * time.Millisecond
	if timeout < 800*time.Millisecond {
		timeout = 800 * time.Millisecond
	}
	if timeout > 8*time.Second {
		timeout = 8 * time.Second
	}
	devices, err := cast.Discover(ctx, timeout)
	if err != nil {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInternalError, "discover cast devices: %v", err)
	}
	if devices == nil {
		devices = []cast.Device{}
	}
	return map[string]any{"devices": devices}, nil
}

func (s *Service) castPlay(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}

	controlURL := strings.TrimSpace(p.String("control_url"))
	if controlURL == "" {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "control_url is required")
	}
	parsedControl, err := lanControlURL(controlURL)
	if err != nil {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "%v", err)
	}

	bvid := strings.TrimSpace(p.String("bvid"))
	roomID, _ := p.ID("room_id")
	seasonID, _ := p.ID("season_id")
	epID, _ := p.ID("ep_id")
	kind := ""
	switch {
	case roomID > 0:
		kind = "live"
	case bvid != "":
		kind = "video"
	case seasonID > 0 || epID > 0:
		kind = "pgc"
	default:
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "bvid, season_id, ep_id or room_id is required")
	}

	host := parsedControl.Hostname()

	mode := strings.ToLower(strings.TrimSpace(p.String("mode")))
	if mode != "proxy" && mode != "direct" {
		mode = "auto"
	}
	title := strings.TrimSpace(p.String("title"))

	// Only one cast at a time: stop the TV and drop its gateway before the new
	// session starts so a failure never leaves an orphaned renderer playing.
	s.clearCastSession(true)

	session, rpcErr := s.startCastDirect(ctx, kind, p, title, mode)
	if session == nil && rpcErr == nil {
		var localIP string
		localIP, err = localIPForHost(host)
		if err != nil {
			return nil, jsonrpc.Errorf(jsonrpc.CodeInternalError, "当前网络无法投屏: %v", err)
		}
		var gateway *streamGateway
		gateway, err = newStreamGatewayOn(s.opt.Logger, localIP)
		if err != nil {
			return nil, jsonrpc.Errorf(jsonrpc.CodeInternalError, "start cast gateway: %v", err)
		}
		s.opt.Logger.Info("cast gateway", "device", host, "bind", localIP)
		session, rpcErr = s.startCastMedia(ctx, kind, p, gateway, title)
		if rpcErr != nil {
			gateway.Close()
			return nil, rpcErr
		}
		if session == nil {
			gateway.Close()
			return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "unsupported cast content")
		}
		session.Mode = "proxy"
		session.MIMEType = "application/vnd.apple.mpegurl"
	} else if rpcErr != nil {
		return nil, rpcErr
	}

	device := cast.Device{
		ID:             strings.TrimSpace(p.String("device_id")),
		Name:           strings.TrimSpace(p.String("device_name")),
		AVTransportURL: controlURL,
		Host:           host,
	}
	if device.Name == "" {
		device.Name = host
	}

	metadata := cast.VideoMetadata(session.Title, session.PlaybackURL, session.MIMEType)
	// Control calls get their own budget: the RPC context may be cancelled by
	// the host right after the phone hands the URL to the TV.
	controlCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := cast.SetAVTransportURI(controlCtx, controlURL, session.PlaybackURL, metadata); err != nil {
		session.closeGateway()
		return nil, jsonrpc.Errorf(jsonrpc.CodeInternalError, "电视拒绝播放: %v", err)
	}
	if position := p.IntDefault("position_ms", 0); position > 1000 {
		_ = cast.Seek(controlCtx, controlURL, time.Duration(position)*time.Millisecond)
	}
	if err := cast.Play(controlCtx, controlURL); err != nil {
		_ = cast.Stop(controlCtx, controlURL)
		session.closeGateway()
		return nil, jsonrpc.Errorf(jsonrpc.CodeInternalError, "电视无法开始播放: %v", err)
	}

	s.opt.Logger.Info("cast started", "mode", session.Mode, "kind", session.Kind, "device", device.Name)
	session.Device = device
	s.castMu.Lock()
	s.activeCast = session
	s.castMu.Unlock()

	out := map[string]any{
		"playing":    true,
		"mode":       session.Mode,
		"kind":       session.Kind,
		"title":      session.Title,
		"device":     device,
		"started_at": session.StartedAt.Unix(),
	}
	if session.Mode == "proxy" {
		out["session_id"] = session.SessionID
		out["playback_url"] = session.PlaybackURL
	}
	return out, nil
}

// startCastDirect resolves a UGC play URL the renderer can fetch on its own.
// The html5 platform returns a complete MP4 durl that does not require the
// web Referer, so the TV plays straight from the CDN and the host process is
// not part of the playback path. PGC and live stay on the LAN proxy.
func (s *Service) startCastDirect(ctx context.Context, kind string, p jsonrpc.Params, title, mode string) (*castSession, *jsonrpc.Error) {
	if kind != "video" {
		if mode == "direct" {
			return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "direct casting only supports UGC videos")
		}
		return nil, nil
	}
	if mode == "proxy" {
		return nil, nil
	}
	bvid := strings.TrimSpace(p.String("bvid"))
	cid, ok := p.ID("cid")
	if !ok || cid <= 0 {
		var rpcErr *jsonrpc.Error
		cid, rpcErr = s.resolveStreamCID(ctx, bvid, p.IntDefault("page", 1))
		if rpcErr != nil {
			if mode == "direct" {
				return nil, rpcErr
			}
			return nil, nil
		}
	}
	query := url.Values{
		"bvid":         {bvid},
		"cid":          {strconv.FormatInt(cid, 10)},
		"qn":           {strconv.FormatInt(p.IntDefault("qn", 80), 10)},
		"fnval":        {"1"},
		"fnver":        {"0"},
		"fourk":        {"1"},
		"platform":     {"html5"},
		"high_quality": {"1"},
		"try_look":     {"1"},
	}
	result, rpcErr := s.endpointRequest(ctx, "ugcUrl", requestOptions{account: accountRef(p), query: query})
	if rpcErr != nil {
		if mode == "direct" {
			return nil, rpcErr
		}
		return nil, nil
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInternalError, "encode cast playurl: %v", err)
	}
	var payload castDurlPayload
	if err := json.Unmarshal(encoded, &payload); err != nil {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInternalError, "decode cast playurl: %v", err)
	}
	if payload.Code != 0 || len(payload.Data.Durl) == 0 {
		if mode == "direct" {
			message := payload.Message
			if message == "" {
				message = "该视频没有可用的直链"
			}
			return nil, jsonrpc.Errorf(jsonrpc.CodeInternalError, "%s (code %d)", message, payload.Code)
		}
		return nil, nil
	}
	media := strings.TrimSpace(payload.Data.Durl[0].URL)
	if media == "" {
		return nil, nil
	}
	if title == "" {
		title = "B站视频"
	}
	return &castSession{
		Kind:        kind,
		Mode:        "direct",
		Title:       title,
		PlaybackURL: media,
		MIMEType:    "video/mp4",
		StartedAt:   time.Now(),
	}, nil
}

type castDurlPayload struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    struct {
		Quality int `json:"quality"`
		Durl    []struct {
			Order  int    `json:"order"`
			Length int64  `json:"length"`
			Size   int64  `json:"size"`
			URL    string `json:"url"`
		} `json:"durl"`
	} `json:"data"`
}

func (c *castSession) closeGateway() {
	if c.Gateway != nil {
		c.Gateway.Close()
	}
}

// startCastMedia resolves the requested content on the LAN gateway. The
// returned session carries the URL the renderer should play.
func (s *Service) startCastMedia(ctx context.Context, kind string, p jsonrpc.Params, gateway *streamGateway, title string) (*castSession, *jsonrpc.Error) {
	params := map[string]any{}
	var handler func(context.Context, json.RawMessage, *streamGateway) (any, *jsonrpc.Error)
	switch kind {
	case "video":
		params["bvid"] = strings.TrimSpace(p.String("bvid"))
		params["page"] = p.IntDefault("page", 1)
		params["qn"] = p.IntDefault("qn", 80)
		params["codec"] = p.StringDefault("codec", "hevc")
		if cid, ok := p.ID("cid"); ok && cid > 0 {
			params["cid"] = cid
		}
		handler = s.streamStartOn
	case "pgc":
		if seasonID, ok := p.ID("season_id"); ok && seasonID > 0 {
			params["season_id"] = seasonID
		}
		if epID, ok := p.ID("ep_id"); ok && epID > 0 {
			params["ep_id"] = epID
		}
		if cid, ok := p.ID("cid"); ok && cid > 0 {
			params["cid"] = cid
		}
		params["qn"] = p.IntDefault("qn", 80)
		params["codec"] = p.StringDefault("codec", "hevc")
		if p.Bool("try_look", false) {
			params["try_look"] = true
		}
		handler = s.pgcPlayOn
	case "live":
		roomID, _ := p.ID("room_id")
		params["room_id"] = roomID
		params["qn"] = p.IntDefault("qn", 0)
		handler = s.livePlayOn
	default:
		return nil, nil
	}
	if rawAccount, ok := p.Raw("account"); ok {
		params["account"] = rawAccount
	}

	encoded, err := json.Marshal(params)
	if err != nil {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInternalError, "encode cast params: %v", err)
	}
	result, rpcErr := handler(ctx, encoded, gateway)
	if rpcErr != nil {
		return nil, rpcErr
	}
	out, _ := result.(map[string]any)
	playable, hasPlayable := out["playable"].(bool)
	if hasPlayable && !playable {
		reason := stringField(out, "reason")
		message := stringField(out, "message")
		if message == "" {
			message = castUnavailableMessage(reason)
		}
		return nil, jsonrpc.Errorf(jsonrpc.CodeInternalError, "%s", message)
	}
	playbackURL := stringField(out, "playback_url")
	if playbackURL == "" {
		// stream.start reports its HLS master as master_url; PGC and live use
		// playback_url.
		playbackURL = stringField(out, "master_url")
	}
	if playbackURL == "" {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInternalError, "投屏流地址为空")
	}
	sessionID := stringField(out, "session_id")
	if title == "" {
		switch kind {
		case "live":
			if room, ok := out["room"].(map[string]any); ok {
				title = stringField(room, "title")
			}
		case "pgc":
			if season, ok := out["season"].(map[string]any); ok {
				title = stringField(season, "title")
			}
		}
	}
	if title == "" {
		switch kind {
		case "video":
			title = "B站视频"
		case "live":
			title = "B站直播"
		case "pgc":
			title = "B站番剧"
		}
	}
	return &castSession{
		Kind:        kind,
		Title:       title,
		SessionID:   sessionID,
		PlaybackURL: playbackURL,
		Gateway:     gateway,
		StartedAt:   time.Now(),
	}, nil
}

func (s *Service) castStop(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	previous := s.clearCastSession(true)
	return map[string]any{"stopped": previous != nil}, nil
}

func (s *Service) castStatus(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	s.castMu.Lock()
	active := s.activeCast
	s.castMu.Unlock()
	if active == nil {
		return map[string]any{"active": false}, nil
	}
	return map[string]any{
		"active":     true,
		"mode":       active.Mode,
		"kind":       active.Kind,
		"title":      active.Title,
		"device":     active.Device,
		"started_at": active.StartedAt.Unix(),
	}, nil
}

func (s *Service) clearCastSession(remote bool) *castSession {
	s.castMu.Lock()
	previous := s.activeCast
	s.activeCast = nil
	s.castMu.Unlock()
	if previous == nil {
		return nil
	}
	if remote {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		_ = cast.Stop(ctx, previous.Device.AVTransportURL)
		cancel()
	}
	previous.closeGateway()
	return previous
}

// lanControlURL accepts only renderer control endpoints on private or
// link-local addresses so cast.play cannot be turned into an arbitrary HTTP
// POST client.
func lanControlURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("invalid control URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("control URL must be http or https")
	}
	ip := net.ParseIP(parsed.Hostname())
	if ip == nil {
		return nil, fmt.Errorf("control URL host must be an IP address")
	}
	if ip.IsLoopback() || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalMulticast() {
		return nil, fmt.Errorf("control URL host must be on the local network")
	}
	if !ip.IsPrivate() && !ip.IsLinkLocalUnicast() && !isCGNAT(ip) {
		return nil, fmt.Errorf("control URL host must be on the local network")
	}
	return parsed, nil
}

func isCGNAT(ip net.IP) bool {
	v4 := ip.To4()
	return v4 != nil && v4[0] == 100 && v4[1]&0xc0 == 64
}

// localIPForHost returns the local interface address used to reach the TV. The
// cast gateway binds to it, and its URLs are built from it, so the renderer
// always sees a route it can answer.
func localIPForHost(host string) (string, error) {
	conn, err := net.Dial("udp", net.JoinHostPort(host, "9"))
	if err != nil {
		return "", err
	}
	defer conn.Close()
	addr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok || addr.IP == nil || addr.IP.IsUnspecified() {
		return "", fmt.Errorf("no local address for %s", host)
	}
	return addr.IP.String(), nil
}

func castUnavailableMessage(reason string) string {
	switch reason {
	case "not_live":
		return "主播尚未开播"
	case "auth_required":
		return "登录后才能观看"
	case "entitlement_required":
		return "当前账号没有该剧集的播放权限"
	case "region_limited":
		return "该内容在当前地区不可播放"
	case "not_found":
		return "内容不存在或已下架"
	case "hls_unavailable":
		return "该内容没有可投屏的流"
	case "risk_control":
		return "请求被风控拦截，请稍后重试"
	default:
		return "内容暂不可投屏"
	}
}
