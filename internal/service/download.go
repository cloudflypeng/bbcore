package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/cloudflypeng/bbcore/internal/download"
	"github.com/cloudflypeng/bbcore/internal/jsonrpc"
)

func (s *Service) registerDownload() {
	s.add("download.start", "Download a video for offline playback (params: bvid, cid, page, qn, codec, audio_id, title, cover, owner, duration_ms)", s.downloadStart)
	s.add("download.list", "List download jobs and disk usage", s.downloadList)
	s.add("download.status", "Get one download job (params: id)", s.downloadStatus)
	s.add("download.pause", "Pause a download (params: id)", s.downloadPause)
	s.add("download.resume", "Resume a paused or failed download (params: id)", s.downloadResume)
	s.add("download.remove", "Delete a download and its files (params: id)", s.downloadRemove)
	s.add("download.url", "Local HLS URL for a completed download (params: id)", s.downloadURL)
	s.add("download.config", "Read or update download concurrency and limit (params: concurrency, limit_kbps)", s.downloadConfig)
}

func (s *Service) downloadManager() (*download.Manager, *jsonrpc.Error) {
	s.downloadMu.Lock()
	defer s.downloadMu.Unlock()
	if s.downloads != nil {
		return s.downloads, nil
	}
	dir := strings.TrimSpace(s.opt.DataDir)
	if dir == "" {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInternalError, "downloads require a data directory")
	}
	manager, err := download.New(download.Options{
		Dir:         filepath.Join(dir, "downloads"),
		Logger:      s.opt.Logger,
		Emit:        s.publishDownloadEvent,
		Concurrency: 1,
	})
	if err != nil {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInternalError, "start download manager: %v", err)
	}
	manager.SetResolver(s.resolveDownload)
	s.downloads = manager
	return manager, nil
}

func (s *Service) publishDownloadEvent(topic string, data any) {
	if s.opt.Events != nil {
		s.opt.Events.Publish(topic, data)
		return
	}
	s.Notify("event."+topic, data)
}

type downloadJobView struct {
	download.Job
	MasterURL string `json:"master_url,omitempty"`
}

func (s *Service) downloadView(job download.Job) downloadJobView {
	view := downloadJobView{Job: job}
	if job.Status == download.StatusCompleted {
		if gateway, rpcErr := s.streamGateway(); rpcErr == nil {
			view.MasterURL = gateway.baseURL + "/d/" + job.ID + "/master.m3u8"
		}
	}
	return view
}

func (s *Service) downloadStart(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	bvid := strings.TrimSpace(p.String("bvid"))
	if bvid == "" {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "bvid is required")
	}
	cid, ok := p.ID("cid")
	if !ok || cid <= 0 {
		cid, rpcErr = s.resolveStreamCID(ctx, bvid, p.IntDefault("page", 1))
		if rpcErr != nil {
			return nil, rpcErr
		}
	}
	manager, rpcErr := s.downloadManager()
	if rpcErr != nil {
		return nil, rpcErr
	}
	job, err := manager.Start(download.Spec{
		BVID:       bvid,
		CID:        cid,
		Title:      p.String("title"),
		Cover:      p.String("cover"),
		Owner:      p.String("owner"),
		QN:         int(p.IntDefault("qn", 80)),
		Codec:      p.StringDefault("codec", "hevc"),
		AudioID:    int(p.IntDefault("audio_id", 0)),
		DurationMS: p.IntDefault("duration_ms", 0),
	})
	if err != nil {
		return nil, downloadRPCError(err)
	}
	return s.downloadView(*job), nil
}

func (s *Service) downloadList(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	manager, rpcErr := s.downloadManager()
	if rpcErr != nil {
		return nil, rpcErr
	}
	jobs := manager.List()
	items := make([]downloadJobView, 0, len(jobs))
	summary := map[string]int{
		"total":       len(jobs),
		"queued":      0,
		"downloading": 0,
		"paused":      0,
		"completed":   0,
		"failed":      0,
	}
	var usedBytes int64
	for _, job := range jobs {
		items = append(items, s.downloadView(job))
		if _, ok := summary[job.Status]; ok {
			summary[job.Status]++
		}
		usedBytes += job.DownloadedBytes
	}
	free, capacity := diskSpace(filepath.Join(s.opt.DataDir, "downloads"))
	return map[string]any{
		"items":      items,
		"summary":    summary,
		"used_bytes": usedBytes,
		"free_bytes": free,
		"disk_bytes": capacity,
	}, nil
}

