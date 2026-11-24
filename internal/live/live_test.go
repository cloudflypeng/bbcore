package live

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"testing"
)

func TestMakePacketHeader(t *testing.T) {
	packet := makePacket(1, opAuth, 1, []byte("hello"))
	if len(packet) != 16+5 {
		t.Fatalf("packet length = %d", len(packet))
	}
	if got := binary.BigEndian.Uint32(packet[0:4]); got != 21 {
		t.Errorf("packet length field = %d, want 21", got)
	}
	if got := binary.BigEndian.Uint16(packet[4:6]); got != 16 {
		t.Errorf("header length = %d, want 16", got)
	}
	if got := binary.BigEndian.Uint16(packet[6:8]); got != 1 {
		t.Errorf("protocol version = %d, want 1", got)
	}
	if got := binary.BigEndian.Uint32(packet[8:12]); got != opAuth {
		t.Errorf("opcode = %d, want %d", got, opAuth)
	}
	if got := binary.BigEndian.Uint32(packet[12:16]); got != 1 {
		t.Errorf("sequence = %d, want 1", got)
	}
	if !bytes.Equal(packet[16:], []byte("hello")) {
		t.Errorf("body = %q", packet[16:])
	}
}

func TestZlibRoundTrip(t *testing.T) {
	payload := makePacket(0, opMessage, 1, []byte(`{"cmd":"DANMU_MSG"}`))
	var buf bytes.Buffer
	w := zlib.NewWriter(&buf)
	if _, err := w.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	out, err := zlibDecompress(buf.Bytes())
	if err != nil {
		t.Fatalf("zlibDecompress: %v", err)
	}
	if !bytes.Equal(out, payload) {
		t.Fatalf("round trip mismatch")
	}
}

func TestBrotliDecompressRejectsGarbage(t *testing.T) {
	if got := brotliDecompress([]byte("not brotli")); len(got) != 0 {
		t.Fatalf("expected empty output for garbage, got %d bytes", len(got))
	}
}

func TestSummarizeDanmaku(t *testing.T) {
	first := make([]any, 16)
	first[2] = 25
	first[3] = 16777215
	first[9] = 1
	first[15] = map[string]any{
		"extra": `{"id_str":"2248799144733219072","dm_type":1}`,
		"user": map[string]any{
			"uid":  42,
			"base": map[string]any{"name": "bob"},
		},
	}
	data := map[string]any{
		"cmd":  "DANMU_MSG:4:0:2:2:2:0",
		"info": []any{first, "hello", []any{42, "bob"}},
	}
	summary, ok := summarizeDanmaku(data)
	if !ok {
		t.Fatal("summarizeDanmaku returned false")
	}
	if summary["text"] != "hello" || summary["uname"] != "bob" {
		t.Fatalf("summary = %v", summary)
	}
	if summary["uid"] != 42 {
		t.Fatalf("uid = %v", summary["uid"])
	}
	if summary["id"] != "2248799144733219072" || summary["dm_type"] != float64(1) {
		t.Fatalf("id/dm_type = %v/%v", summary["id"], summary["dm_type"])
	}
	if summary["fontsize"] != 25 || summary["color"] != 16777215 || summary["mode"] != 1 {
		t.Fatalf("style fields = %v", summary)
	}
}
