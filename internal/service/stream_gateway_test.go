package service

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseByteRange(t *testing.T) {
	tests := []struct {
		raw   string
		start int64
		end   int64
		ok    bool
	}{
		{"0-1099", 0, 1099, true},
		{" 12-12 ", 12, 12, true},
		{"100-", 0, 0, false},
		{"-100", 0, 0, false},
		{"10-9", 0, 0, false},
		{"a-10", 0, 0, false},
		{"", 0, 0, false},
	}
	for _, test := range tests {
		start, end, err := parseByteRange(test.raw)
		if test.ok && err != nil {
			t.Fatalf("%q: unexpected error: %v", test.raw, err)
		}
		if !test.ok && err == nil {
			t.Fatalf("%q: expected an error", test.raw)
		}
		if test.ok && (start != test.start || end != test.end) {
			t.Fatalf("%q: got %d-%d", test.raw, start, end)
		}
	}
}

type sidxReference struct {
	size     uint32
	duration uint32
}

func makeSIDX(version byte, timescale uint32, firstOffset uint64, refs []sidxReference) []byte {
	headerSize := 32
	if version == 1 {
		headerSize = 40
	}
	size := headerSize + len(refs)*12
	box := make([]byte, size)
	binary.BigEndian.PutUint32(box[0:4], uint32(size))
	copy(box[4:8], "sidx")
	box[8] = version
	binary.BigEndian.PutUint32(box[12:16], 1)
	binary.BigEndian.PutUint32(box[16:20], timescale)
	if version == 0 {
		binary.BigEndian.PutUint32(box[24:28], uint32(firstOffset))
		binary.BigEndian.PutUint16(box[30:32], uint16(len(refs)))
	} else {
		binary.BigEndian.PutUint64(box[20:28], 0)
		binary.BigEndian.PutUint64(box[28:36], firstOffset)
		binary.BigEndian.PutUint16(box[38:40], uint16(len(refs)))
	}
	offset := headerSize
	for _, ref := range refs {
		binary.BigEndian.PutUint32(box[offset:offset+4], ref.size)
		binary.BigEndian.PutUint32(box[offset+4:offset+8], ref.duration)
		offset += 12
	}
	return box
}

func TestParseSIDX(t *testing.T) {
	sidx := makeSIDX(0, 16000, 0, []sidxReference{{100, 8000}, {200, 16000}})
	segments, err := parseSIDX(sidx, 5000)
	if err != nil {
		t.Fatal(err)
	}
	if len(segments) != 2 {
		t.Fatalf("got %d segments", len(segments))
	}
	firstOffset := int64(5000 + len(sidx))
	if segments[0].offset != firstOffset || segments[0].size != 100 || segments[0].duration != 0.5 {
		t.Fatalf("first segment = %+v", segments[0])
	}
	if segments[1].offset != firstOffset+100 || segments[1].size != 200 || segments[1].duration != 1 {
		t.Fatalf("second segment = %+v", segments[1])
	}
}

func TestParseSIDXExtendedAndPrefix(t *testing.T) {
	sidx := makeSIDX(1, 90000, 7, []sidxReference{{42, 4500}})
	prefix := make([]byte, 8)
	binary.BigEndian.PutUint32(prefix[0:4], 8)
	copy(prefix[4:8], "free")
	segments, err := parseSIDX(append(prefix, sidx...), 1000)
	if err != nil {
		t.Fatal(err)
	}
	wantOffset := int64(1000 + len(prefix) + len(sidx) + 7)
	if len(segments) != 1 || segments[0].offset != wantOffset || segments[0].duration != 0.05 {
		t.Fatalf("segments = %+v, want offset %d", segments, wantOffset)
	}
}

func TestParseSIDXRejectsInvalid(t *testing.T) {
	hierarchical := makeSIDX(0, 1000, 0, []sidxReference{{10, 10}})
	binary.BigEndian.PutUint32(hierarchical[32:36], 0x80000000|10)
	size := binary.BigEndian.Uint32(hierarchical[0:4])
	binary.BigEndian.PutUint32(hierarchical[0:4], size&0x7fffffff)
	if _, err := parseSIDX(hierarchical, 0); err == nil {
		t.Fatal("expected hierarchical reference error")
	}
	truncated := makeSIDX(0, 1000, 0, []sidxReference{{10, 10}})
	if _, err := parseSIDX(truncated[:len(truncated)-4], 0); err == nil {
		t.Fatal("expected truncated box error")
	}
	if _, err := parseSIDX([]byte("not a sidx"), 0); err == nil {
		t.Fatal("expected missing sidx error")
	}
}

