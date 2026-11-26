package service

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cloudflypeng/bbcore/internal/jsonrpc"
)

const streamUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Safari/605.1.15"

type streamGateway struct {
	logger     *slog.Logger
	client     *http.Client
	liveClient *http.Client
	listener   net.Listener
	server     *http.Server
	baseURL    string

	downloadDir string

	mu        sync.Mutex
	sessions  map[string]*streamSession
	liveRooms map[string]*liveSession
	liveHosts []string
	closed    bool
}

type liveSession struct {
	id           string
	playlistURL  string
	expiresAt    time.Time
	urlExpiresAt time.Time
}

var defaultLiveHosts = []string{
	"bilivideo.com",
	"bilivideo.cn",
	"bilivideo.net",
	"bilivideo.com.cn",
	"hdslb.com",
	"bilibili.com",
	"acgvideo.com",
	"szbdyd.com",
	"akamaized.net",
}

type streamSession struct {
	id             string
	video          *streamTrack
	audio          *streamTrack
	videoSegments  []hlsSegment
	audioSegments  []hlsSegment
	masterPlaylist string
	videoPlaylist  string
	audioPlaylist  string
	expiresAt      time.Time
}

type streamTrack struct {
	source   streamSource
	initData []byte
	segments []hlsSegment
}

type hlsSegment struct {
	offset   int64
	size     int64
	duration float64
}

func (s *Service) streamGateway() (*streamGateway, *jsonrpc.Error) {
	s.streamMu.Lock()
	defer s.streamMu.Unlock()
	if s.streams == nil {
		gateway, err := newStreamGateway(s.opt.Logger)
		if err != nil {
			return nil, jsonrpc.Errorf(jsonrpc.CodeInternalError, "start HLS gateway: %v", err)
		}
		if strings.TrimSpace(s.opt.DataDir) != "" {
			gateway.downloadDir = filepath.Join(s.opt.DataDir, "downloads")
		}
		s.streams = gateway
	}
	return s.streams, nil
}

func newStreamGateway(logger *slog.Logger) (*streamGateway, error) {
	return newStreamGatewayOn(logger, "127.0.0.1")
}

// newStreamGatewayOn binds the gateway to one local address. Casting uses the
// address that routes to the TV so only that LAN interface is exposed.
func newStreamGatewayOn(logger *slog.Logger, bindHost string) (*streamGateway, error) {
	if logger == nil {
		logger = slog.Default()
	}
	bindHost = strings.TrimSpace(bindHost)
	if bindHost == "" {
		bindHost = "127.0.0.1"
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(bindHost, "0"))
	if err != nil {
		return nil, err
	}
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          16,
		MaxIdleConnsPerHost:   8,
		IdleConnTimeout:       90 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second,
	}
	gateway := &streamGateway{
		logger:    logger,
		listener:  listener,
		baseURL:   "http://" + net.JoinHostPort(bindHost, strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)),
		sessions:  map[string]*streamSession{},
		liveRooms: map[string]*liveSession{},
		liveHosts: defaultLiveHosts,
		client:    &http.Client{Transport: transport},
	}
	gateway.liveClient = &http.Client{
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects")
			}
			if !gateway.liveURLAllowed(req.URL.String()) {
				return fmt.Errorf("redirect host is not allowed")
			}
			return nil
		},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/s/", gateway.handle)
	mux.HandleFunc("/p/", gateway.handle)
	mux.HandleFunc("/l/", gateway.handleLive)
	mux.HandleFunc("/d/", gateway.handleDownload)
	gateway.server = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		if err := gateway.server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			gateway.logger.Warn("HLS gateway stopped", "error", err)
		}
	}()
	return gateway, nil
}

func (g *streamGateway) Close() {
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		return
	}
	g.closed = true
	g.sessions = map[string]*streamSession{}
	g.liveRooms = map[string]*liveSession{}
	g.mu.Unlock()
	_ = g.server.Close()
}

func (g *streamGateway) stop(id string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, ok := g.sessions[id]; ok {
		delete(g.sessions, id)
		return true
	}
	if _, ok := g.liveRooms[id]; ok {
		delete(g.liveRooms, id)
		return true
	}
	return false
}

