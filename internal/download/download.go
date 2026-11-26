// Package download manages offline video downloads. A job resolves a DASH
// stream once, stores the fMP4 files and generated HLS playlists under its own
// directory, and can pause, resume and remove itself without losing progress.
package download

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Job statuses.
const (
	StatusQueued      = "queued"
	StatusDownloading = "downloading"
	StatusPaused      = "paused"
	StatusCompleted   = "completed"
	StatusFailed      = "failed"
)

// Event topics published through Options.Emit.
const (
	TopicProgress  = "download.progress"
	TopicCompleted = "download.completed"
	TopicFailed    = "download.failed"
)

var (
	ErrNotFound = errors.New("download not found")
	ErrClosed   = errors.New("download manager closed")
	ErrBusy     = errors.New("download is still running")
)

const (
	chunkSize         = 64 * 1024
	progressInterval  = time.Second
	persistInterval   = 3 * time.Second
	fetchAttempts     = 3
	shutdownWait      = 5 * time.Second
	upstreamUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Safari/605.1.15"
)

// Job is the persisted, user-facing state of one download.
type Job struct {
	ID              string `json:"id"`
	BVID            string `json:"bvid"`
	CID             int64  `json:"cid"`
	Title           string `json:"title"`
	Cover           string `json:"cover"`
	Owner           string `json:"owner"`
	QN              int    `json:"qn"`
	Codec           string `json:"codec"`
	AudioID         int    `json:"audio_id"`
	DurationMS      int64  `json:"duration_ms"`
	Status          string `json:"status"`
	Error           string `json:"error,omitempty"`
	DownloadedBytes int64  `json:"downloaded_bytes"`
	TotalBytes      int64  `json:"total_bytes"`
	VideoID         int    `json:"video_id,omitempty"`
	VideoCodecID    int    `json:"video_codec_id,omitempty"`
	VideoBytes      int64  `json:"video_bytes,omitempty"`
	AudioStreamID   int    `json:"audio_stream_id,omitempty"`
	AudioBytes      int64  `json:"audio_bytes,omitempty"`
	HasAudio        bool   `json:"has_audio"`
	CreatedAt       int64  `json:"created_at"`
	UpdatedAt       int64  `json:"updated_at"`
	FinishedAt      int64  `json:"finished_at,omitempty"`
}

// Spec starts a new job. Metadata is supplied by the caller so the list renders
// before resolution finishes.
type Spec struct {
	BVID       string
	CID        int64
	Title      string
	Cover      string
	Owner      string
	QN         int
	Codec      string
	AudioID    int
	DurationMS int64
}

// Track is one resolved byte-range stream.
type Track struct {
	ID        int
	CodecID   int
	MimeType  string
	URLs      []string
	Init      []byte
	TotalSize int64
}

// Media is the resolver output: local files plus playlists that reference them.
type Media struct {
	Video          Track
	Audio          *Track
	MasterPlaylist string
	VideoPlaylist  string
	AudioPlaylist  string
}

// Resolver refreshes upstream URLs and playlists for an existing job.
type Resolver func(ctx context.Context, job Job) (*Media, error)

// Options configures a Manager.
type Options struct {
	Dir              string
	Logger           *slog.Logger
	Emit             func(topic string, data any)
	Concurrency      int
	LimitBytesPerSec int64
}