func TestMediaPlaylist(t *testing.T) {
	playlist, err := mediaPlaylist([]hlsSegment{
		{offset: 100, size: 10, duration: 4},
		{offset: 200, size: 20, duration: 5.25},
	}, "video.mp4", "video-init.mp4")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{
		"#EXT-X-VERSION:7",
		"#EXT-X-PLAYLIST-TYPE:VOD",
		"#EXT-X-TARGETDURATION:6",
		`#EXT-X-MAP:URI="video-init.mp4"`,
		"#EXTINF:4.000000,",
		"#EXT-X-BYTERANGE:10@100",
		"video.mp4",
		"#EXT-X-BYTERANGE:20@200",
		"#EXT-X-ENDLIST",
	} {
		if !strings.Contains(playlist, line) {
			t.Fatalf("playlist missing %q:\n%s", line, playlist)
		}
	}
	if _, err := mediaPlaylist(nil, "video.mp4", "init.mp4"); err == nil {
		t.Fatal("expected empty playlist error")
	}
}

func TestMasterPlaylist(t *testing.T) {
	video := &streamTrack{source: streamSource{
		codecs: "hvc1.1.6.L150.90", bandwidth: 1_000_000, width: 1920, height: 1080, frameRate: 30,
	}}
	audio := &streamTrack{source: streamSource{codecs: "mp4a.40.2", bandwidth: 128_000}}
	playlist := masterPlaylist(video, audio)
	for _, line := range []string{
		`#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="audio"`,
		"BANDWIDTH=1128000",
		"RESOLUTION=1920x1080",
		"FRAME-RATE=30.000",
		`CODECS="hvc1.1.6.L150.90,mp4a.40.2"`,
		`AUDIO="audio"`,
	} {
		if !strings.Contains(playlist, line) {
			t.Fatalf("master playlist missing %q:\n%s", line, playlist)
		}
	}
}

func TestMasterPlaylistUsesSegmentPeakBandwidth(t *testing.T) {
	video := &streamTrack{
		source: streamSource{codecs: "avc1.640032", bandwidth: 785_811, width: 1920, height: 1080, frameRate: 30},
		segments: []hlsSegment{
			{offset: 0, size: 1_000_000, duration: 4},
			{offset: 1_000_000, size: 500_000, duration: 5},
		},
	}
	audio := &streamTrack{
		source:   streamSource{codecs: "mp4a.40.2"},
		segments: []hlsSegment{{offset: 0, size: 60_000, duration: 4}},
	}
	playlist := masterPlaylist(video, audio)
	for _, line := range []string{
		"BANDWIDTH=2226000,AVERAGE-BANDWIDTH=1453333",
	} {
		if !strings.Contains(playlist, line) {
			t.Fatalf("master playlist missing %q:\n%s", line, playlist)
		}
	}
}

func testDashStream(id, codecid int, bandwidth int64) dashStream {
	return dashStream{
		ID:        id,
		Codecid:   codecid,
		Codecs:    "hvc1.1.6.L150.90",
		Width:     1920,
		Height:    1080,
		Bandwidth: bandwidth,
		BaseURL:   "https://example.invalid/stream.m4s",
		SegmentBase: segmentBase{
			Initialization: "0-999",
			IndexRange:     "1000-1999",
		},
	}
}

func TestChooseVideoStream(t *testing.T) {
	streams := []dashStream{
		testDashStream(64, 12, 400_000),
		testDashStream(80, 7, 800_000),
		testDashStream(80, 12, 700_000),
		testDashStream(80, 13, 900_000),
	}
	hevc, err := chooseVideoStream(streams, 80, "hevc")
	if err != nil {
		t.Fatal(err)
	}
	if hevc.id != 80 || hevc.codecID != 12 {
		t.Fatalf("hevc stream = %+v", hevc)
	}
	avc, err := chooseVideoStream(streams, 80, "avc")
	if err != nil {
		t.Fatal(err)
	}
	if avc.id != 80 || avc.codecID != 7 {
		t.Fatalf("avc stream = %+v", avc)
	}
	lower, err := chooseVideoStream(streams, 64, "hevc")
	if err != nil {
		t.Fatal(err)
	}
	if lower.id != 64 {
		t.Fatalf("quality-limited stream = %+v", lower)
	}
	if _, err := chooseVideoStream(streams, 80, "av1"); err == nil {
		t.Fatal("expected unsupported codec error")
	}
	if _, err := chooseVideoStream([]dashStream{testDashStream(80, 13, 900_000)}, 80, "hevc"); err == nil {
		t.Fatal("expected AV1-only error")
	}
}

func TestChooseAudioStream(t *testing.T) {
	streams := []dashStream{
		testDashStream(30216, 0, 64_000),
		testDashStream(30280, 0, 192_000),
		testDashStream(30232, 0, 128_000),
	}
	for i := range streams {
		streams[i].Width = 0
		streams[i].Height = 0
		streams[i].Codecs = "mp4a.40.2"
	}
	dolby := testDashStream(30250, 0, 448_000)
	dolby.Width = 0
	dolby.Height = 0
	dolby.Codecs = "ec-3"
	streams = append(streams, dolby)

	best, err := chooseAudioStream(streams, 0)
	if err != nil {
		t.Fatal(err)
	}
	if best.id != 30280 || best.bandwidth != 192_000 || !isAACStream(dashStream{ID: best.id, Codecs: best.codecs}) {
		t.Fatalf("best audio = %+v", best)
	}
	explicit, err := chooseAudioStream(streams, 30216)
	if err != nil {
		t.Fatal(err)
	}
	if explicit.id != 30216 {
		t.Fatalf("explicit audio = %+v", explicit)
	}
	dolbyExplicit, err := chooseAudioStream(streams, 30250)
	if err != nil {
		t.Fatal(err)
	}
	if dolbyExplicit.id != 30250 {
		t.Fatalf("explicit Dolby audio = %+v", dolbyExplicit)
	}
	if _, err := chooseAudioStream(streams, 999); err == nil {
		t.Fatal("expected missing audio error")
	}
}

