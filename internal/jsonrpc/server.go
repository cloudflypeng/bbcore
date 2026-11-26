// Package jsonrpc implements the stdio JSON-RPC 2.0 transport used to embed
// bbcore in a host process. Messages are newline-delimited JSON; LSP-style
// Content-Length framing is also accepted on input.
package jsonrpc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/cloudflypeng/bbcore/internal/events"
)

// Standard JSON-RPC 2.0 error codes.
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
)

// Error is a JSON-RPC error object.
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (e *Error) Error() string { return fmt.Sprintf("%d: %s", e.Code, e.Message) }

// Errorf builds an Error.
func Errorf(code int, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// Request is an incoming JSON-RPC message.
type Request struct {
	JSONRPC string          `json:"jsonrpc,omitempty"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Response is an outgoing JSON-RPC response.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// Notification is a server-initiated message without an id.
type Notification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// Handler executes one RPC method.
type Handler func(ctx context.Context, params json.RawMessage) (any, *Error)

// Server reads requests from an io.Reader and writes responses to an io.Writer.
// All writes are serialized so notifications may be emitted from any goroutine.
type Server struct {
	mu       sync.Mutex
	out      io.Writer
	logOut   io.Writer
	handlers map[string]Handler
	sem      chan struct{}
	wg       sync.WaitGroup

	hub      *events.Hub
	token    string
	pumpOnce sync.Once
}

// NewServer creates a server writing protocol messages to out and human logs to
// logOut. Up to maxConcurrent requests execute at once (0 = 32).
func NewServer(out, logOut io.Writer, maxConcurrent int) *Server {
	if maxConcurrent <= 0 {
		maxConcurrent = 32
	}
	return &Server{
		out:      out,
		logOut:   logOut,
		handlers: make(map[string]Handler),
		sem:      make(chan struct{}, maxConcurrent),
	}
}

// SetEventHub attaches the event hub used by stdio notifications and the HTTP
// SSE endpoint. It must be called before Serve/ServeHTTP starts.
func (s *Server) SetEventHub(hub *events.Hub) {
	s.hub = hub
}

// SetToken optionally protects the HTTP transport with a bearer token. The
// empty token keeps the development transport unauthenticated.
func (s *Server) SetToken(token string) {
	s.token = token
}

// Register adds a method handler.
func (s *Server) Register(method string, h Handler) {
	s.handlers[method] = h
}

// Call invokes one registered method synchronously. It is used by embedded
// transports that do not speak the stdio or HTTP framing.
func (s *Server) Call(ctx context.Context, method string, params json.RawMessage) (result any, rpcErr *Error) {
	h, ok := s.handlers[method]
	if !ok {
		return nil, Errorf(CodeMethodNotFound, "method not found: %s", method)
	}
	s.sem <- struct{}{}
	defer func() { <-s.sem }()
	defer func() {
		if rec := recover(); rec != nil {
			result = nil
			rpcErr = Errorf(CodeInternalError, "panic: %v", rec)
		}
	}()
	return h(ctx, params)
}

// Registered returns the sorted registered method names.
func (s *Server) Registered() []string {
	names := make([]string, 0, len(s.handlers))
	for name := range s.handlers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// WriteNotification pushes a server-initiated notification to the host.
func (s *Server) WriteNotification(method string, params any) {
	n := Notification{JSONRPC: "2.0", Method: method, Params: params}
	s.writeJSON(n)
}

func (s *Server) startEventPump(ctx context.Context) {
	if s.hub == nil {
		return
	}
	s.pumpOnce.Do(func() {
		ch, cancel := s.hub.Subscribe(nil, 0)
		go func() {
			defer cancel()
			for {
				select {
				case <-ctx.Done():
					return
				case ev, ok := <-ch:
					if !ok {
						return
					}
					s.WriteNotification("event."+ev.Topic, map[string]any{
						"seq":  ev.Seq,
						"ts":   ev.TS,
						"data": ev.Data,
					})
				}
			}
		}()
	})
}

func (s *Server) writeJSON(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		b, _ = json.Marshal(Notification{
			JSONRPC: "2.0",
			Method:  "event.internal_error",
			Params:  map[string]any{"error": err.Error()},
		})
	}
	b = append(b, '\n')
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _ = s.out.Write(b)
}

// Logf writes a human-readable line to the log stream (stderr).
func (s *Server) Logf(format string, args ...any) {
	if s.logOut == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	fmt.Fprintf(s.logOut, format+"\n", args...)
}

// Serve blocks reading messages until EOF or ctx cancellation.
//
// Reads happen in a separate goroutine because closing an os.File does not
// reliably interrupt a blocking read on stdio; ctx cancellation must be able
// to stop Serve on its own.
func (s *Server) Serve(ctx context.Context, r io.Reader) error {
	s.startEventPump(ctx)

	type message struct {
		payload []byte
		err     error
	}
	msgs := make(chan message)
	go func() {
		reader := bufio.NewReaderSize(r, 1<<20)
		for {
			payload, err := readMessage(reader)
			select {
			case msgs <- message{payload: payload, err: err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	for {
		select {
		case <-ctx.Done():
			s.wg.Wait()
			return nil
		case msg := <-msgs:
			if msg.err != nil {
				if errors.Is(msg.err, io.EOF) || errors.Is(msg.err, io.ErrUnexpectedEOF) || ctx.Err() != nil {
					s.wg.Wait()
					return nil
				}
				s.writeJSON(Response{
					JSONRPC: "2.0",
					Error:   Errorf(CodeParseError, "read error: %v", msg.err),
				})
				return msg.err
			}
			if len(msg.payload) == 0 {
				continue
			}
			s.handlePayload(ctx, msg.payload)
		}
	}
}

func (s *Server) handlePayload(ctx context.Context, payload []byte) {
	trimmed := bytes.TrimSpace(payload)
	if len(trimmed) == 0 {
		return
	}
	if trimmed[0] == '[' {
		var batch []Request
		if err := json.Unmarshal(trimmed, &batch); err != nil {
			s.writeJSON(Response{JSONRPC: "2.0", Error: Errorf(CodeParseError, "%v", err)})
			return
		}
		for i := range batch {
			s.dispatch(ctx, batch[i])
		}
		return
	}
	var req Request
	if err := json.Unmarshal(trimmed, &req); err != nil {
		s.writeJSON(Response{JSONRPC: "2.0", Error: Errorf(CodeParseError, "%v", err)})
		return
	}
	s.dispatch(ctx, req)
}

func (s *Server) dispatch(ctx context.Context, req Request) {
	if req.Method == "" {
		s.writeJSON(Response{JSONRPC: "2.0", ID: req.ID, Error: Errorf(CodeInvalidRequest, "missing method")})
		return
	}
	h, ok := s.handlers[req.Method]
	if !ok {
		if len(req.ID) > 0 {
			s.writeJSON(Response{JSONRPC: "2.0", ID: req.ID, Error: Errorf(CodeMethodNotFound, "method not found: %s", req.Method)})
		}
		return
	}
	isNotification := len(req.ID) == 0
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.sem <- struct{}{}
		defer func() { <-s.sem }()
		defer func() {
			if rec := recover(); rec != nil {
				if !isNotification {
					s.writeJSON(Response{JSONRPC: "2.0", ID: req.ID, Error: Errorf(CodeInternalError, "panic: %v", rec)})
				}
			}
		}()
		result, rpcErr := h(ctx, req.Params)
		if isNotification {
			return
		}
		if rpcErr != nil {
			s.writeJSON(Response{JSONRPC: "2.0", ID: req.ID, Error: rpcErr})
			return
		}
		s.writeJSON(Response{JSONRPC: "2.0", ID: req.ID, Result: result})
	}()
}

// readMessage reads one framed message. Accepts NDJSON and Content-Length
// framing (headers terminated by a blank line, body of N bytes).
func readMessage(r *bufio.Reader) ([]byte, error) {
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			if len(line) > 0 {
				return []byte(strings.TrimSpace(line)), nil
			}
			return nil, err
		}
		if len(strings.TrimSpace(line)) == 0 {
			continue
		}
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(line)), "content-length:") {
			length, convErr := strconv.Atoi(strings.TrimSpace(strings.SplitN(line, ":", 2)[1]))
			if convErr != nil || length < 0 {
				return nil, fmt.Errorf("invalid content-length: %q", line)
			}
			// Consume remaining headers until blank line.
			for {
				h, hErr := r.ReadString('\n')
				if hErr != nil {
					return nil, hErr
				}
				if strings.TrimSpace(h) == "" {
					break
				}
			}
			buf := make([]byte, length)
			if _, err := io.ReadFull(r, buf); err != nil {
				return nil, err
			}
			return buf, nil
		}
		return []byte(strings.TrimSpace(line)), nil
	}
}