// Manager owns the download queue and its on-disk state.
type Manager struct {
	dir      string
	logger   *slog.Logger
	emit     func(string, any)
	client   *http.Client
	throttle *throttle

	mu       sync.Mutex
	jobs     map[string]*jobState
	resolver Resolver
	closed   bool
	gate     *gate

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

type jobState struct {
	job             Job
	cancel          context.CancelFunc
	done            chan struct{}
	running         bool
	removed         bool
	resumeRequested bool

	lastEmit    time.Time
	lastPersist time.Time
}

// New creates a manager rooted at opts.Dir and loads any persisted jobs.
func New(opts Options) (*Manager, error) {
	dir := strings.TrimSpace(opts.Dir)
	if dir == "" {
		return nil, fmt.Errorf("download dir is required")
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Concurrency <= 0 {
		opts.Concurrency = 1
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create download dir: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          16,
		MaxIdleConnsPerHost:   8,
		IdleConnTimeout:       90 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	}
	m := &Manager{
		dir:      dir,
		logger:   opts.Logger,
		emit:     opts.Emit,
		client:   &http.Client{Transport: transport},
		throttle: &throttle{rate: opts.LimitBytesPerSec},
		jobs:     map[string]*jobState{},
		gate:     newGate(opts.Concurrency),
		ctx:      ctx,
		cancel:   cancel,
	}
	if err := m.load(); err != nil {
		m.logger.Warn("load download jobs", "error", err)
	}
	return m, nil
}

// SetResolver installs the stream resolver used by every job.
func (m *Manager) SetResolver(resolver Resolver) {
	m.mu.Lock()
	m.resolver = resolver
	m.mu.Unlock()
}

// SetLimit updates the shared download rate limit in bytes per second.
// Zero disables throttling.
func (m *Manager) SetLimit(bytesPerSec int64) {
	m.throttle.setRate(bytesPerSec)
}

// SetConcurrency updates how many transfers may run at once. The value is
// clamped to 1..8.
func (m *Manager) SetConcurrency(n int) {
	m.gate.setLimit(clampConcurrency(n))
}

// Config returns the current concurrency and rate limit in bytes per second.
func (m *Manager) Config() (int, int64) {
	return m.gate.limitValue(), m.throttle.rateValue()
}

// Start queues a download. Repeating the same request returns the existing job.
func (m *Manager) Start(spec Spec) (*Job, error) {
	spec.BVID = strings.TrimSpace(spec.BVID)
	if spec.BVID == "" {
		return nil, fmt.Errorf("bvid is required")
	}
	if spec.CID <= 0 {
		return nil, fmt.Errorf("cid is required")
	}
	if spec.QN <= 0 {
		spec.QN = 80
	}
	if spec.Codec == "" {
		spec.Codec = "hevc"
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, ErrClosed
	}
	for _, st := range m.jobs {
		if st.removed {
			continue
		}
		job := st.job
		if job.BVID == spec.BVID && job.CID == spec.CID && job.QN == spec.QN &&
			job.Codec == spec.Codec && job.Status != StatusFailed {
			return &job, nil
		}
	}
	id, err := newID()
	if err != nil {
		return nil, err
	}
	now := nowMs()
	title := strings.TrimSpace(spec.Title)
	if title == "" {
		title = spec.BVID
	}
	st := &jobState{
		job: Job{
			ID:         id,
			BVID:       spec.BVID,
			CID:        spec.CID,
			Title:      title,
			Cover:      spec.Cover,
			Owner:      spec.Owner,
			QN:         spec.QN,
			Codec:      spec.Codec,
			AudioID:    spec.AudioID,
			DurationMS: spec.DurationMS,
			Status:     StatusQueued,
			CreatedAt:  now,
			UpdatedAt:  now,
		},
		done:    make(chan struct{}),
		running: true,
	}
	m.jobs[id] = st
	m.persistLocked()
	job := st.job
	m.wg.Add(1)
	go m.run(st)
	return &job, nil
}

// Get returns a snapshot of one job.
func (m *Manager) Get(id string) (*Job, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.jobs[id]
	if st == nil || st.removed {
		return nil, false
	}
	job := st.job
	return &job, true
}

// List returns jobs newest first.
func (m *Manager) List() []Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.listLocked()
}

// Pause stops an active job and keeps its partial files.
func (m *Manager) Pause(id string) (*Job, error) {
	m.mu.Lock()
	st := m.jobs[id]
	if st == nil || st.removed {
		m.mu.Unlock()
		return nil, ErrNotFound
	}
	switch st.job.Status {
	case StatusQueued, StatusDownloading:
		st.job.Status = StatusPaused
		st.job.UpdatedAt = nowMs()
		if st.cancel != nil {
			st.cancel()
		}
		m.persistLocked()
	}
	job := st.job
	m.mu.Unlock()
	return &job, nil
}

// Resume restarts a paused or failed job from the bytes already on disk.
func (m *Manager) Resume(id string) (*Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.jobs[id]
	if st == nil || st.removed {
		return nil, ErrNotFound
	}
	if m.closed {
		return nil, ErrClosed
	}
	switch st.job.Status {
	case StatusPaused, StatusFailed:
	case StatusQueued, StatusDownloading:
		job := st.job
		return &job, nil
	default:
		job := st.job
		return &job, nil
	}
	st.job.Status = StatusQueued
	st.job.Error = ""
	st.job.FinishedAt = 0
	st.job.UpdatedAt = nowMs()
	m.persistLocked()
	if st.running {
		st.resumeRequested = true
	} else {
		st.done = make(chan struct{})
		st.running = true
		m.wg.Add(1)
		go m.run(st)
	}
	job := st.job
	return &job, nil
}

// Remove cancels a job and deletes its files.
func (m *Manager) Remove(id string) error {
	m.mu.Lock()
	st := m.jobs[id]
	if st == nil || st.removed {
		m.mu.Unlock()
		return ErrNotFound
	}
	st.removed = true
	if st.cancel != nil {
		st.cancel()
	}
	wasRunning := st.running
	done := st.done
	delete(m.jobs, id)
	m.persistLocked()
	dir := m.jobDir(id)
	m.mu.Unlock()

	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("remove download files: %w", err)
	}
	if wasRunning {
		go func() {
			<-done
			_ = os.RemoveAll(dir)
		}()
	}
	return nil
}

