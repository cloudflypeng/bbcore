// Package live implements the Bilibili live danmaku WebSocket protocol used by
// PiliPlus (lib/tcp/live.dart) and forwards messages to the host as JSON-RPC
// notifications.
package live

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cloudflypeng/bbcore/internal/bili"

	"github.com/andybalholm/brotli"
	"github.com/gorilla/websocket"
)

const (
	opHeartbeat      = 2
	opHeartbeatReply = 3
	opMessage        = 5
	opAuth           = 7
	opAuthReply      = 8

	danmuInfoPath = "/xlive/web-room/v1/index/getDanmuInfo"
)

// Emit sends one notification to the host.
type Emit func(method string, params any)

// Manager owns all live room subscriptions.
type Manager struct {
	client *bili.Client
	log    *slog.Logger
	emit   Emit
	mu     sync.Mutex
	rooms  map[int64]*room
}

// NewManager creates a live danmaku manager.
func NewManager(client *bili.Client, logger *slog.Logger, emit Emit) *Manager {
	if logger == nil {
		logger = slog.Default()
	}
	return &Manager{client: client, log: logger, emit: emit, rooms: map[int64]*room{}}
}

// ConnectInfo is returned after the websocket handshake and auth packet.
type ConnectInfo struct {
	RoomID int64  `json:"room_id"`
	Host   string `json:"host"`
	Token  string `json:"token,omitempty"`
	UID    int64  `json:"uid"`
}

// Connect starts a danmaku subscription for roomID. Reconnecting is handled
// automatically until Disconnect/Close.
func (m *Manager) Connect(ctx context.Context, roomID int64, cred *bili.Credential) (*ConnectInfo, error) {
	if roomID <= 0 {
		return nil, fmt.Errorf("room_id must be positive")
	}
	m.mu.Lock()
	if existing, ok := m.rooms[roomID]; ok {
		info := existing.snapshot()
		m.mu.Unlock()
		return &info, nil
	}
	runCtx, cancel := context.WithCancel(context.Background())
	r := &room{id: roomID, cred: cred, cancel: cancel, done: make(chan struct{})}
	m.rooms[roomID] = r
	m.mu.Unlock()

	conn, host, token, uid, err := m.dial(runCtx, r)
	if err != nil {
		cancel()
		m.mu.Lock()
		delete(m.rooms, roomID)
		m.mu.Unlock()
		close(r.done)
		return nil, err
	}
	r.mu.Lock()
	r.info = ConnectInfo{RoomID: roomID, Host: host, Token: token, UID: uid}
	r.mu.Unlock()

	go m.serve(runCtx, r, conn)
	return &ConnectInfo{RoomID: roomID, Host: host, Token: token, UID: uid}, nil
}

// Disconnect stops a subscription.
func (m *Manager) Disconnect(roomID int64) bool {
	m.mu.Lock()
	r, ok := m.rooms[roomID]
	if ok {
		delete(m.rooms, roomID)
	}
	m.mu.Unlock()
	if ok {
		r.stop()
		<-r.done
	}
	return ok
}

// Rooms lists active subscriptions.
func (m *Manager) Rooms() []int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]int64, 0, len(m.rooms))
	for id := range m.rooms {
		out = append(out, id)
	}
	return out
}

// Close stops every subscription.
func (m *Manager) Close() {
	m.mu.Lock()
	rooms := make([]*room, 0, len(m.rooms))
	for id, r := range m.rooms {
		rooms = append(rooms, r)
		delete(m.rooms, id)
	}
	m.mu.Unlock()
	for _, r := range rooms {
		r.stop()
	}
	for _, r := range rooms {
		<-r.done
	}
}

type room struct {
	id     int64
	cred   *bili.Credential
	cancel context.CancelFunc
	done   chan struct{}

	mu        sync.Mutex
	info      ConnectInfo
	conn      *websocket.Conn
	connected bool
}

func (r *room) snapshot() ConnectInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.info
}

func (r *room) stop() {
	r.cancel()
	r.mu.Lock()
	if r.conn != nil {
		_ = r.conn.Close()
	}
	r.mu.Unlock()
}