func (g *streamGateway) start(ctx context.Context, video *streamSource, audio *streamSource) (*streamSession, error) {
	if video == nil {
		return nil, fmt.Errorf("video stream is required")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	videoInit, err := g.fetchRange(ctx, video.urls, video.initStart, video.initEnd)
	if err != nil {
		return nil, fmt.Errorf("fetch video init: %w", err)
	}
	videoIndex, err := g.fetchRange(ctx, video.urls, video.indexStart, video.indexEnd)
	if err != nil {
		return nil, fmt.Errorf("fetch video index: %w", err)
	}
	videoSegments, err := parseSIDX(videoIndex, video.indexStart)
	if err != nil {
		return nil, fmt.Errorf("parse video index: %w", err)
	}
	if err := g.validateSegment(ctx, video.urls, videoSegments[0]); err != nil {
		return nil, fmt.Errorf("validate video index: %w", err)
	}
	videoTrack := &streamTrack{source: *video, initData: videoInit, segments: videoSegments}

	var audioTrack *streamTrack
	var audioSegments []hlsSegment
	if audio != nil {
		audioInit, err := g.fetchRange(ctx, audio.urls, audio.initStart, audio.initEnd)
		if err != nil {
			return nil, fmt.Errorf("fetch audio init: %w", err)
		}
		audioIndex, err := g.fetchRange(ctx, audio.urls, audio.indexStart, audio.indexEnd)
		if err != nil {
			return nil, fmt.Errorf("fetch audio index: %w", err)
		}
		audioSegments, err = parseSIDX(audioIndex, audio.indexStart)
		if err != nil {
			return nil, fmt.Errorf("parse audio index: %w", err)
		}
		if err := g.validateSegment(ctx, audio.urls, audioSegments[0]); err != nil {
			return nil, fmt.Errorf("validate audio index: %w", err)
		}
		audioTrack = &streamTrack{source: *audio, initData: audioInit, segments: audioSegments}
	}

	videoPlaylist, err := mediaPlaylist(videoSegments, "video.mp4", "video-init.mp4")
	if err != nil {
		return nil, err
	}
	audioPlaylist := ""
	if audioTrack != nil {
		audioPlaylist, err = mediaPlaylist(audioSegments, "audio.mp4", "audio-init.mp4")
		if err != nil {
			return nil, err
		}
	}
	master := masterPlaylist(videoTrack, audioTrack)

	id, err := sessionID()
	if err != nil {
		return nil, err
	}
	session := &streamSession{
		id:             id,
		video:          videoTrack,
		audio:          audioTrack,
		videoSegments:  videoSegments,
		audioSegments:  audioSegments,
		masterPlaylist: master,
		videoPlaylist:  videoPlaylist,
		audioPlaylist:  audioPlaylist,
		expiresAt:      time.Now().Add(2 * time.Hour),
	}

	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		return nil, fmt.Errorf("gateway is closed")
	}
	g.sessions[id] = session
	now := time.Now()
	for key, existing := range g.sessions {
		if now.After(existing.expiresAt) {
			delete(g.sessions, key)
		}
	}
	g.mu.Unlock()

	return session, nil
}

func (g *streamGateway) session(id string) *streamSession {
	g.mu.Lock()
	defer g.mu.Unlock()
	session := g.sessions[id]
	if session == nil {
		return nil
	}
	now := time.Now()
	if now.After(session.expiresAt) {
		delete(g.sessions, id)
		return nil
	}
	session.expiresAt = now.Add(2 * time.Hour)
	return session
}