// Close stops active transfers and flushes job state.
func (m *Manager) Close() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	m.cancel()
	m.mu.Unlock()

	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(shutdownWait):
		m.logger.Warn("download manager closed with active transfers")
	}

	m.mu.Lock()
	m.persistLocked()
	m.mu.Unlock()
}

func (m *Manager) run(st *jobState) {
	defer m.wg.Done()

	m.mu.Lock()
	done := st.done
	ctx, cancel := context.WithCancel(m.ctx)
	start := !st.removed && !m.closed && st.job.Status == StatusQueued
	if start {
		st.cancel = cancel
	}
	m.mu.Unlock()

	defer func() {
		cancel()
		m.mu.Lock()
		st.cancel = nil
		st.running = false
		resume := start && st.resumeRequested && !st.removed && !m.closed &&
			st.job.Status == StatusQueued
		st.resumeRequested = false
		if resume {
			st.done = make(chan struct{})
			st.running = true
			m.wg.Add(1)
			go m.run(st)
		}
		m.mu.Unlock()
		close(done)
	}()

	if !start {
		return
	}

	select {
	case <-ctx.Done():
		m.finish(ctx, st, ctx.Err())
		return
	default:
	}
	if err := m.gate.acquire(ctx); err != nil {
		m.finish(ctx, st, err)
		return
	}
	defer m.gate.release()

	m.mu.Lock()
	if st.removed || st.job.Status != StatusQueued {
		m.mu.Unlock()
		return
	}
	st.job.Status = StatusDownloading
	st.job.Error = ""
	st.job.UpdatedAt = nowMs()
	job := st.job
	m.persistLocked()
	m.mu.Unlock()
	m.emitEvent(TopicProgress, job)

	m.mu.Lock()
	resolver := m.resolver
	m.mu.Unlock()
	if resolver == nil {
		m.finish(ctx, st, fmt.Errorf("no download resolver is configured"))
		return
	}
	media, err := resolver(ctx, job)
	if err != nil {
		m.finish(ctx, st, err)
		return
	}
	if err := m.prepareFiles(st, media); err != nil {
		m.finish(ctx, st, err)
		return
	}

	m.mu.Lock()
	if st.removed {
		m.mu.Unlock()
		return
	}
	st.job.VideoID = media.Video.ID
	st.job.VideoCodecID = media.Video.CodecID
	st.job.VideoBytes = media.Video.TotalSize
	st.job.HasAudio = media.Audio != nil
	st.job.AudioStreamID = 0
	st.job.AudioBytes = 0
	if media.Audio != nil {
		st.job.AudioStreamID = media.Audio.ID
		st.job.AudioBytes = media.Audio.TotalSize
	}
	total := media.Video.TotalSize
	if media.Audio != nil {
		total += media.Audio.TotalSize
	}
	st.job.TotalBytes = total
	st.job.DownloadedBytes = m.onDiskBytesLocked(st.job.ID)
	if st.job.DownloadedBytes > total {
		st.job.DownloadedBytes = total
	}
	job = st.job
	m.persistLocked()
	m.mu.Unlock()
	m.emitEvent(TopicProgress, job)

	dir := m.jobDir(job.ID)
	if err := m.fetchFile(ctx, st, media.Video, filepath.Join(dir, "video.mp4"), 0); err != nil {
		m.finish(ctx, st, err)
		return
	}
	if media.Audio != nil {
		if err := m.fetchFile(ctx, st, *media.Audio, filepath.Join(dir, "audio.mp4"), media.Video.TotalSize); err != nil {
			m.finish(ctx, st, err)
			return
		}
	}
	m.complete(st)
}