// serve reads from the current connection, reconnecting until runCtx ends.
func (m *Manager) serve(ctx context.Context, r *room, conn *websocket.Conn) {
	defer close(r.done)
	defer func() {
		if conn != nil {
			_ = conn.Close()
		}
	}()
	backoff := time.Second
	for {
		err := m.readLoop(ctx, r, conn)
		if ctx.Err() != nil {
			return
		}
		r.mu.Lock()
		r.connected = false
		r.conn = nil
		r.mu.Unlock()
		reason := "connection closed"
		if err != nil {
			reason = err.Error()
		}
		m.emit("event.live.disconnected", map[string]any{"room_id": r.id, "reason": reason})
		m.log.Warn("live stream disconnected", "room", r.id, "reason", reason)
		_ = conn.Close()
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
		newConn, _, _, _, dialErr := m.dial(ctx, r)
		if dialErr != nil {
			m.log.Warn("live stream reconnect failed", "room", r.id, "error", dialErr)
			continue
		}
		conn = newConn
		backoff = time.Second
	}
}

func (m *Manager) readLoop(ctx context.Context, r *room, conn *websocket.Conn) error {
	for {
		_, payload, err := conn.ReadMessage()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("read: %w", err)
		}
		m.processFrames(r, payload, 0)
	}
}

// dial fetches a fresh token, connects to a server and sends the auth packet.
func (m *Manager) dial(ctx context.Context, r *room) (*websocket.Conn, string, string, int64, error) {
	token, hosts, err := m.fetchToken(ctx, r.id, r.cred)
	if err != nil {
		return nil, "", "", 0, err
	}
	var conn *websocket.Conn
	var host string
	var dialErr error
	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	for _, candidate := range hosts {
		conn, _, dialErr = dialer.DialContext(ctx, "wss://"+candidate+"/sub", http.Header{
			"User-Agent": []string{bili.UserAgent},
			"Origin":     []string{"https://live.bilibili.com"},
		})
		if dialErr == nil {
			host = candidate
			break
		}
	}
	if conn == nil {
		return nil, "", "", 0, fmt.Errorf("connect danmaku servers: %w", dialErr)
	}
	conn.SetReadLimit(16 << 20)

	uid := int64(0)
	if r.cred != nil && r.cred.LoggedIn {
		uid = r.cred.Mid
	}
	authBody, _ := json.Marshal(map[string]any{
		"roomid":   r.id,
		"uid":      uid,
		"protover": 3,
		"platform": "web",
		"type":     2,
		"key":      token,
	})
	if err := conn.WriteMessage(websocket.BinaryMessage, makePacket(1, opAuth, 1, authBody)); err != nil {
		_ = conn.Close()
		return nil, "", "", 0, fmt.Errorf("send auth packet: %w", err)
	}

	r.mu.Lock()
	r.conn = conn
	r.info.Host = host
	r.info.Token = token
	r.info.UID = uid
	r.mu.Unlock()

	go heartbeat(ctx, conn)
	return conn, host, token, uid, nil
}

func heartbeat(ctx context.Context, conn *websocket.Conn) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	seq := uint32(1)
	send := func() bool {
		if err := conn.WriteMessage(websocket.BinaryMessage, makePacket(1, opHeartbeat, seq, nil)); err != nil {
			return false
		}
		seq++
		return true
	}
	// Servers expect a heartbeat within the first half minute.
	select {
	case <-ctx.Done():
		return
	case <-time.After(2 * time.Second):
		if !send() {
			return
		}
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !send() {
				return
			}
		}
	}
}

func (m *Manager) fetchToken(ctx context.Context, roomID int64, cred *bili.Credential) (string, []string, error) {
	result, err := m.client.Do(ctx, bili.Spec{
		Base:   bili.BaseLive,
		Method: http.MethodGet,
		Path:   danmuInfoPath,
		Query: map[string][]string{
			"id":           {strconv.FormatInt(roomID, 10)},
			"web_location": {"444.8"},
		},
		Sign:       bili.SignWBI,
		Credential: cred,
		Headers:    map[string]string{"Referer": fmt.Sprintf("https://live.bilibili.com/%d", roomID)},
	})
	if err != nil {
		return "", nil, err
	}
	root := result.JSONMap()
	if result.BiliCode() != 0 {
		return "", nil, fmt.Errorf("getDanmuInfo code=%d message=%s", result.BiliCode(), result.BiliMessage())
	}
	data, _ := root["data"].(map[string]any)
	token, _ := data["token"].(string)
	hosts := []string{}
	if list, ok := data["host_list"].([]any); ok {
		for _, item := range list {
			hostMap, _ := item.(map[string]any)
			h, _ := hostMap["host"].(string)
			if h == "" {
				continue
			}
			port := toInt64(hostMap["wss_port"])
			if port == 0 {
				port = 443
			}
			hosts = append(hosts, fmt.Sprintf("%s:%d", h, port))
		}
	}
	if token == "" || len(hosts) == 0 {
		return "", nil, fmt.Errorf("getDanmuInfo returned no token/hosts")
	}
	return token, hosts, nil
}

