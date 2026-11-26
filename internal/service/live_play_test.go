package service

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func testGateway(t *testing.T, hosts []string) *streamGateway {
	t.Helper()
	gateway, err := newStreamGateway(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("newStreamGateway: %v", err)
	}
	gateway.liveHosts = hosts
	t.Cleanup(gateway.Close)
	return gateway
}

func TestLiveURLAllowed(t *testing.T) {
	gateway := &streamGateway{liveHosts: defaultLiveHosts}
	tests := []struct {
		url string
		ok  bool
	}{
		{"https://cn-hnzz-cm-01-03.bilivideo.com/live/x/index.m3u8", true},
		{"https://d1--cn-gotcha104b.bilivideo.com/live/x.ts?expires=1", true},
		{"https://i0.hdslb.com/bfs/live/a.jpg", true},
		{"http://127.0.0.1:8080/secret", false},
		{"https://evilbilivideo.com/x", false},
		{"file:///etc/passwd", false},
		{"", false},
	}
	for _, test := range tests {
		if got := gateway.liveURLAllowed(test.url); got != test.ok {
			t.Fatalf("%q: got %v want %v", test.url, got, test.ok)
		}
	}
}

func TestRewriteLivePlaylist(t *testing.T) {
	gateway := &streamGateway{liveHosts: defaultLiveHosts}
	base, err := url.Parse("https://cn-hnzz-cm-01-03.bilivideo.com/live/x/index.m3u8?expires=1")
	if err != nil {
		t.Fatal(err)
	}
	playlist := strings.Join([]string{
		"#EXTM3U",
		`#EXT-X-MAP:URI="init.m4s"`,
		"#EXTINF:1.00,",
		"seg-1.m4s",
		"#EXTINF:1.00,",
		"https://d1--cn-gotcha104b.bilivideo.com/live/x/seg-2.m4s?token=1",
		"#EXTINF:1.00,",
		"http://127.0.0.1/evil.m4s",
		"#EXT-X-ENDLIST",
	}, "\n")

	rewritten := gateway.rewriteLivePlaylist(playlist, base, "abc123")
	wantInit := `/l/abc123/seg?u=https%3A%2F%2Fcn-hnzz-cm-01-03.bilivideo.com%2Flive%2Fx%2Finit.m4s`
	if !strings.Contains(rewritten, wantInit) {
		t.Fatalf("init URI not rewritten:\n%s", rewritten)
	}
	wantSegment := `/l/abc123/seg?u=https%3A%2F%2Fd1--cn-gotcha104b.bilivideo.com%2Flive%2Fx%2Fseg-2.m4s%3Ftoken%3D1`
	if !strings.Contains(rewritten, wantSegment) {
		t.Fatalf("absolute segment not rewritten:\n%s", rewritten)
	}
	if !strings.Contains(rewritten, "http://127.0.0.1/evil.m4s") {
		t.Fatalf("disallowed segment should be left untouched:\n%s", rewritten)
	}
	if strings.Contains(rewritten, "/l/abc123/seg?u=http%3A%2F%2F127.0.0.1") {
		t.Fatalf("loopback segment was rewritten:\n%s", rewritten)
	}
}

func TestLiveGatewayServesPlaylistAndSegment(t *testing.T) {
	segment := []byte("live-segment-bytes")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/index.m3u8":
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = io.WriteString(w, "#EXTM3U\n#EXT-X-MAP:URI=\"init.m4s\"\n#EXTINF:1.0,\nseg-1.m4s\n")
		case "/seg-1.m4s":
			w.Header().Set("Content-Type", "video/iso.segment")
			_, _ = w.Write(segment)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	gateway := testGateway(t, []string{"127.0.0.1"})
	session, err := gateway.startLive(context.Background(), upstream.URL+"/index.m3u8")
	if err != nil {
		t.Fatalf("startLive: %v", err)
	}

	playlistResp, err := http.Get(gateway.baseURL + "/l/" + session.id + "/playlist.m3u8")
	if err != nil {
		t.Fatal(err)
	}
	playlistBody, _ := io.ReadAll(playlistResp.Body)
	playlistResp.Body.Close()
	if playlistResp.StatusCode != http.StatusOK {
		t.Fatalf("playlist status %d: %s", playlistResp.StatusCode, playlistBody)
	}
	rewritten := string(playlistBody)
	if !strings.Contains(rewritten, "/l/"+session.id+"/seg?u=") {
		t.Fatalf("playlist was not rewritten: %s", rewritten)
	}

	var segmentPath string
	for _, line := range strings.Split(rewritten, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "/l/") && strings.Contains(line, "seg-1.m4s") {
			segmentPath = line
			break
		}
	}
	if segmentPath == "" {
		t.Fatalf("no segment URL in playlist: %s", rewritten)
	}
	segmentResp, err := http.Get(gateway.baseURL + segmentPath)
	if err != nil {
		t.Fatal(err)
	}
	segmentBody, _ := io.ReadAll(segmentResp.Body)
	segmentResp.Body.Close()
	if segmentResp.StatusCode != http.StatusOK || string(segmentBody) != string(segment) {
		t.Fatalf("segment status %d body %q", segmentResp.StatusCode, segmentBody)
	}

	if !gateway.stop(session.id) {
		t.Fatal("stop should remove live session")
	}
	notFound, err := http.Get(gateway.baseURL + "/l/" + session.id + "/playlist.m3u8")
	if err != nil {
		t.Fatal(err)
	}
	notFound.Body.Close()
	if notFound.StatusCode != http.StatusNotFound {
		t.Fatalf("stopped session returned %d", notFound.StatusCode)
	}
}