func (g *streamGateway) startLive(ctx context.Context, playlistURL string) (*liveSession, error) {
	playlistURL = strings.TrimSpace(playlistURL)
	if !g.liveURLAllowed(playlistURL) {
		return nil, fmt.Errorf("live playlist host is not allowed")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	playlist, _, err := g.fetchLivePlaylist(ctx, playlistURL)
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(strings.TrimSpace(playlist), "#EXTM3U") {
		return nil, fmt.Errorf("upstream live playlist is not HLS")
	}

	id, err := sessionID()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	urlExpiresAt := liveURLExpiry(playlistURL)
	expiresAt := now.Add(30 * time.Minute)
	if !urlExpiresAt.IsZero() && urlExpiresAt.Before(expiresAt) {
		expiresAt = urlExpiresAt
	}
	session := &liveSession{
		id:           id,
		playlistURL:  playlistURL,
		expiresAt:    expiresAt,
		urlExpiresAt: urlExpiresAt,
	}

	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		return nil, fmt.Errorf("gateway is closed")
	}
	g.liveRooms[id] = session
	for key, existing := range g.liveRooms {
		if now.After(existing.expiresAt) {
			delete(g.liveRooms, key)
		}
	}
	g.mu.Unlock()
	return session, nil
}

func (g *streamGateway) liveSession(id string) *liveSession {
	g.mu.Lock()
	defer g.mu.Unlock()
	session := g.liveRooms[id]
	if session == nil {
		return nil
	}
	now := time.Now()
	if now.After(session.expiresAt) {
		delete(g.liveRooms, id)
		return nil
	}
	expiresAt := now.Add(30 * time.Minute)
	if !session.urlExpiresAt.IsZero() && session.urlExpiresAt.Before(expiresAt) {
		expiresAt = session.urlExpiresAt
	}
	session.expiresAt = expiresAt
	return session
}

func (g *streamGateway) handleLive(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) != 3 || parts[0] != "l" {
		http.NotFound(w, r)
		return
	}
	session := g.liveSession(parts[1])
	if session == nil {
		http.NotFound(w, r)
		return
	}

	switch parts[2] {
	case "playlist.m3u8":
		playlist, base, err := g.fetchLivePlaylist(r.Context(), session.playlistURL)
		if err != nil {
			http.Error(w, fmt.Sprintf("upstream live playlist failed: %v", err), http.StatusBadGateway)
			return
		}
		writeText(w, r, "application/vnd.apple.mpegurl", g.rewriteLivePlaylist(playlist, base, session.id))
	case "seg":
		rawURL := r.URL.Query().Get("u")
		if !g.liveURLAllowed(rawURL) {
			http.Error(w, "forbidden live segment host", http.StatusForbidden)
			return
		}
		g.proxyLiveSegment(w, r, rawURL)
	default:
		http.NotFound(w, r)
	}
}

func (g *streamGateway) fetchLivePlaylist(ctx context.Context, rawURL string) (string, *url.URL, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", nil, err
	}
	setUpstreamHeaders(req)
	resp, err := g.liveClient.Do(req)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", nil, fmt.Errorf("upstream %s returned %s", hostOf(rawURL), resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", nil, err
	}
	base := resp.Request.URL
	if base == nil {
		base, _ = url.Parse(rawURL)
	}
	return string(data), base, nil
}

func (g *streamGateway) rewriteLivePlaylist(playlist string, base *url.URL, sessionID string) string {
	lines := strings.Split(playlist, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			lines[i] = g.rewriteLiveTagURIs(line, base, sessionID)
			continue
		}
		lines[i] = g.proxyLiveURL(trimmed, base, sessionID)
	}
	return strings.Join(lines, "\n")
}

func (g *streamGateway) rewriteLiveTagURIs(line string, base *url.URL, sessionID string) string {
	const marker = `URI="`
	var builder strings.Builder
	rest := line
	for {
		index := strings.Index(rest, marker)
		if index < 0 {
			builder.WriteString(rest)
			break
		}
		builder.WriteString(rest[:index+len(marker)])
		rest = rest[index+len(marker):]
		end := strings.IndexByte(rest, '"')
		if end < 0 {
			builder.WriteString(rest)
			break
		}
		builder.WriteString(g.proxyLiveURL(rest[:end], base, sessionID))
		builder.WriteByte('"')
		rest = rest[end+1:]
	}
	return builder.String()
}

