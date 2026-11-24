package jsonrpc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestParseParamsObject(t *testing.T) {
	p, rpcErr := ParseParams(json.RawMessage(`{"mid":123456789012345678,"name":"x","flag":true}`))
	if rpcErr != nil {
		t.Fatalf("ParseParams: %v", rpcErr)
	}
	mid, ok := p.ID("mid")
	if !ok || mid != 123456789012345678 {
		t.Fatalf("mid = %d, %v", mid, ok)
	}
	if p.String("name") != "x" || !p.Bool("flag", false) {
		t.Fatalf("unexpected values: %+v", p)
	}
}

func TestParseParamsArrayAndEmpty(t *testing.T) {
	p, rpcErr := ParseParams(json.RawMessage(`[{"a":1}]`))
	if rpcErr != nil || p.IntDefault("a", 0) != 1 {
		t.Fatalf("array params: %v %+v", rpcErr, p)
	}
	p, rpcErr = ParseParams(nil)
	if rpcErr != nil || len(p) != 0 {
		t.Fatalf("empty params: %v %+v", rpcErr, p)
	}
	if _, rpcErr := ParseParams(json.RawMessage(`"nope"`)); rpcErr == nil {
		t.Fatal("expected invalid params error")
	}
}

func TestParamsQuery(t *testing.T) {
	p, _ := ParseParams(json.RawMessage(`{"query":{"a":["1","2"],"b":{"x":1},"c":3}}`))
	q := p.Query("query")
	if got := strings.Join(q["a"], ","); got != "1,2" {
		t.Errorf("a = %q", got)
	}
	if got := q.Get("b"); got != `{"x":1}` {
		t.Errorf("b = %q", got)
	}
	if got := q.Get("c"); got != "3" {
		t.Errorf("c = %q", got)
	}
}

func runServer(t *testing.T, input string) []string {
	t.Helper()
	out := &bytes.Buffer{}
	server := NewServer(out, nil, 2)
	server.Register("echo", func(ctx context.Context, params json.RawMessage) (any, *Error) {
		return json.RawMessage(params), nil
	})
	server.Register("fail", func(ctx context.Context, params json.RawMessage) (any, *Error) {
		return nil, Errorf(CodeInvalidParams, "bad input")
	})
	if err := server.Serve(context.Background(), strings.NewReader(input)); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil
	}
	return lines
}

func TestServerNDJSON(t *testing.T) {
	lines := runServer(t, `{"jsonrpc":"2.0","id":1,"method":"echo","params":{"a":1}}
{"jsonrpc":"2.0","method":"echo","params":{"a":2}}
{"jsonrpc":"2.0","id":2,"method":"fail"}
{"jsonrpc":"2.0","id":3,"method":"missing"}
`)
	if len(lines) != 3 {
		t.Fatalf("got %d responses: %v", len(lines), lines)
	}
	seen := map[string]bool{}
	for _, line := range lines {
		var resp Response
		if err := json.Unmarshal([]byte(line), &resp); err != nil {
			t.Fatalf("bad response %q: %v", line, err)
		}
		if resp.Error != nil {
			seen[fmt.Sprintf("%s:err%d", resp.ID, resp.Error.Code)] = true
			continue
		}
		seen[string(resp.ID)] = true
	}
	if !seen["1"] || !seen["2:err-32602"] || !seen["3:err-32601"] {
		t.Fatalf("unexpected responses: %v", seen)
	}
}

func TestServerContextCancellation(t *testing.T) {
	out := &bytes.Buffer{}
	server := NewServer(out, nil, 2)
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer pw.Close()
	defer pr.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx, pr) }()
	cancel()
	select {
	case serveErr := <-done:
		if serveErr != nil {
			t.Fatalf("Serve: %v", serveErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after context cancellation")
	}
}

func TestServerContentLengthFraming(t *testing.T) {
	body := `{"jsonrpc":"2.0","id":7,"method":"echo","params":{"k":"v"}}`
	input := fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(body), body)
	lines := runServer(t, input)
	if len(lines) != 1 {
		t.Fatalf("got %d responses: %v", len(lines), lines)
	}
	var resp Response
	if err := json.Unmarshal([]byte(lines[0]), &resp); err != nil {
		t.Fatalf("bad response: %v", err)
	}
	if string(resp.ID) != "7" || resp.Error != nil {
		t.Fatalf("unexpected response: %+v", resp)
	}
}