func (s *Service) downloadStatus(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	id, rpcErr := downloadParamID(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	manager, rpcErr := s.downloadManager()
	if rpcErr != nil {
		return nil, rpcErr
	}
	job, ok := manager.Get(id)
	if !ok {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "download not found")
	}
	return s.downloadView(*job), nil
}

func (s *Service) downloadPause(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	id, rpcErr := downloadParamID(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	manager, rpcErr := s.downloadManager()
	if rpcErr != nil {
		return nil, rpcErr
	}
	job, err := manager.Pause(id)
	if err != nil {
		return nil, downloadRPCError(err)
	}
	return s.downloadView(*job), nil
}

func (s *Service) downloadResume(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	id, rpcErr := downloadParamID(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	manager, rpcErr := s.downloadManager()
	if rpcErr != nil {
		return nil, rpcErr
	}
	job, err := manager.Resume(id)
	if err != nil {
		return nil, downloadRPCError(err)
	}
	return s.downloadView(*job), nil
}

func (s *Service) downloadRemove(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	id, rpcErr := downloadParamID(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	manager, rpcErr := s.downloadManager()
	if rpcErr != nil {
		return nil, rpcErr
	}
	if err := manager.Remove(id); err != nil {
		return nil, downloadRPCError(err)
	}
	return map[string]any{"removed": true}, nil
}

func (s *Service) downloadURL(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	id, rpcErr := downloadParamID(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	manager, rpcErr := s.downloadManager()
	if rpcErr != nil {
		return nil, rpcErr
	}
	job, ok := manager.Get(id)
	if !ok {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "download not found")
	}
	if job.Status != download.StatusCompleted {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "download is not complete")
	}
	gateway, rpcErr := s.streamGateway()
	if rpcErr != nil {
		return nil, rpcErr
	}
	return map[string]any{
		"master_url":  gateway.baseURL + "/d/" + job.ID + "/master.m3u8",
		"duration_ms": job.DurationMS,
	}, nil
}

func (s *Service) downloadConfig(ctx context.Context, raw json.RawMessage) (any, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return nil, rpcErr
	}
	manager, rpcErr := s.downloadManager()
	if rpcErr != nil {
		return nil, rpcErr
	}
	if _, ok := p.Raw("concurrency"); ok {
		manager.SetConcurrency(int(p.IntDefault("concurrency", 1)))
	}
	if _, ok := p.Raw("limit_kbps"); ok {
		kbps := p.IntDefault("limit_kbps", 0)
		if kbps < 0 {
			kbps = 0
		}
		manager.SetLimit(kbps * 1024)
	}
	concurrency, limit := manager.Config()
	return map[string]any{
		"concurrency":  concurrency,
		"limit_kbps":   limit / 1024,
		"download_dir": filepath.Join(s.opt.DataDir, "downloads"),
	}, nil
}

func downloadParamID(raw json.RawMessage) (string, *jsonrpc.Error) {
	p, rpcErr := jsonrpc.ParseParams(raw)
	if rpcErr != nil {
		return "", rpcErr
	}
	id := strings.TrimSpace(p.String("id"))
	if id == "" {
		return "", jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "id is required")
	}
	return id, nil
}

func downloadRPCError(err error) *jsonrpc.Error {
	switch {
	case errors.Is(err, download.ErrNotFound):
		return jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "download not found")
	case errors.Is(err, download.ErrClosed):
		return jsonrpc.Errorf(jsonrpc.CodeInternalError, "download manager is closed")
	default:
		return jsonrpc.Errorf(jsonrpc.CodeInternalError, "%v", err)
	}
}

func diskSpace(dir string) (free, total int64) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(dir, &stat); err != nil {
		return 0, 0
	}
	size := int64(stat.Bsize)
	return int64(stat.Bavail) * size, int64(stat.Blocks) * size
}