func (g *streamGateway) proxyLiveURL(raw string, base *url.URL, sessionID string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return raw
	}
	resolved := raw
	if base != nil {
		if next, err := base.Parse(raw); err == nil {
			resolved = next.String()
		}
	}
	if !g.liveURLAllowed(resolved) {
		return raw
	}
	return "/l/" + sessionID + "/seg?u=" + url.QueryEscape(resolved)
}

func (g *streamGateway) proxyLiveSegment(w http.ResponseWriter, r *http.Request, rawURL string) {
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, rawURL, nil)
	if err != nil {
		http.Error(w, "invalid live segment URL", http.StatusBadRequest)
		return
	}
	setUpstreamHeaders(req)
	if rangeHeader := strings.TrimSpace(r.Header.Get("Range")); rangeHeader != "" {
		req.Header.Set("Range", rangeHeader)
	}
	resp, err := g.liveClient.Do(req)
	if err != nil {
		http.Error(w, fmt.Sprintf("upstream live segment failed: %v", err), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		http.Error(w, fmt.Sprintf("upstream %s returned %s", hostOf(rawURL), resp.Status), http.StatusBadGateway)
		return
	}

	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = liveSegmentContentType(rawURL)
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Accept-Ranges", "bytes")
	for _, name := range []string{"Content-Length", "Content-Range", "ETag", "Last-Modified"} {
		if value := resp.Header.Get(name); value != "" {
			w.Header().Set(name, value)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func (g *streamGateway) liveURLAllowed(rawURL string) bool {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Hostname() == "" {
		return false
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	for _, domain := range g.liveHosts {
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return true
		}
	}
	return false
}

func liveURLExpiry(rawURL string) time.Time {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return time.Time{}
	}
	raw := parsed.Query().Get("expires")
	if raw == "" {
		return time.Time{}
	}
	seconds, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || seconds <= 0 {
		return time.Time{}
	}
	return time.Unix(seconds-30, 0)
}

func liveSegmentContentType(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "application/octet-stream"
	}
	path := strings.ToLower(parsed.Path)
	switch {
	case strings.HasSuffix(path, ".ts"):
		return "video/mp2t"
	case strings.HasSuffix(path, ".m4s"), strings.HasSuffix(path, ".mp4"):
		return "video/mp4"
	default:
		return "application/octet-stream"
	}
}

func (g *streamGateway) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) != 3 || (parts[0] != "s" && parts[0] != "p") {
		http.NotFound(w, r)
		return
	}
	session := g.session(parts[1])
	if session == nil {
		http.NotFound(w, r)
		return
	}
	g.logger.Debug("HLS gateway request", "path", r.URL.Path, "remote", r.RemoteAddr)

	switch parts[2] {
	case "master.m3u8":
		writeText(w, r, "application/vnd.apple.mpegurl", session.masterPlaylist)
	case "video.m3u8":
		writeText(w, r, "application/vnd.apple.mpegurl", session.videoPlaylist)
	case "audio.m3u8":
		if session.audio == nil {
			http.NotFound(w, r)
			return
		}
		writeText(w, r, "application/vnd.apple.mpegurl", session.audioPlaylist)
	case "video-init.mp4":
		writeBytes(w, r, "video/mp4", session.video.initData)
	case "audio-init.mp4":
		if session.audio == nil {
			http.NotFound(w, r)
			return
		}
		writeBytes(w, r, "audio/mp4", session.audio.initData)
	case "video.mp4":
		g.proxyTrack(w, r, session.video)
	case "audio.mp4":
		if session.audio == nil {
			http.NotFound(w, r)
			return
		}
		g.proxyTrack(w, r, session.audio)
	default:
		http.NotFound(w, r)
	}
}

func (g *streamGateway) handleDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) != 3 || parts[0] != "d" || g.downloadDir == "" {
		http.NotFound(w, r)
		return
	}
	id := parts[1]
	if !validDownloadID(id) {
		http.NotFound(w, r)
		return
	}
	path := filepath.Join(g.downloadDir, id, parts[2])
	switch parts[2] {
	case "master.m3u8", "video.m3u8", "audio.m3u8":
		data, err := os.ReadFile(path)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		writeText(w, r, "application/vnd.apple.mpegurl", string(data))
	case "video-init.mp4", "audio-init.mp4", "video.mp4", "audio.mp4":
		file, err := os.Open(path)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil || info.IsDir() {
			http.NotFound(w, r)
			return
		}
		if strings.HasPrefix(parts[2], "audio") {
			w.Header().Set("Content-Type", "audio/mp4")
		} else {
			w.Header().Set("Content-Type", "video/mp4")
		}
		http.ServeContent(w, r, parts[2], info.ModTime(), file)
	default:
		http.NotFound(w, r)
	}
}