func TestStreamGatewayServesSession(t *testing.T) {
	upstreamData := []byte("0123456789abcdef")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rangeHeader := r.Header.Get("Range")
		if rangeHeader == "" {
			w.Header().Set("Content-Length", fmt.Sprint(len(upstreamData)))
			_, _ = w.Write(upstreamData)
			return
		}
		var start, end int
		if _, err := fmt.Sscanf(rangeHeader, "bytes=%d-%d", &start, &end); err != nil {
			t.Errorf("bad range %q", rangeHeader)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Length", fmt.Sprint(end-start+1))
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(upstreamData)))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(upstreamData[start : end+1])
	}))
	defer upstream.Close()

	gateway, err := newStreamGateway(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer gateway.Close()

	source := streamSource{urls: []string{upstream.URL}, mimeType: "video/mp4"}
	session := &streamSession{
		id:             "test-session",
		video:          &streamTrack{source: source, initData: []byte("init")},
		masterPlaylist: "#EXTM3U\n# master\n",
		videoPlaylist:  "#EXTM3U\n# video\n",
		expiresAt:      time.Now().Add(time.Hour),
	}
	gateway.sessions[session.id] = session

	base := gateway.baseURL + "/s/" + session.id + "/"
	for path, want := range map[string]string{
		"master.m3u8":    "# master",
		"video.m3u8":     "# video",
		"video-init.mp4": "init",
	} {
		resp, err := http.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || !bytes.Contains(body, []byte(want)) {
			t.Fatalf("%s: status %d body %q", path, resp.StatusCode, body)
		}
	}

	req, _ := http.NewRequest(http.MethodGet, base+"video.mp4", nil)
	req.Header.Set("Range", "bytes=2-5")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent || string(body) != "2345" {
		t.Fatalf("range: status %d body %q", resp.StatusCode, body)
	}
	if got := resp.Header.Get("Content-Range"); got != "bytes 2-5/16" {
		t.Fatalf("Content-Range = %q", got)
	}
	if resp.Header.Get("Content-Type") != "video/mp4" {
		t.Fatalf("Content-Type = %q", resp.Header.Get("Content-Type"))
	}

	gateway.stop(session.id)
	resp, err = http.Get(base + "master.m3u8")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("stopped session status = %d", resp.StatusCode)
	}
}

func TestFetchRangeFallsBackToBackup(t *testing.T) {
	data := []byte("abcdefghij")
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "4")
		w.Header().Set("Content-Range", "bytes 2-5/10")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(data[2:6])
	}))
	defer good.Close()
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer bad.Close()

	gateway, err := newStreamGateway(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer gateway.Close()
	got, err := gateway.fetchRange(context.Background(), []string{bad.URL, good.URL}, 2, 5)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "cdef" {
		t.Fatalf("got %q", got)
	}
}

func TestDownloadGatewayServesLocalFiles(t *testing.T) {
	dir := t.TempDir()
	id := "0123456789abcdef"
	jobDir := filepath.Join(dir, id)
	if err := os.MkdirAll(jobDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(jobDir, "master.m3u8"), []byte("#EXTM3U\n"), 0o644); err != nil {
		t.Fatalf("write playlist: %v", err)
	}
	data := []byte("0123456789")
	if err := os.WriteFile(filepath.Join(jobDir, "video.mp4"), data, 0o644); err != nil {
		t.Fatalf("write video: %v", err)
	}

	gateway, err := newStreamGateway(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("newStreamGateway: %v", err)
	}
	gateway.downloadDir = dir
	defer gateway.Close()

	resp, err := http.Get(gateway.baseURL + "/d/" + id + "/master.m3u8")
	if err != nil {
		t.Fatalf("get playlist: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "#EXTM3U\n" {
		t.Fatalf("playlist = %d %q", resp.StatusCode, body)
	}

	req, err := http.NewRequest(http.MethodGet, gateway.baseURL+"/d/"+id+"/video.mp4", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Range", "bytes=2-5")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("range request: %v", err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent || string(body) != "2345" {
		t.Fatalf("range response = %d %q", resp.StatusCode, body)
	}

	for _, path := range []string{
		"/d/not-a-valid-id/master.m3u8",
		"/d/ffffffffffffffff/master.m3u8",
		"/d/" + id + "/missing.mp4",
	} {
		resp, err := http.Get(gateway.baseURL + path)
		if err != nil {
			t.Fatalf("get %s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("%s = %d, want 404", path, resp.StatusCode)
		}
	}
}