// resolveDownload refreshes the playurl response, fetches indexes and init
// segments, and generates local playlists over the fMP4 files.
func (s *Service) resolveDownload(ctx context.Context, job download.Job) (*download.Media, error) {
	playParams := map[string]any{
		"bvid":  job.BVID,
		"cid":   job.CID,
		"qn":    job.QN,
		"fnval": 4048,
	}
	encoded, err := json.Marshal(playParams)
	if err != nil {
		return nil, fmt.Errorf("encode playurl params: %w", err)
	}
	result, rpcErr := s.videoPlayURL(ctx, encoded)
	if rpcErr != nil {
		return nil, errors.New(rpcErr.Message)
	}
	payloadBytes, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("encode playurl response: %w", err)
	}
	var payload playURLPayload
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		return nil, fmt.Errorf("decode playurl response: %w", err)
	}
	if payload.Code != 0 {
		return nil, fmt.Errorf("playurl: %s (code %d)", payload.Message, payload.Code)
	}
	if payload.Data.Dash == nil {
		return nil, fmt.Errorf("playurl returned no DASH streams")
	}
	video, err := chooseVideoStream(payload.Data.Dash.Video, int64(job.QN), job.Codec)
	if err != nil {
		return nil, err
	}
	audio, err := chooseAudioStream(payload.Data.Dash.Audio, int64(job.AudioID))
	if err != nil {
		return nil, err
	}
	gateway, rpcErr := s.streamGateway()
	if rpcErr != nil {
		return nil, errors.New(rpcErr.Message)
	}

	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	videoInit, err := gateway.fetchRange(ctx, video.urls, video.initStart, video.initEnd)
	if err != nil {
		return nil, fmt.Errorf("fetch video init: %w", err)
	}
	videoIndex, err := gateway.fetchRange(ctx, video.urls, video.indexStart, video.indexEnd)
	if err != nil {
		return nil, fmt.Errorf("fetch video index: %w", err)
	}
	videoSegments, err := parseSIDX(videoIndex, video.indexStart)
	if err != nil {
		return nil, fmt.Errorf("parse video index: %w", err)
	}
	videoTrack := &streamTrack{source: *video, initData: videoInit, segments: videoSegments}
	videoMedia := download.Track{
		ID:        video.id,
		CodecID:   video.codecID,
		MimeType:  video.mimeType,
		URLs:      video.urls,
		Init:      videoInit,
		TotalSize: trackTotalSize(videoSegments, video.initEnd, video.indexEnd),
	}
	videoPlaylist, err := mediaPlaylist(videoSegments, "video.mp4", "video-init.mp4")
	if err != nil {
		return nil, fmt.Errorf("build video playlist: %w", err)
	}

	var audioTrack *streamTrack
	var audioMedia *download.Track
	var audioPlaylist string
	if audio != nil {
		audioInit, err := gateway.fetchRange(ctx, audio.urls, audio.initStart, audio.initEnd)
		if err != nil {
			return nil, fmt.Errorf("fetch audio init: %w", err)
		}
		audioIndex, err := gateway.fetchRange(ctx, audio.urls, audio.indexStart, audio.indexEnd)
		if err != nil {
			return nil, fmt.Errorf("fetch audio index: %w", err)
		}
		audioSegments, err := parseSIDX(audioIndex, audio.indexStart)
		if err != nil {
			return nil, fmt.Errorf("parse audio index: %w", err)
		}
		audioTrack = &streamTrack{source: *audio, initData: audioInit, segments: audioSegments}
		audioMedia = &download.Track{
			ID:        audio.id,
			CodecID:   audio.codecID,
			MimeType:  audio.mimeType,
			URLs:      audio.urls,
			Init:      audioInit,
			TotalSize: trackTotalSize(audioSegments, audio.initEnd, audio.indexEnd),
		}
		audioPlaylist, err = mediaPlaylist(audioSegments, "audio.mp4", "audio-init.mp4")
		if err != nil {
			return nil, fmt.Errorf("build audio playlist: %w", err)
		}
	}

	return &download.Media{
		Video:          videoMedia,
		Audio:          audioMedia,
		MasterPlaylist: masterPlaylist(videoTrack, audioTrack),
		VideoPlaylist:  videoPlaylist,
		AudioPlaylist:  audioPlaylist,
	}, nil
}

func trackTotalSize(segments []hlsSegment, ends ...int64) int64 {
	total := int64(0)
	for _, end := range ends {
		if end+1 > total {
			total = end + 1
		}
	}
	for _, segment := range segments {
		if end := segment.offset + segment.size; end > total {
			total = end
		}
	}
	return total
}