func validDownloadID(id string) bool {
	if len(id) != 16 {
		return false
	}
	for _, r := range id {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

func (g *streamGateway) proxyTrack(w http.ResponseWriter, r *http.Request, track *streamTrack) {
	rangeHeader := strings.TrimSpace(r.Header.Get("Range"))
	if r.Method == http.MethodHead {
		w.Header().Set("Content-Type", track.source.mimeType)
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		return
	}

	var lastErr error
	for _, rawURL := range track.source.urls {
		req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, rawURL, nil)
		if err != nil {
			lastErr = err
			continue
		}
		setUpstreamHeaders(req)
		if rangeHeader != "" {
			req.Header.Set("Range", rangeHeader)
		}
		resp, err := g.client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode != http.StatusPartialContent &&
			!(resp.StatusCode == http.StatusOK && rangeHeader == "") {
			lastErr = fmt.Errorf("upstream %s returned %s", hostOf(rawURL), resp.Status)
			resp.Body.Close()
			continue
		}

		w.Header().Set("Content-Type", track.source.mimeType)
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Cache-Control", "no-store")
		for _, name := range []string{"Content-Length", "Content-Range", "ETag", "Last-Modified"} {
			if value := resp.Header.Get(name); value != "" {
				w.Header().Set(name, value)
			}
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
		resp.Body.Close()
		return
	}
	http.Error(w, fmt.Sprintf("upstream range request failed: %v", lastErr), http.StatusBadGateway)
}

func (g *streamGateway) fetchRange(ctx context.Context, urls []string, start, end int64) ([]byte, error) {
	if start < 0 || end < start {
		return nil, fmt.Errorf("invalid byte range %d-%d", start, end)
	}
	expected := end - start + 1
	var lastErr error
	for _, rawURL := range urls {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			lastErr = err
			continue
		}
		setUpstreamHeaders(req)
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
		resp, err := g.client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode != http.StatusPartialContent &&
			!(resp.StatusCode == http.StatusOK && start == 0) {
			lastErr = fmt.Errorf("upstream %s returned %s", hostOf(rawURL), resp.Status)
			resp.Body.Close()
			continue
		}
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, expected+1))
		resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			continue
		}
		if int64(len(data)) < expected {
			lastErr = fmt.Errorf("short range response: got %d bytes, want %d", len(data), expected)
			continue
		}
		return data[:expected], nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no upstream URL")
	}
	return nil, lastErr
}

func (g *streamGateway) validateSegment(ctx context.Context, urls []string, segment hlsSegment) error {
	data, err := g.fetchRange(ctx, urls, segment.offset, segment.offset+11)
	if err != nil {
		return err
	}
	if len(data) < 8 {
		return fmt.Errorf("segment header too short")
	}
	boxType := string(data[4:8])
	if boxType != "moof" && boxType != "styp" {
		return fmt.Errorf("segment starts with %q, expected moof/styp", boxType)
	}
	return nil
}