func (m *Manager) prepareFiles(st *jobState, media *Media) error {
	if media.Video.TotalSize <= 0 {
		return fmt.Errorf("resolved video stream has no size")
	}
	if strings.TrimSpace(media.MasterPlaylist) == "" || strings.TrimSpace(media.VideoPlaylist) == "" {
		return fmt.Errorf("resolved playlists are empty")
	}
	if media.Audio != nil {
		if media.Audio.TotalSize <= 0 {
			return fmt.Errorf("resolved audio stream has no size")
		}
		if strings.TrimSpace(media.AudioPlaylist) == "" {
			return fmt.Errorf("resolved audio playlist is empty")
		}
	}

	m.mu.Lock()
	restartVideo := st.job.VideoID != 0 &&
		(media.Video.ID != st.job.VideoID || media.Video.CodecID != st.job.VideoCodecID ||
			media.Video.TotalSize != st.job.VideoBytes)
	restartAudio := st.job.HasAudio &&
		(media.Audio == nil || media.Audio.ID != st.job.AudioStreamID ||
			media.Audio.TotalSize != st.job.AudioBytes)
	dir := m.jobDir(st.job.ID)
	m.mu.Unlock()

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create job dir: %w", err)
	}
	if restartVideo {
		_ = os.Remove(filepath.Join(dir, "video.mp4"))
	}
	if restartAudio {
		_ = os.Remove(filepath.Join(dir, "audio.mp4"))
		_ = os.Remove(filepath.Join(dir, "audio-init.mp4"))
		_ = os.Remove(filepath.Join(dir, "audio.m3u8"))
	}
	files := map[string][]byte{
		"master.m3u8":    []byte(media.MasterPlaylist),
		"video.m3u8":     []byte(media.VideoPlaylist),
		"video-init.mp4": media.Video.Init,
	}
	if media.Audio != nil {
		files["audio.m3u8"] = []byte(media.AudioPlaylist)
		files["audio-init.mp4"] = media.Audio.Init
	}
	for name, data := range files {
		if err := writeFileAtomic(filepath.Join(dir, name), data); err != nil {
			return fmt.Errorf("write %s: %w", name, err)
		}
	}
	return nil
}

func (m *Manager) fetchFile(ctx context.Context, st *jobState, track Track, path string, base int64) error {
	if len(track.URLs) == 0 {
		return fmt.Errorf("no upstream URL")
	}
	offset := fileSize(path)
	if offset > track.TotalSize {
		if err := os.Truncate(path, 0); err != nil {
			return err
		}
		offset = 0
	}
	if offset >= track.TotalSize {
		m.setProgress(st, base+offset, true)
		return nil
	}

	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return err
	}

	position := offset
	report := func() {
		m.setProgress(st, base+position, false)
	}
	var lastErr error
	for attempt := 0; attempt < fetchAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		lastErr = m.fetchFromURLs(ctx, track, file, &position, report)
		if lastErr == nil {
			m.setProgress(st, base+position, true)
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		m.logger.Warn("download attempt failed", "job", st.job.ID, "attempt", attempt+1, "error", lastErr)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(attempt+1) * 2 * time.Second):
		}
	}
	return lastErr
}

func (m *Manager) fetchFromURLs(ctx context.Context, track Track, file *os.File, position *int64, report func()) error {
	var lastErr error
	for _, rawURL := range track.URLs {
		err := m.fetchOne(ctx, track, rawURL, file, position, report)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no upstream URL")
	}
	return lastErr
}

