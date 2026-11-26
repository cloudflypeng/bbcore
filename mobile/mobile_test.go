package mobile

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

type recordingHandler struct {
	events chan string
}

func (h *recordingHandler) OnEvent(payload string) {
	select {
	case h.events <- payload:
	default:
	}
}

func TestStartCallStop(t *testing.T) {
	dir := t.TempDir()
	if err := Start(dir); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer Stop()

	raw, err := Call("core.capabilities", "")
	if err != nil {
		t.Fatalf("core.capabilities: %v", err)
	}
	var caps map[string]any
	if err := json.Unmarshal([]byte(raw), &caps); err != nil {
		t.Fatalf("decode capabilities: %v", err)
	}
	if caps["version"] == nil || caps["version"] == "" {
		t.Fatalf("capabilities missing version: %s", raw)
	}
	if caps["methods"] == nil {
		t.Fatalf("capabilities missing methods: %s", raw)
	}

	if _, err := Call("does.not.exist", "{}"); err == nil {
		t.Fatal("unknown method succeeded")
	} else if !strings.HasPrefix(err.Error(), "-32601") {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := Call("core.capabilities", "{not json"); err == nil {
		t.Fatal("invalid params JSON succeeded")
	}

	Stop()
	if _, err := Call("core.capabilities", ""); err == nil {
		t.Fatal("call succeeded after Stop")
	}
}

func TestStartRequiresDirectory(t *testing.T) {
	if err := Start("   "); err == nil {
		Stop()
		t.Fatal("Start accepted an empty data directory")
	}
}

func TestStartRejectsDoubleStart(t *testing.T) {
	dir := t.TempDir()
	if err := Start(dir); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer Stop()
	if err := Start(dir); err == nil {
		t.Fatal("second Start succeeded")
	}
}

func TestEventHandlerReceivesNotifications(t *testing.T) {
	handler := &recordingHandler{events: make(chan string, 4)}
	SetEventHandler(handler)
	defer SetEventHandler(nil)

	if err := Start(t.TempDir()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer Stop()

	mu.Lock()
	hub := running.hub
	mu.Unlock()
	if hub == nil {
		t.Fatal("runtime has no hub")
	}
	hub.Publish("test.notice", map[string]any{"value": 7})

	select {
	case payload := <-handler.events:
		var notification struct {
			Method string `json:"method"`
			Params struct {
				Data map[string]any `json:"data"`
			} `json:"params"`
		}
		if err := json.Unmarshal([]byte(payload), &notification); err != nil {
			t.Fatalf("decode notification: %v", err)
		}
		if notification.Method != "event.test.notice" {
			t.Fatalf("method = %q", notification.Method)
		}
		if notification.Params.Data["value"] != float64(7) {
			t.Fatalf("data = %#v", notification.Params.Data)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no event delivered")
	}
}
