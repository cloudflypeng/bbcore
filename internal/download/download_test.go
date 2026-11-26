package download

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type fakeUpstream struct {
	data     []byte
	chunk    int
	delay    time.Duration
	mu       sync.Mutex
	requests int
}

func (f *fakeUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests++
	f.mu.Unlock()

	start := int64(0)
	if header := r.Header.Get("Range"); header != "" {
		fmt.Sscanf(header, "bytes=%d-", &start)
	}
	if start >= int64(len(f.data)) {
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	if start > 0 {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(f.data)-1, len(f.data)))
		w.WriteHeader(http.StatusPartialContent)
	} else {
		w.WriteHeader(http.StatusOK)
	}
	chunk := f.chunk
	if chunk <= 0 {
		chunk = 4096
	}
	for position := start; position < int64(len(f.data)); position += int64(chunk) {
		end := position + int64(chunk)
		if end > int64(len(f.data)) {
			end = int64(len(f.data))
		}
		if _, err := w.Write(f.data[position:end]); err != nil {
			return
		}
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		if f.delay > 0 {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(f.delay):
			}
		}
	}
}

func testData(size int) []byte {
	data := make([]byte, size)
	for i := range data {
		data[i] = byte(i * 31 % 251)
	}
	return data
}

func testMedia(url string, size int64) *Media {
	return &Media{
		Video: Track{
			ID:        80,
			CodecID:   12,
			MimeType:  "video/mp4",
			URLs:      []string{url},
			Init:      []byte("video-init"),
			TotalSize: size,
		},
		MasterPlaylist: "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\nvideo.m3u8\n",
		VideoPlaylist:  "#EXTM3U\n#EXT-X-MAP:URI=\"video-init.mp4\"\n#EXTINF:1,\n#EXT-X-BYTERANGE:1@0\nvideo.mp4\n#EXT-X-ENDLIST\n",
	}
}