func (m *Manager) processFrames(r *room, payload []byte, depth int) {
	if depth > 4 {
		return
	}
	for len(payload) >= 16 {
		total := int(binary.BigEndian.Uint32(payload[0:4]))
		headerLen := int(binary.BigEndian.Uint16(payload[4:6]))
		protoVer := int(binary.BigEndian.Uint16(payload[6:8]))
		op := int(binary.BigEndian.Uint32(payload[8:12]))
		if total < headerLen || total > len(payload) || headerLen < 16 {
			return
		}
		body := payload[headerLen:total]
		switch op {
		case opHeartbeatReply:
			if len(body) >= 4 {
				m.emit("event.live.popularity", map[string]any{
					"room_id": r.id,
					"value":   int64(binary.BigEndian.Uint32(body[:4])),
				})
			}
		case opAuthReply:
			r.mu.Lock()
			r.connected = true
			r.mu.Unlock()
			m.emit("event.live.connected", map[string]any{"room_id": r.id})
		case opMessage:
			switch protoVer {
			case 0, 1:
				m.emitCommand(r, body)
			case 2:
				if data, err := zlibDecompress(body); err == nil {
					m.processFrames(r, data, depth+1)
				}
			case 3:
				m.processFrames(r, brotliDecompress(body), depth+1)
			}
		}
		payload = payload[total:]
	}
}

func (m *Manager) emitCommand(r *room, body []byte) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var data map[string]any
	if err := dec.Decode(&data); err != nil {
		return
	}
	cmd, _ := data["cmd"].(string)
	payload := map[string]any{"room_id": r.id, "cmd": cmd, "data": data}
	if strings.HasPrefix(cmd, "DANMU_MSG") {
		if summary, ok := summarizeDanmaku(data); ok {
			payload["danmaku"] = summary
		}
	}
	m.emit("event.live.message", payload)
}

// summarizeDanmaku extracts text/uid/uname from the DANMU_MSG payload.
func summarizeDanmaku(data map[string]any) (map[string]any, bool) {
	info, ok := data["info"].([]any)
	if !ok || len(info) < 2 {
		return nil, false
	}
	text, _ := info[1].(string)
	summary := map[string]any{"text": text}
	if first, ok := info[0].([]any); ok {
		if len(first) > 15 {
			if content, ok := first[15].(map[string]any); ok {
				if extra, ok := content["extra"].(string); ok && extra != "" {
					var parsed map[string]any
					if json.Unmarshal([]byte(extra), &parsed) == nil {
						summary["id"] = parsed["id_str"]
						summary["dm_type"] = parsed["dm_type"]
					}
				}
				if user, ok := content["user"].(map[string]any); ok {
					summary["uid"] = user["uid"]
					if base, ok := user["base"].(map[string]any); ok {
						summary["uname"] = base["name"]
					}
				}
			}
		}
		if len(first) > 3 {
			summary["fontsize"] = first[2]
			summary["color"] = first[3]
		}
		if len(first) > 9 {
			summary["mode"] = first[9]
		}
	}
	if len(info) > 2 {
		if second, ok := info[2].([]any); ok && len(second) >= 2 {
			if summary["uid"] == nil {
				summary["uid"] = second[0]
			}
			if summary["uname"] == nil {
				summary["uname"] = second[1]
			}
		}
	}
	return summary, true
}

func makePacket(protoVer int, op int, seq uint32, body []byte) []byte {
	buf := make([]byte, 16+len(body))
	binary.BigEndian.PutUint32(buf[0:4], uint32(16+len(body)))
	binary.BigEndian.PutUint16(buf[4:6], 16)
	binary.BigEndian.PutUint16(buf[6:8], uint16(protoVer))
	binary.BigEndian.PutUint32(buf[8:12], uint32(op))
	binary.BigEndian.PutUint32(buf[12:16], seq)
	copy(buf[16:], body)
	return buf
}

func zlibDecompress(data []byte) ([]byte, error) {
	reader, err := zlib.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return io.ReadAll(io.LimitReader(reader, 32<<20))
}

func brotliDecompress(data []byte) []byte {
	out, err := io.ReadAll(io.LimitReader(brotli.NewReader(bytes.NewReader(data)), 32<<20))
	if err != nil {
		return nil
	}
	return out
}

func toInt64(v any) int64 {
	switch t := v.(type) {
	case json.Number:
		n, _ := t.Int64()
		return n
	case float64:
		return int64(t)
	case int:
		return int64(t)
	case int64:
		return t
	case string:
		n, _ := strconv.ParseInt(t, 10, 64)
		return n
	}
	return 0
}