func mediaPlaylist(segments []hlsSegment, mediaName, initName string) (string, error) {
	if len(segments) == 0 {
		return "", fmt.Errorf("no HLS segments")
	}
	maxDuration := 0.0
	for _, segment := range segments {
		if segment.size <= 0 || segment.duration <= 0 {
			return "", fmt.Errorf("invalid HLS segment")
		}
		if segment.duration > maxDuration {
			maxDuration = segment.duration
		}
	}
	targetDuration := int(math.Ceil(maxDuration))
	if targetDuration < 1 {
		targetDuration = 1
	}

	var builder strings.Builder
	builder.WriteString("#EXTM3U\n")
	builder.WriteString("#EXT-X-VERSION:7\n")
	builder.WriteString("#EXT-X-PLAYLIST-TYPE:VOD\n")
	builder.WriteString("#EXT-X-INDEPENDENT-SEGMENTS\n")
	fmt.Fprintf(&builder, "#EXT-X-TARGETDURATION:%d\n", targetDuration)
	builder.WriteString("#EXT-X-MEDIA-SEQUENCE:0\n")
	fmt.Fprintf(&builder, "#EXT-X-MAP:URI=%q\n", initName)
	for _, segment := range segments {
		fmt.Fprintf(&builder, "#EXTINF:%.6f,\n", segment.duration)
		fmt.Fprintf(&builder, "#EXT-X-BYTERANGE:%d@%d\n", segment.size, segment.offset)
		builder.WriteString(mediaName)
		builder.WriteByte('\n')
	}
	builder.WriteString("#EXT-X-ENDLIST\n")
	return builder.String(), nil
}

func masterPlaylist(video, audio *streamTrack) string {
	var builder strings.Builder
	builder.WriteString("#EXTM3U\n")
	builder.WriteString("#EXT-X-VERSION:7\n")
	builder.WriteString("#EXT-X-INDEPENDENT-SEGMENTS\n")
	if audio != nil {
		builder.WriteString("#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"audio\",NAME=\"audio\",DEFAULT=YES,AUTOSELECT=YES,URI=\"audio.m3u8\"\n")
	}

	videoPeak, videoAverage := trackBitrates(video)
	audioPeak, audioAverage := trackBitrates(audio)
	bandwidth := videoPeak + audioPeak
	average := videoAverage + audioAverage
	if bandwidth > 0 {
		// EXT-X-STREAM-INF BANDWIDTH is the peak segment bit rate; upstream
		// playurl reports only the average, which AVPlayer rejects with
		// -12318 and can leave playback waiting to minimize stalls.
		bandwidth = bandwidth * 105 / 100
	} else {
		bandwidth = video.source.bandwidth
		if audio != nil {
			bandwidth += audio.source.bandwidth
		}
		average = bandwidth
	}
	if average <= 0 || average > bandwidth {
		average = bandwidth
	}
	codecs := video.source.codecs
	if audio != nil {
		audioCodecs := audio.source.codecs
		if audioCodecs == "" {
			audioCodecs = "mp4a.40.2"
		}
		if codecs != "" {
			codecs += ","
		}
		codecs += audioCodecs
	}
	if bandwidth <= 0 {
		bandwidth = 1
	}

	fmt.Fprintf(&builder, "#EXT-X-STREAM-INF:BANDWIDTH=%d,AVERAGE-BANDWIDTH=%d", bandwidth, average)
	if video.source.width > 0 && video.source.height > 0 {
		fmt.Fprintf(&builder, ",RESOLUTION=%dx%d", video.source.width, video.source.height)
	}
	if video.source.frameRate > 0 {
		fmt.Fprintf(&builder, ",FRAME-RATE=%.3f", video.source.frameRate)
	}
	if codecs != "" {
		fmt.Fprintf(&builder, ",CODECS=%q", codecs)
	}
	if audio != nil {
		builder.WriteString(",AUDIO=\"audio\"")
	}
	builder.WriteString("\nvideo.m3u8\n")
	return builder.String()
}

func trackBitrates(track *streamTrack) (peak, average int64) {
	if track == nil {
		return 0, 0
	}
	var totalBytes int64
	var totalDuration float64
	for _, segment := range track.segments {
		if segment.size <= 0 || segment.duration <= 0 {
			continue
		}
		totalBytes += segment.size
		totalDuration += segment.duration
		if rate := int64(float64(segment.size) * 8 / segment.duration); rate > peak {
			peak = rate
		}
	}
	if totalDuration > 0 {
		average = int64(float64(totalBytes) * 8 / totalDuration)
	}
	return peak, average
}