func TestLiveSegmentRejectsForeignHost(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "#EXTM3U\n")
	}))
	defer upstream.Close()

	gateway := testGateway(t, []string{"127.0.0.1"})
	session, err := gateway.startLive(context.Background(), upstream.URL+"/index.m3u8")
	if err != nil {
		t.Fatalf("startLive: %v", err)
	}
	request := httptest.NewRequest(http.MethodGet, "/l/"+session.id+"/seg?u=http%3A%2F%2Fexample.com%2Fx.m4s", nil)
	recorder := httptest.NewRecorder()
	gateway.handleLive(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("got status %d", recorder.Code)
	}
}

func TestLiveHLSCandidatesRanking(t *testing.T) {
	streams := []livePlayStream{
		{ProtocolName: "http_stream", Format: []livePlayFormat{{
			FormatName: "flv",
			Codec: []livePlayCodec{{
				CodecName: "avc",
				CurrentQN: 10000,
				BaseURL:   "/flv",
				URLInfo:   []livePlayURLInfo{{Host: "https://a.bilivideo.com"}},
			}},
		}}},
		{ProtocolName: "http_hls", Format: []livePlayFormat{
			{
				FormatName: "ts",
				Codec: []livePlayCodec{{
					CodecName: "avc",
					CurrentQN: 10000,
					BaseURL:   "/ts.m3u8?",
					URLInfo:   []livePlayURLInfo{{Host: "https://b.bilivideo.com", Extra: "expires=1"}},
				}},
			},
			{
				FormatName: "fmp4",
				Codec: []livePlayCodec{{
					CodecName: "avc",
					CurrentQN: 250,
					BaseURL:   "/index.m3u8?",
					URLInfo:   []livePlayURLInfo{{Host: "https://c.bilivideo.com", Extra: "expires=2"}},
				}},
			},
		}},
	}

	candidates := liveHLSCandidates(streams, "", "")
	if len(candidates) != 2 {
		t.Fatalf("got %d candidates", len(candidates))
	}
	if candidates[0].qn != 10000 || candidates[0].format != "ts" {
		t.Fatalf("higher quality should come first: %+v", candidates[0])
	}
	if candidates[0].url != "https://b.bilivideo.com/ts.m3u8?expires=1" {
		t.Fatalf("unexpected url %q", candidates[0].url)
	}
	fmp4Only := liveHLSCandidates(streams, "fmp4", "")
	if len(fmp4Only) != 1 || fmp4Only[0].qn != 250 {
		t.Fatalf("format filter failed: %+v", fmp4Only)
	}

	mixed := []livePlayStream{{
		ProtocolName: "http_hls",
		Format: []livePlayFormat{{
			FormatName: "fmp4",
			Codec: []livePlayCodec{
				{CodecName: "av1", CurrentQN: 10000, BaseURL: "/av1.m3u8", URLInfo: []livePlayURLInfo{{Host: "https://a"}}},
				{CodecName: "avc", CurrentQN: 400, BaseURL: "/avc.m3u8", URLInfo: []livePlayURLInfo{{Host: "https://b"}}},
				{CodecName: "hevc", CurrentQN: 10000, BaseURL: "/hevc.m3u8", URLInfo: []livePlayURLInfo{{Host: "https://c"}}},
			},
		}},
	}}
	ranked := liveHLSCandidates(mixed, "", "")
	if len(ranked) != 3 || ranked[0].codec != "avc" || ranked[1].codec != "hevc" || ranked[2].codec != "av1" {
		t.Fatalf("codec ranking should prefer AVC over AV1 even at lower quality: %+v", ranked)
	}
	if ranked[0].qn != 400 || ranked[1].qn != 10000 {
		t.Fatalf("quality should sort within a codec: %+v", ranked)
	}
	hevc := liveHLSCandidates(mixed, "", "hevc")
	if len(hevc) != 1 || hevc[0].codec != "hevc" || hevc[0].qn != 10000 {
		t.Fatalf("codec filter failed: %+v", hevc)
	}
}

func TestLiveQualitiesMarksCurrent(t *testing.T) {
	desc := []liveQNDesc{{QN: 250, Desc: "超清"}, {QN: 10000, Desc: "原画"}}
	qualities := liveQualities(desc, 250)
	if len(qualities) != 2 || qualities[0]["qn"].(int) != 10000 || qualities[1]["qn"].(int) != 250 {
		t.Fatalf("unexpected qualities: %+v", qualities)
	}
	if qualities[1]["current"] != true || qualities[0]["current"] != false {
		t.Fatalf("current flag is wrong: %+v", qualities)
	}
	if liveQualityDesc(desc, 250) != "超清" || liveQualityDesc(nil, 400) != "蓝光" {
		t.Fatal("quality desc fallback failed")
	}
}
