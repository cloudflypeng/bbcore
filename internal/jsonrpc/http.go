package jsonrpc

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const maxHTTPBody = 16 << 20

// ServeHTTP executes one JSON-RPC request carried in the POST body and writes
// the response as JSON. GET /events exposes the same event stream as SSE for
// hosts that cannot spawn a sidecar process (for example iOS).
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(w, r) {
		return
	}
	if r.URL.Path == "/events" {
		s.serveEvents(w, r)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxHTTPBody))
	if err != nil {
		writeHTTPResponse(w, Response{JSONRPC: "2.0", Error: Errorf(CodeParseError, "read body: %v", err)})
		return
	}

	var req Request
	if err := json.Unmarshal(body, &req); err != nil {
		writeHTTPResponse(w, Response{JSONRPC: "2.0", Error: Errorf(CodeParseError, "%v", err)})
		return
	}
	if req.Method == "" {
		writeHTTPResponse(w, Response{JSONRPC: "2.0", ID: req.ID, Error: Errorf(CodeInvalidRequest, "missing method")})
		return
	}
	h, ok := s.handlers[req.Method]
	if !ok {
		writeHTTPResponse(w, Response{JSONRPC: "2.0", ID: req.ID, Error: Errorf(CodeMethodNotFound, "method not found: %s", req.Method)})
		return
	}
	if len(req.ID) == 0 {
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			h(r.Context(), req.Params)
		}()
		w.WriteHeader(http.StatusNoContent)
		return
	}

	s.sem <- struct{}{}
	defer func() { <-s.sem }()
	result, rpcErr := s.execute(r.Context(), h, req.Params)
	if rpcErr != nil {
		writeHTTPResponse(w, Response{JSONRPC: "2.0", ID: req.ID, Error: rpcErr})
		return
	}
	writeHTTPResponse(w, Response{JSONRPC: "2.0", ID: req.ID, Result: result})
}

func (s *Server) execute(ctx context.Context, h Handler, params json.RawMessage) (result any, rpcErr *Error) {
	defer func() {
		if rec := recover(); rec != nil {
			result = nil
			rpcErr = Errorf(CodeInternalError, "panic: %v", rec)
		}
	}()
	return h(ctx, params)
}

func writeHTTPResponse(w http.ResponseWriter, resp Response) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) authorized(w http.ResponseWriter, r *http.Request) bool {
	if s.token == "" {
		return true
	}
	got := r.Header.Get("Authorization")
	want := "Bearer " + s.token
	if subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1 {
		return true
	}
	w.Header().Set("WWW-Authenticate", `Bearer realm="bbcore"`)
	http.Error(w, "unauthorized", http.StatusUnauthorized)
	return false
}

func (s *Server) serveEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.hub == nil {
		http.Error(w, "events are not available", http.StatusServiceUnavailable)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming is not supported", http.StatusInternalServerError)
		return
	}

	var topics []string
	if raw := strings.TrimSpace(r.URL.Query().Get("topics")); raw != "" {
		topics = []string{raw}
	}

	ch, cancel := s.hub.Subscribe(topics, eventSince(r))
	defer cancel()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			payload, err := json.Marshal(Notification{
				JSONRPC: "2.0",
				Method:  "event." + ev.Topic,
				Params: map[string]any{
					"seq":  ev.Seq,
					"ts":   ev.TS,
					"data": ev.Data,
				},
			})
			if err != nil {
				continue
			}
			if _, err := fmt.Fprintf(w, "id: %d\ndata: %s\n\n", ev.Seq, payload); err != nil {
				return
			}
			flusher.Flush()
		case <-ticker.C:
			if _, err := io.WriteString(w, ": heartbeat\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func eventSince(r *http.Request) uint64 {
	raw := strings.TrimSpace(r.Header.Get("Last-Event-ID"))
	if raw == "" {
		raw = strings.TrimSpace(r.URL.Query().Get("since"))
	}
	since, _ := strconv.ParseUint(raw, 10, 64)
	return since
}
