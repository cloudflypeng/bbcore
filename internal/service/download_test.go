package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/cloudflypeng/bbcore/internal/download"
)

func TestDownloadMethodsRegistered(t *testing.T) {
	s := New(Options{})
	for _, name := range []string{
		"download.start", "download.list", "download.status",
		"download.pause", "download.resume", "download.remove", "download.url",
		"download.config",
	} {
		if _, ok := s.methods[name]; !ok {
			t.Errorf("method %s is not registered", name)
		}
	}
}

func TestDownloadStartRequiresBVID(t *testing.T) {
	s := New(Options{DataDir: t.TempDir()})
	if _, rpcErr := s.downloadStart(context.Background(), json.RawMessage(`{}`)); rpcErr == nil {
		t.Fatal("download.start accepted empty params")
	}
}

func TestDownloadStatusRequiresID(t *testing.T) {
	s := New(Options{DataDir: t.TempDir()})
	if _, rpcErr := s.downloadStatus(context.Background(), json.RawMessage(`{}`)); rpcErr == nil {
		t.Fatal("download.status accepted empty params")
	}
}

func TestDownloadStartRequiresDataDir(t *testing.T) {
	s := New(Options{})
	if _, rpcErr := s.downloadStart(context.Background(), json.RawMessage(`{"bvid":"BV1xx","cid":1}`)); rpcErr == nil {
		t.Fatal("download.start started without a data directory")
	}
}

func TestDownloadListEmpty(t *testing.T) {
	s := New(Options{DataDir: t.TempDir()})
	defer s.Stop()
	out, rpcErr := s.downloadList(context.Background(), nil)
	if rpcErr != nil {
		t.Fatalf("download.list: %v", rpcErr)
	}
	payload, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("unexpected payload %T", out)
	}
	items, ok := payload["items"].([]downloadJobView)
	if !ok || len(items) != 0 {
		t.Fatalf("items = %#v", payload["items"])
	}
	summary, ok := payload["summary"].(map[string]int)
	if !ok || summary["total"] != 0 {
		t.Fatalf("summary = %#v", payload["summary"])
	}
}

func TestDownloadViewMasterURL(t *testing.T) {
	s := New(Options{DataDir: t.TempDir()})
	defer s.Stop()
	view := s.downloadView(download.Job{ID: "0123456789abcdef", Status: download.StatusCompleted})
	want := "/d/0123456789abcdef/master.m3u8"
	if !strings.HasSuffix(view.MasterURL, want) {
		t.Fatalf("master URL = %q, want suffix %q", view.MasterURL, want)
	}
	paused := s.downloadView(download.Job{ID: "0123456789abcdef", Status: download.StatusPaused})
	if paused.MasterURL != "" {
		t.Fatalf("paused job has a master URL: %q", paused.MasterURL)
	}
}

func TestDownloadConfigUpdatesManager(t *testing.T) {
	s := New(Options{DataDir: t.TempDir()})
	defer s.Stop()
	out, rpcErr := s.downloadConfig(context.Background(), json.RawMessage(`{"concurrency":3,"limit_kbps":512}`))
	if rpcErr != nil {
		t.Fatalf("download.config: %v", rpcErr)
	}
	payload, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("unexpected payload %T", out)
	}
	if payload["concurrency"] != 3 {
		t.Fatalf("concurrency = %v, want 3", payload["concurrency"])
	}
	if payload["limit_kbps"] != int64(512) {
		t.Fatalf("limit_kbps = %v, want 512", payload["limit_kbps"])
	}
	if dir, _ := payload["download_dir"].(string); !strings.HasSuffix(dir, "downloads") {
		t.Fatalf("download_dir = %q", dir)
	}

	out, rpcErr = s.downloadConfig(context.Background(), json.RawMessage(`{"concurrency":99,"limit_kbps":0}`))
	if rpcErr != nil {
		t.Fatalf("download.config clamp: %v", rpcErr)
	}
	payload = out.(map[string]any)
	if payload["concurrency"] != 8 {
		t.Fatalf("concurrency = %v, want clamped 8", payload["concurrency"])
	}
	if payload["limit_kbps"] != int64(0) {
		t.Fatalf("limit_kbps = %v, want 0", payload["limit_kbps"])
	}
}

func TestDownloadConfigRequiresDataDir(t *testing.T) {
	s := New(Options{})
	if _, rpcErr := s.downloadConfig(context.Background(), json.RawMessage(`{}`)); rpcErr == nil {
		t.Fatal("download.config worked without a data directory")
	}
}

func TestTrackTotalSize(t *testing.T) {
	segments := []hlsSegment{
		{offset: 100, size: 50},
		{offset: 150, size: 25},
	}
	if got := trackTotalSize(segments, 40, 99); got != 175 {
		t.Fatalf("trackTotalSize = %d, want 175", got)
	}
	if got := trackTotalSize(nil, 9); got != 10 {
		t.Fatalf("trackTotalSize(nil) = %d, want 10", got)
	}
}
