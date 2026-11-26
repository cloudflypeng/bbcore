package service

import (
	"encoding/json"
	"testing"
)

func TestPGCPlayUnavailableMapping(t *testing.T) {
	cases := map[int]string{
		-10403: "region_limited",
		-10402: "region_limited",
		87007:  "entitlement_required",
		87008:  "entitlement_required",
		-101:   "auth_required",
		-404:   "not_found",
		-9999:  "upstream_error",
	}
	for code, want := range cases {
		out := pgcPlayUnavailable(code, "msg")
		if out["reason"] != want {
			t.Errorf("code %d reason = %v, want %q", code, out["reason"], want)
		}
		if out["playable"] != false {
			t.Errorf("code %d should not be playable", code)
		}
	}
	out := pgcPlayUnavailable(87007, "0")
	if out["message"] != "剧集暂不可播放" {
		t.Errorf("empty message = %v", out["message"])
	}
}

func TestPGCEpisodeListDedupesPreservingOrder(t *testing.T) {
	season := map[string]any{
		"episodes": []any{
			map[string]any{"ep_id": float64(2), "cid": float64(20), "title": "2", "long_title": "B", "duration": float64(120000)},
			map[string]any{"ep_id": float64(1), "cid": float64(10), "title": "1", "long_title": "A", "duration": float64(60000)},
		},
		"main_section": map[string]any{
			"episodes": []any{
				map[string]any{"ep_id": float64(2), "cid": float64(20), "title": "2"},
				map[string]any{"ep_id": float64(3), "cid": float64(30), "title": "3"},
			},
		},
	}
	list := pgcEpisodeList(season)
	if len(list) != 3 {
		t.Fatalf("episodes = %d, want 3", len(list))
	}
	if list[0]["ep_id"] != int64(2) || list[1]["ep_id"] != int64(1) || list[2]["ep_id"] != int64(3) {
		t.Fatalf("unexpected order: %#v", list)
	}
	if list[1]["duration_ms"] != int64(60000) {
		t.Fatalf("duration_ms = %v", list[1]["duration_ms"])
	}
}

func TestPGCFindEpisode(t *testing.T) {
	episodes := []map[string]any{
		{"ep_id": int64(1), "cid": int64(10)},
		{"ep_id": int64(2), "cid": int64(20)},
	}
	if ep := pgcFindEpisode(episodes, "cid", 20); ep == nil || ep["ep_id"] != int64(2) {
		t.Fatalf("cid lookup failed: %#v", ep)
	}
	if ep := pgcFindEpisode(episodes, "ep_id", 1); ep == nil || ep["cid"] != int64(10) {
		t.Fatalf("ep_id lookup failed: %#v", ep)
	}
	if ep := pgcFindEpisode(episodes, "cid", 99); ep != nil {
		t.Fatalf("expected nil, got %#v", ep)
	}
}

func TestPGCQualities(t *testing.T) {
	qns := []int64{125, 112, 80}
	labels := []string{"真彩 HDR", "高清 1080P+", "高清 1080P"}
	streams := []dashStream{
		{ID: 80, Codecid: 12, Width: 1920, Height: 1080, Bandwidth: 3000, BaseURL: "https://a", SegmentBase: segmentBase{Initialization: "0-1", IndexRange: "2-3"}},
		{ID: 80, Codecid: 7, Width: 1920, Height: 1080, Bandwidth: 2500, BaseURL: "https://b", SegmentBase: segmentBase{Initialization: "0-1", IndexRange: "2-3"}},
		{ID: 112, Codecid: 12, BaseURL: "", SegmentBase: segmentBase{Initialization: "0-1", IndexRange: "2-3"}},
	}
	out := pgcQualities(qns, labels, streams)
	if len(out) != 1 {
		t.Fatalf("qualities = %d, want 1 (unavailable QN dropped)", len(out))
	}
	if out[0]["qn"] != int64(80) || out[0]["label"] != "高清 1080P" {
		t.Fatalf("unexpected quality: %#v", out[0])
	}
	codecs, _ := out[0]["codecs"].([]string)
	if len(codecs) != 2 || codecs[0] != "hevc" || codecs[1] != "avc" {
		t.Fatalf("codecs = %#v", codecs)
	}
}

func TestPGCQualityLabelFallback(t *testing.T) {
	out := pgcQualityLabel([]int64{80}, []string{""}, 80)
	if out["label"] != "1080P" {
		t.Fatalf("label = %v", out["label"])
	}
	out = pgcQualityLabel(nil, nil, 64)
	if out["label"] != "720P" {
		t.Fatalf("label = %v", out["label"])
	}
}

func TestPGCPlayPayloadDecode(t *testing.T) {
	const body = `{
		"code": 0,
		"message": "0",
		"data": {
			"plugins": [{"name": "VipQualityTrialPlugin"}],
			"user_status": {"isLogin": 1, "vip": 0},
			"video_info": {
				"timelength": 123000,
				"is_preview": 1,
				"accept_quality": [80, 64],
				"accept_description": ["高清 1080P", "高清 720P"],
				"dash": {
					"duration": 123,
					"video": [{"id": 80, "codecid": 12, "base_url": "https://a", "segment_base": {"initialization": "0-9", "index_range": "10-19"}}],
					"audio": [{"id": 30280, "base_url": "https://b", "segment_base": {"initialization": "0-9", "index_range": "10-19"}}]
				}
			}
		}
	}`
	var payload pgcPlayPayload
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.Code != 0 || payload.Data.VideoInfo.Dash == nil {
		t.Fatal("missing dash")
	}
	if payload.Data.VideoInfo.Timelength != 123000 || len(payload.Data.VideoInfo.Dash.Video) != 1 {
		t.Fatalf("unexpected payload: %#v", payload.Data.VideoInfo)
	}
	if !pgcFlag(payload.Data.VideoInfo.IsPreview) {
		t.Fatal("expected numeric is_preview to parse as true")
	}
	if pgcFlag(float64(0)) || pgcFlag(nil) || pgcFlag("false") {
		t.Fatal("pgcFlag should treat 0/nil/\"false\" as false")
	}
}

func TestPGCPlayRequiresIdentifiers(t *testing.T) {
	s := New(Options{})
	if _, err := s.pgcPlay(t.Context(), json.RawMessage(`{}`)); err == nil {
		t.Fatal("expected invalid params error")
	}
}