func (m *Manager) fetchOne(ctx context.Context, track Track, rawURL string, file *os.File, position *int64, report func()) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", upstreamUserAgent)
	req.Header.Set("Referer", "https://www.bilibili.com/")
	req.Header.Set("Accept-Encoding", "identity")
	if *position > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", *position))
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusPartialContent:
	case resp.StatusCode == http.StatusOK && *position == 0:
	case resp.StatusCode == http.StatusOK:
		// The server ignored Range; keep the file consistent by restarting.
		if err := file.Truncate(0); err != nil {
			return err
		}
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return err
		}
		*position = 0
		report()
	case resp.StatusCode == http.StatusRequestedRangeNotSatisfiable && *position >= track.TotalSize:
		return nil
	default:
		return fmt.Errorf("upstream returned %s", resp.Status)
	}

	buffer := make([]byte, chunkSize)
	for {
		n, readErr := resp.Body.Read(buffer)
		if n > 0 {
			if err := m.throttle.wait(ctx, int64(n)); err != nil {
				return err
			}
			if _, err := file.Write(buffer[:n]); err != nil {
				return err
			}
			*position += int64(n)
			report()
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	if track.TotalSize > 0 && *position < track.TotalSize {
		return fmt.Errorf("short download: got %d of %d bytes", *position, track.TotalSize)
	}
	return nil
}

func (m *Manager) setProgress(st *jobState, downloaded int64, force bool) {
	m.mu.Lock()
	if st.removed {
		m.mu.Unlock()
		return
	}
	changed := st.job.DownloadedBytes != downloaded
	if changed {
		st.job.DownloadedBytes = downloaded
		st.job.UpdatedAt = nowMs()
	}
	now := time.Now()
	emit := changed && (force || now.Sub(st.lastEmit) >= progressInterval)
	persist := changed && (force || now.Sub(st.lastPersist) >= persistInterval)
	if emit {
		st.lastEmit = now
	}
	if persist {
		st.lastPersist = now
		m.persistLocked()
	}
	job := st.job
	m.mu.Unlock()
	if emit {
		m.emitEvent(TopicProgress, job)
	}
}

func (m *Manager) finish(ctx context.Context, st *jobState, err error) {
	m.mu.Lock()
	if st.removed {
		m.mu.Unlock()
		return
	}
	switch {
	case st.job.Status == StatusQueued:
		// A resume was requested; the run loop will pick it up again.
	case ctx.Err() != nil || st.job.Status == StatusPaused:
		st.job.Status = StatusPaused
	default:
		st.job.Status = StatusFailed
		st.job.Error = err.Error()
	}
	st.job.UpdatedAt = nowMs()
	job := st.job
	m.persistLocked()
	m.mu.Unlock()
	if job.Status == StatusFailed {
		m.logger.Warn("download failed", "job", job.ID, "error", job.Error)
		m.emitEvent(TopicFailed, job)
		return
	}
	m.emitEvent(TopicProgress, job)
}

func (m *Manager) complete(st *jobState) {
	m.mu.Lock()
	if st.removed {
		m.mu.Unlock()
		return
	}
	st.job.Status = StatusCompleted
	st.job.Error = ""
	st.job.DownloadedBytes = st.job.TotalBytes
	st.job.FinishedAt = nowMs()
	st.job.UpdatedAt = st.job.FinishedAt
	job := st.job
	m.persistLocked()
	m.mu.Unlock()
	m.emitEvent(TopicCompleted, job)
}

func (m *Manager) emitEvent(topic string, job Job) {
	if m.emit == nil {
		return
	}
	m.emit(topic, job)
}

func (m *Manager) jobDir(id string) string {
	return filepath.Join(m.dir, id)
}

func (m *Manager) onDiskBytesLocked(id string) int64 {
	dir := m.jobDir(id)
	downloaded := fileSize(filepath.Join(dir, "video.mp4")) + fileSize(filepath.Join(dir, "audio.mp4"))
	return downloaded
}

func (m *Manager) listLocked() []Job {
	out := make([]Job, 0, len(m.jobs))
	for _, st := range m.jobs {
		if st.removed {
			continue
		}
		out = append(out, st.job)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt == out[j].CreatedAt {
			return out[i].ID > out[j].ID
		}
		return out[i].CreatedAt > out[j].CreatedAt
	})
	return out
}

func (m *Manager) persistLocked() {
	payload := struct {
		Jobs []Job `json:"jobs"`
	}{Jobs: m.listLocked()}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		m.logger.Warn("encode download jobs", "error", err)
		return
	}
	if err := writeFileAtomic(filepath.Join(m.dir, "jobs.json"), data); err != nil {
		m.logger.Warn("persist download jobs", "error", err)
	}
}