func newTestManager(t *testing.T, dir string, media *Media, events chan<- Event) *Manager {
	t.Helper()
	manager, err := New(Options{
		Dir:    dir,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Emit: func(topic string, data any) {
			if events != nil {
				select {
				case events <- Event{topic: topic, data: data}:
				default:
				}
			}
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	manager.SetResolver(func(ctx context.Context, job Job) (*Media, error) {
		return media, nil
	})
	t.Cleanup(manager.Close)
	return manager
}

type Event struct {
	topic string
	data  any
}

func waitFor(t *testing.T, manager *Manager, id, status string) Job {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		job, ok := manager.Get(id)
		if ok && job.Status == status {
			return *job
		}
		time.Sleep(5 * time.Millisecond)
	}
	job, _ := manager.Get(id)
	t.Fatalf("job %s did not reach %q: %+v", id, status, job)
	return Job{}
}

func TestManagerCompletesAndReloads(t *testing.T) {
	data := testData(300 * 1024)
	upstream := &fakeUpstream{data: data}
	server := httptest.NewServer(upstream)
	defer server.Close()

	dir := t.TempDir()
	events := make(chan Event, 64)
	manager := newTestManager(t, dir, testMedia(server.URL, int64(len(data))), events)
	job, err := manager.Start(Spec{BVID: "BV1xx", CID: 1, Title: "测试视频"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	done := waitFor(t, manager, job.ID, StatusCompleted)
	if done.DownloadedBytes != int64(len(data)) || done.TotalBytes != int64(len(data)) {
		t.Fatalf("progress = %d/%d", done.DownloadedBytes, done.TotalBytes)
	}

	video, err := os.ReadFile(filepath.Join(dir, job.ID, "video.mp4"))
	if err != nil {
		t.Fatalf("read video: %v", err)
	}
	if !bytes.Equal(video, data) {
		t.Fatalf("downloaded %d bytes, want %d", len(video), len(data))
	}
	if init, err := os.ReadFile(filepath.Join(dir, job.ID, "video-init.mp4")); err != nil || string(init) != "video-init" {
		t.Fatalf("video init = %q, err = %v", init, err)
	}
	if _, err := os.Stat(filepath.Join(dir, job.ID, "master.m3u8")); err != nil {
		t.Fatalf("master playlist: %v", err)
	}

	sawCompleted := false
	for {
		select {
		case event := <-events:
			if event.topic == TopicCompleted {
				sawCompleted = true
			}
			continue
		default:
		}
		break
	}
	if !sawCompleted {
		t.Fatal("no download.completed event")
	}

	manager.Close()
	reloaded, err := New(Options{
		Dir:    dir,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	defer reloaded.Close()
	jobs := reloaded.List()
	if len(jobs) != 1 || jobs[0].Status != StatusCompleted || jobs[0].ID != job.ID {
		t.Fatalf("reloaded jobs = %+v", jobs)
	}
}

func TestManagerPauseResume(t *testing.T) {
	data := testData(512 * 1024)
	upstream := &fakeUpstream{data: data, chunk: 8 * 1024, delay: 4 * time.Millisecond}
	server := httptest.NewServer(upstream)
	defer server.Close()

	dir := t.TempDir()
	media := testMedia(server.URL, int64(len(data)))
	manager := newTestManager(t, dir, media, nil)
	job, err := manager.Start(Spec{BVID: "BV1yy", CID: 2})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		current, ok := manager.Get(job.ID)
		if ok && current.DownloadedBytes > 0 && current.DownloadedBytes < current.TotalBytes {
			break
		}
		if !ok || current.Status == StatusCompleted {
			t.Fatalf("job finished before pause: %+v", current)
		}
		if time.Now().After(deadline) {
			t.Fatalf("no progress before pause: %+v", current)
		}
		time.Sleep(2 * time.Millisecond)
	}
	if _, err := manager.Pause(job.ID); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	paused := waitFor(t, manager, job.ID, StatusPaused)
	if paused.DownloadedBytes <= 0 || paused.DownloadedBytes >= paused.TotalBytes {
		t.Fatalf("paused progress = %d/%d", paused.DownloadedBytes, paused.TotalBytes)
	}
	partial := fileSize(filepath.Join(dir, job.ID, "video.mp4"))
	if partial <= 0 || partial >= int64(len(data)) {
		t.Fatalf("partial file size = %d", partial)
	}
	time.Sleep(50 * time.Millisecond)
	if again, _ := manager.Get(job.ID); again.Status != StatusPaused {
		t.Fatalf("job left paused state: %+v", again)
	}

	if _, err := manager.Resume(job.ID); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	waitFor(t, manager, job.ID, StatusCompleted)
	video, err := os.ReadFile(filepath.Join(dir, job.ID, "video.mp4"))
	if err != nil {
		t.Fatalf("read video: %v", err)
	}
	if !bytes.Equal(video, data) {
		t.Fatalf("resumed download is corrupt: got %d bytes", len(video))
	}
}

func TestManagerRemoveDeletesFiles(t *testing.T) {
	data := testData(64 * 1024)
	upstream := &fakeUpstream{data: data}
	server := httptest.NewServer(upstream)
	defer server.Close()

	dir := t.TempDir()
	manager := newTestManager(t, dir, testMedia(server.URL, int64(len(data))), nil)
	job, err := manager.Start(Spec{BVID: "BV1zz", CID: 3})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitFor(t, manager, job.ID, StatusCompleted)
	if err := manager.Remove(job.ID); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, job.ID)); !os.IsNotExist(err) {
		t.Fatalf("job dir still exists: %v", err)
	}
	if jobs := manager.List(); len(jobs) != 0 {
		t.Fatalf("jobs after remove = %+v", jobs)
	}
	if err := manager.Remove(job.ID); err != ErrNotFound {
		t.Fatalf("second remove = %v", err)
	}
}

func TestManagerStartIsIdempotent(t *testing.T) {
	data := testData(16 * 1024)
	upstream := &fakeUpstream{data: data}
	server := httptest.NewServer(upstream)
	defer server.Close()

	dir := t.TempDir()
	manager := newTestManager(t, dir, testMedia(server.URL, int64(len(data))), nil)
	first, err := manager.Start(Spec{BVID: "BV1aa", CID: 4})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	second, err := manager.Start(Spec{BVID: "BV1aa", CID: 4})
	if err != nil {
		t.Fatalf("second Start: %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("ids differ: %s vs %s", first.ID, second.ID)
	}
	waitFor(t, manager, first.ID, StatusCompleted)
}

func TestManagerFailedResolve(t *testing.T) {
	dir := t.TempDir()
	manager, err := New(Options{
		Dir:    dir,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer manager.Close()
	manager.SetResolver(func(ctx context.Context, job Job) (*Media, error) {
		return nil, fmt.Errorf("playurl denied")
	})
	job, err := manager.Start(Spec{BVID: "BV1bb", CID: 5})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	failed := waitFor(t, manager, job.ID, StatusFailed)
	if failed.Error == "" {
		t.Fatal("failed job has no error message")
	}
	if _, err := manager.Resume(job.ID); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	waitFor(t, manager, job.ID, StatusFailed)
}

func TestGateDynamicLimit(t *testing.T) {
	g := newGate(1)
	if err := g.acquire(context.Background()); err != nil {
		t.Fatalf("acquire: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if err := g.acquire(ctx); err == nil {
		t.Fatal("gate allowed more than one transfer")
	}

	g.setLimit(2)
	g.mu.Lock()
	active := g.active
	waiters := len(g.waiters)
	g.mu.Unlock()
	if active != 1 || waiters != 0 {
		t.Fatalf("cancelled waiter leaked: active=%d waiters=%d", active, waiters)
	}

	g.setLimit(1)
	done := make(chan error, 1)
	go func() { done <- g.acquire(context.Background()) }()

	deadline := time.Now().Add(time.Second)
	for {
		g.mu.Lock()
		waiting := len(g.waiters)
		g.mu.Unlock()
		if waiting == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("waiter was not registered")
		}
		time.Sleep(time.Millisecond)
	}

	g.setLimit(3)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("promoted acquire: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("raising the limit did not wake the waiter")
	}

	g.release()
	g.release()
	g.mu.Lock()
	active = g.active
	g.mu.Unlock()
	if active != 0 {
		t.Fatalf("active = %d after releasing everything", active)
	}

	if g.limitValue() != 3 {
		t.Fatalf("limit = %d, want 3", g.limitValue())
	}
	g.setLimit(0)
	if g.limitValue() != 1 {
		t.Fatalf("limit = %d, want clamped 1", g.limitValue())
	}
}

func TestThrottleRateValue(t *testing.T) {
	th := &throttle{}
	th.setRate(4096)
	if got := th.rateValue(); got != 4096 {
		t.Fatalf("rate = %d, want 4096", got)
	}
	th.setRate(0)
	if got := th.rateValue(); got != 0 {
		t.Fatalf("rate = %d, want 0", got)
	}
}
