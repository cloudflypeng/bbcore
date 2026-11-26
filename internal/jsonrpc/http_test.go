package jsonrpc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cloudflypeng/bbcore/internal/events"
)

func newHTTPTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := NewServer(io.Discard, nil, 2)
	server.Register("echo", func(ctx context.Context, params json.RawMessage) (any, *Error) {
		return json.RawMessage(params), nil
	})
	ts := httptest.NewServer(server)
	t.Cleanup(ts.Close)
	return ts
}

func postRPC(t *testing.T, url, body string) (*http.Response, []byte) {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp, data
}

func TestServeHTTP(t *testing.T) {
	ts := newHTTPTestServer(t)
	resp, body := postRPC(t, ts.URL, `{"jsonrpc":"2.0","id":7,"method":"echo","params":{"a":1}}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	var out struct {
		ID     int             `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  *Error          `json:"error"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode response %q: %v", body, err)
	}
	if out.Error != nil || out.ID != 7 {
		t.Fatalf("response = %+v", out)
	}
	var params map[string]int
	if err := json.Unmarshal(out.Result, &params); err != nil || params["a"] != 1 {
		t.Fatalf("result = %s, %v", out.Result, err)
	}
}

func TestServeHTTPErrors(t *testing.T) {
	ts := newHTTPTestServer(t)

	_, body := postRPC(t, ts.URL, `{"jsonrpc":"2.0","id":8,"method":"missing"}`)
	var unknown struct {
		Error *Error `json:"error"`
	}
	if err := json.Unmarshal(body, &unknown); err != nil || unknown.Error == nil || unknown.Error.Code != CodeMethodNotFound {
		t.Fatalf("unknown method response = %s, %v", body, err)
	}

	_, body = postRPC(t, ts.URL, `{bad json`)
	var parse struct {
		Error *Error `json:"error"`
	}
	if err := json.Unmarshal(body, &parse); err != nil || parse.Error == nil || parse.Error.Code != CodeParseError {
		t.Fatalf("parse error response = %s, %v", body, err)
	}

	resp, body := postRPC(t, ts.URL, `{"jsonrpc":"2.0","method":"echo","params":{"a":2}}`)
	if resp.StatusCode != http.StatusNoContent || len(bytes.TrimSpace(body)) != 0 {
		t.Fatalf("notification status = %d, body = %q", resp.StatusCode, body)
	}
}

func TestServeHTTPRejectsGet(t *testing.T) {
	ts := newHTTPTestServer(t)
	resp, err := http.Get(ts.URL)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestServeHTTPEvents(t *testing.T) {
	hub := events.New(events.Options{Buffer: 8, History: 8})
	server := NewServer(io.Discard, nil, 2)
	server.SetEventHub(hub)
	ts := httptest.NewServer(server)
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/events?topics=live.*", nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /events: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Fatalf("content type = %q", got)
	}

	hub.Publish("live.message", map[string]any{"text": "hello"})
	hub.Publish("account.login", map[string]any{"mid": 1})

	reader := bufio.NewReader(resp.Body)
	var data string
	for i := 0; i < 8; i++ {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read SSE: %v", err)
		}
		if strings.HasPrefix(line, "data: ") {
			data = strings.TrimSpace(strings.TrimPrefix(line, "data: "))
			break
		}
	}
	if data == "" {
		t.Fatal("no data line")
	}
	var notification Notification
	if err := json.Unmarshal([]byte(data), &notification); err != nil {
		t.Fatalf("decode notification %q: %v", data, err)
	}
	if notification.Method != "event.live.message" {
		t.Fatalf("method = %q", notification.Method)
	}
	params, ok := notification.Params.(map[string]any)
	if !ok || params["seq"] != float64(1) {
		t.Fatalf("params = %#v", notification.Params)
	}
}

func TestServeHTTPToken(t *testing.T) {
	server := NewServer(io.Discard, nil, 2)
	server.SetToken("test-token")
	server.SetEventHub(events.New(events.Options{}))
	ts := httptest.NewServer(server)
	defer ts.Close()

	resp, body := postRPC(t, ts.URL, `{"jsonrpc":"2.0","id":1,"method":"missing"}`)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}

	req, err := http.NewRequest(http.MethodPost, ts.URL, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"missing"}`))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("authorized POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("authorized status = %d", resp.StatusCode)
	}
}
