package service

import (
	"strings"
	"testing"
)

func TestQualityLabels(t *testing.T) {
	cases := map[int64]string{
		127: "8K",
		120: "4K",
		116: "1080P60",
		80:  "1080P",
		64:  "720P",
		16:  "360P",
		3:   "QN3",
	}
	for qn, want := range cases {
		if got := qualityLabel(qn); got != want {
			t.Errorf("qualityLabel(%d) = %q, want %q", qn, got, want)
		}
	}
	if codecLabel(7) != "avc" || codecLabel(12) != "hevc" || codecLabel(13) != "av1" || codecLabel(99) != "codec99" {
		t.Fatal("unexpected codec labels")
	}
}

func TestVTTTime(t *testing.T) {
	cases := map[float64]string{
		0:        "00:00:00.000",
		1.5:      "00:00:01.500",
		59.999:   "00:00:59.999",
		60:       "00:01:00.000",
		3661.234: "01:01:01.234",
		-2:       "00:00:00.000",
	}
	for seconds, want := range cases {
		if got := vttTime(seconds); got != want {
			t.Errorf("vttTime(%v) = %q, want %q", seconds, got, want)
		}
	}
}

func TestJSONToVTT(t *testing.T) {
	body := []any{
		map[string]any{"from": 0.0, "to": 1.25, "content": "hello"},
		map[string]any{"from": 2.0, "to": 3.5, "content": "  world  "},
		map[string]any{"from": 4.0, "to": 3.0, "content": "backwards"},
		map[string]any{"from": 5.0, "to": 6.0, "content": "   "},
		"not a cue",
	}
	vtt, cues := jsonToVTT(body)
	if cues != 2 {
		t.Fatalf("cues = %d, want 2", cues)
	}
	want := "WEBVTT\n\n00:00:00.000 --> 00:00:01.250\nhello\n\n00:00:02.000 --> 00:00:03.500\nworld\n\n"
	if vtt != want {
		t.Fatalf("vtt = %q, want %q", vtt, want)
	}
}

func TestNormalizeSubtitleURL(t *testing.T) {
	valid := []string{
		"https://i0.hdslb.com/bfs/subtitle/a.json",
		"https://aisubtitle.hdslb.com/bfs/subtitle/a.json",
		"https://www.bilibili.com/x/a.json",
		"//i0.hdslb.com/bfs/subtitle/a.json",
	}
	for _, raw := range valid {
		if _, ok := normalizeSubtitleURL(raw); !ok {
			t.Errorf("expected %q to be accepted", raw)
		}
	}
	invalid := []string{
		"",
		"http://i0.hdslb.com/a.json",
		"https://evil.com/a.json",
		"ftp://i0.hdslb.com/a.json",
		"not a url",
	}
	for _, raw := range invalid {
		if _, ok := normalizeSubtitleURL(raw); ok {
			t.Errorf("expected %q to be rejected", raw)
		}
	}
}

func TestParsePages(t *testing.T) {
	pages := parsePages([]any{
		map[string]any{"page": float64(1), "cid": float64(11), "part": "P1", "duration": float64(60)},
		map[string]any{"page": float64(2), "cid": float64(22), "part": "P2", "duration": float64(90)},
		"bad",
	})
	if len(pages) != 2 {
		t.Fatalf("pages = %d, want 2", len(pages))
	}
	if pages[0]["cid"] != int64(11) || pages[0]["duration_ms"] != int64(60000) || pages[0]["part"] != "P1" {
		t.Fatalf("unexpected first page: %#v", pages[0])
	}
}

func TestParseSubtitlesFiltersUntrustedURLs(t *testing.T) {
	playInfo := map[string]any{
		"subtitle": map[string]any{
			"subtitles": []any{
				map[string]any{"lan": "zh-CN", "lan_doc": "中文", "subtitle_url": "//i0.hdslb.com/a.json"},
				map[string]any{"lan": "en", "lan_doc": "English", "subtitle_url": "https://evil.com/a.json"},
			},
		},
	}
	subtitles := parseSubtitles(playInfo)
	if len(subtitles) != 1 {
		t.Fatalf("subtitles = %d, want 1", len(subtitles))
	}
	if subtitles[0]["url"] != "https://i0.hdslb.com/a.json" {
		t.Fatalf("url = %v", subtitles[0]["url"])
	}
}

func TestJSONToVTTEmpty(t *testing.T) {
	vtt, cues := jsonToVTT(nil)
	if cues != 0 || !strings.HasPrefix(vtt, "WEBVTT\n\n") {
		t.Fatalf("vtt = %q, cues = %d", vtt, cues)
	}
}