func parseSIDX(data []byte, indexStart int64) ([]hlsSegment, error) {
	offset := 0
	for offset+8 <= len(data) {
		size := int64(binary.BigEndian.Uint32(data[offset : offset+4]))
		boxType := string(data[offset+4 : offset+8])
		header := int64(8)
		if size == 1 {
			if offset+16 > len(data) {
				return nil, fmt.Errorf("truncated extended box header")
			}
			size = int64(binary.BigEndian.Uint64(data[offset+8 : offset+16]))
			header = 16
		} else if size == 0 {
			size = int64(len(data) - offset)
		}
		if size < header || int64(offset)+size > int64(len(data)) {
			return nil, fmt.Errorf("invalid box size %d at offset %d", size, offset)
		}
		if boxType == "sidx" {
			return parseSIDXBox(
				data[offset:offset+int(size)],
				header,
				indexStart+int64(offset),
				size,
			)
		}
		offset += int(size)
	}
	return nil, fmt.Errorf("sidx box not found")
}

func parseSIDXBox(box []byte, header, boxStart, boxSize int64) ([]hlsSegment, error) {
	payload := box[header:]
	if len(payload) < 12 {
		return nil, fmt.Errorf("sidx payload too short")
	}
	version := payload[0]
	position := 4
	if len(payload) < position+8 {
		return nil, fmt.Errorf("sidx timescale missing")
	}
	timescale := binary.BigEndian.Uint32(payload[position+4 : position+8])
	position += 8

	var firstOffset uint64
	if version == 0 {
		if len(payload) < position+8 {
			return nil, fmt.Errorf("sidx version 0 fields missing")
		}
		firstOffset = uint64(binary.BigEndian.Uint32(payload[position+4 : position+8]))
		position += 8
	} else if version == 1 {
		if len(payload) < position+16 {
			return nil, fmt.Errorf("sidx version 1 fields missing")
		}
		firstOffset = binary.BigEndian.Uint64(payload[position+8 : position+16])
		position += 16
	} else {
		return nil, fmt.Errorf("unsupported sidx version %d", version)
	}
	if timescale == 0 {
		return nil, fmt.Errorf("sidx timescale is zero")
	}

	if len(payload) < position+4 {
		return nil, fmt.Errorf("sidx reference count missing")
	}
	position += 2
	referenceCount := int(binary.BigEndian.Uint16(payload[position : position+2]))
	position += 2
	if referenceCount <= 0 {
		return nil, fmt.Errorf("sidx has no references")
	}
	if len(payload) < position+referenceCount*12 {
		return nil, fmt.Errorf("sidx references truncated")
	}

	offset := boxStart + boxSize + int64(firstOffset)
	segments := make([]hlsSegment, 0, referenceCount)
	for i := 0; i < referenceCount; i++ {
		word := binary.BigEndian.Uint32(payload[position : position+4])
		position += 4
		if word>>31 != 0 {
			return nil, fmt.Errorf("hierarchical sidx references are not supported")
		}
		size := int64(word & 0x7fffffff)
		duration := binary.BigEndian.Uint32(payload[position : position+4])
		position += 8 // duration and SAP word
		if size <= 0 {
			return nil, fmt.Errorf("sidx reference %d has zero size", i)
		}
		segments = append(segments, hlsSegment{
			offset:   offset,
			size:     size,
			duration: float64(duration) / float64(timescale),
		})
		offset += size
	}
	return segments, nil
}

func setUpstreamHeaders(req *http.Request) {
	req.Header.Set("User-Agent", streamUserAgent)
	req.Header.Set("Referer", "https://www.bilibili.com/")
	req.Header.Set("Accept-Encoding", "identity")
}

func hostOf(rawURL string) string {
	if index := strings.Index(rawURL, "://"); index >= 0 {
		rest := rawURL[index+3:]
		if slash := strings.IndexByte(rest, '/'); slash >= 0 {
			return rest[:slash]
		}
		return rest
	}
	return rawURL
}

func sessionID() (string, error) {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return hex.EncodeToString(buffer), nil
}

func writeText(w http.ResponseWriter, r *http.Request, contentType, text string) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", strconv.Itoa(len(text)))
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, text)
}

func writeBytes(w http.ResponseWriter, r *http.Request, contentType string, data []byte) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}