func (m *Manager) load() error {
	data, err := os.ReadFile(filepath.Join(m.dir, "jobs.json"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	var payload struct {
		Jobs []Job `json:"jobs"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return fmt.Errorf("decode jobs.json: %w", err)
	}
	for _, job := range payload.Jobs {
		if !validID(job.ID) || job.BVID == "" {
			continue
		}
		switch job.Status {
		case StatusQueued, StatusDownloading:
			job.Status = StatusPaused
		case StatusCompleted:
			if fileSize(filepath.Join(m.jobDir(job.ID), "master.m3u8")) == 0 {
				job.Status = StatusFailed
				job.Error = "downloaded files are missing"
			}
		}
		st := &jobState{job: job, done: make(chan struct{})}
		m.refreshProgressLocked(st)
		m.jobs[job.ID] = st
	}
	return nil
}

func (m *Manager) refreshProgressLocked(st *jobState) {
	downloaded := m.onDiskBytesLocked(st.job.ID)
	if st.job.TotalBytes > 0 && downloaded > st.job.TotalBytes {
		downloaded = st.job.TotalBytes
	}
	st.job.DownloadedBytes = downloaded
	if st.job.Status == StatusCompleted && st.job.TotalBytes > 0 && downloaded < st.job.TotalBytes {
		st.job.Status = StatusFailed
		st.job.Error = "downloaded files are incomplete"
	}
}

func writeFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return 0
	}
	return info.Size()
}

func newID() (string, error) {
	buffer := make([]byte, 8)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return hex.EncodeToString(buffer), nil
}

func validID(id string) bool {
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

func nowMs() int64 {
	return time.Now().UnixMilli()
}

func clampConcurrency(n int) int {
	if n < 1 {
		return 1
	}
	if n > 8 {
		return 8
	}
	return n
}

// gate limits how many transfers run at once while allowing the limit to
// change at runtime. A released slot is handed straight to the next waiter.
type gate struct {
	mu      sync.Mutex
	limit   int
	active  int
	nextID  uint64
	waiters map[uint64]chan struct{}
}

func newGate(limit int) *gate {
	return &gate{limit: clampConcurrency(limit), waiters: map[uint64]chan struct{}{}}
}

func (g *gate) acquire(ctx context.Context) error {
	g.mu.Lock()
	if g.active < g.limit {
		g.active++
		g.mu.Unlock()
		return nil
	}
	id := g.nextID
	g.nextID++
	ch := make(chan struct{}, 1)
	g.waiters[id] = ch
	g.mu.Unlock()

	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		g.mu.Lock()
		if _, ok := g.waiters[id]; ok {
			delete(g.waiters, id)
			g.mu.Unlock()
			return ctx.Err()
		}
		g.mu.Unlock()
		// A slot was handed to this waiter as it was cancelling; consume it.
		select {
		case <-ch:
		default:
		}
		g.release()
		return ctx.Err()
	}
}

func (g *gate) release() {
	g.mu.Lock()
	defer g.mu.Unlock()
	for id, ch := range g.waiters {
		delete(g.waiters, id)
		ch <- struct{}{}
		return
	}
	if g.active > 0 {
		g.active--
	}
}

func (g *gate) setLimit(limit int) {
	limit = clampConcurrency(limit)
	g.mu.Lock()
	defer g.mu.Unlock()
	g.limit = limit
	for g.active < g.limit {
		woke := false
		for id, ch := range g.waiters {
			delete(g.waiters, id)
			g.active++
			ch <- struct{}{}
			woke = true
			break
		}
		if !woke {
			break
		}
	}
}

func (g *gate) limitValue() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.limit
}

// throttle spaces reads so that concurrent jobs share one byte rate.
type throttle struct {
	mu   sync.Mutex
	rate int64
	next time.Time
}

func (t *throttle) setRate(rate int64) {
	t.mu.Lock()
	t.rate = rate
	t.next = time.Time{}
	t.mu.Unlock()
}

func (t *throttle) rateValue() int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.rate
}

func (t *throttle) wait(ctx context.Context, n int64) error {
	if n <= 0 {
		return nil
	}
	t.mu.Lock()
	rate := t.rate
	if rate <= 0 {
		t.mu.Unlock()
		return nil
	}
	now := time.Now()
	if t.next.Before(now) {
		t.next = now
	}
	delay := t.next.Sub(now)
	t.next = t.next.Add(time.Duration(float64(n) / float64(rate) * float64(time.Second)))
	t.mu.Unlock()
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
