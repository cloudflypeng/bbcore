package biligrpc

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/cloudflypeng/bbcore/internal/bili"
)

const grpcHost = "https://app.bilibili.com"

// Client calls Bilibili gRPC endpoints over HTTP/2 with protobuf payloads.
type Client struct {
	http *http.Client
	log  *slog.Logger
}

// New creates a gRPC client.
func New(logger *slog.Logger) *Client {
	if logger == nil {
		logger = slog.Default()
	}
	return &Client{http: &http.Client{Timeout: 30 * time.Second}, log: logger}
}

// CallSpec describes one unary gRPC call.
type CallSpec struct {
	// Path is the full gRPC path, e.g.
	// "/bilibili.community.service.dm.v1.DM/DmSegMobile".
	Path string
	// Payload is the protobuf-encoded request message.
	Payload []byte
	// Credential supplies buvid/access_key for the gRPC metadata headers.
	Credential *bili.Credential
	// Compress forces gzip compression of the payload (default: auto > 64B).
	Compress *bool
	// Headers overrides/adds headers.
	Headers map[string]string
}

// Result is a unary gRPC response.
type Result struct {
	Status      int         `json:"status"`
	GrpcStatus  int         `json:"grpc_status"`
	GrpcMessage string      `json:"grpc_message,omitempty"`
	Message     []byte      `json:"-"`
	MessageB64  string      `json:"message_base64,omitempty"`
	Header      http.Header `json:"-"`
	Trailers    http.Header `json:"-"`
}

// Call performs a unary gRPC request.
func (c *Client) Call(ctx context.Context, spec CallSpec) (*Result, error) {
	if !strings.HasPrefix(spec.Path, "/") {
		return nil, fmt.Errorf("grpc path must start with /: %q", spec.Path)
	}
	compress := len(spec.Payload) > 64
	if spec.Compress != nil {
		compress = *spec.Compress
	}
	body, err := frame(spec.Payload, compress)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, grpcHost+spec.Path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	cred := spec.Credential
	if cred == nil {
		cred = bili.AnonymousCredential()
	}
	for k, v := range Headers(cred) {
		req.Header.Set(k, v)
	}
	req.Header.Set("Content-Type", "application/grpc")
	req.Header.Set("TE", "trailers")
	req.Header.Set("grpc-encoding", "gzip")
	req.Header.Set("gzip-accept-encoding", "gzip,identity")
	for k, v := range spec.Headers {
		req.Header.Set(k, v)
	}
	if c.log != nil {
		c.log.Debug("grpc request", "path", spec.Path, "bytes", len(spec.Payload))
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("grpc %s: %w", spec.Path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, fmt.Errorf("read grpc body: %w", err)
	}

	result := &Result{
		Status:   resp.StatusCode,
		Header:   resp.Header,
		Trailers: resp.Trailer,
	}
	statusRaw := resp.Trailer.Get("Grpc-Status")
	if statusRaw == "" {
		statusRaw = resp.Header.Get("Grpc-Status")
	}
	if statusRaw != "" {
		fmt.Sscan(statusRaw, &result.GrpcStatus)
	}
	result.GrpcMessage = resp.Trailer.Get("Grpc-Message")
	if result.GrpcMessage == "" {
		result.GrpcMessage = resp.Header.Get("Grpc-Message")
	}
	if len(raw) >= 5 {
		flag := raw[0]
		length := int(binary.BigEndian.Uint32(raw[1:5]))
		if length <= len(raw)-5 {
			message := raw[5 : 5+length]
			if flag&1 == 1 {
				reader, gzErr := gzip.NewReader(bytes.NewReader(message))
				if gzErr != nil {
					return nil, fmt.Errorf("gunzip grpc response: %w", gzErr)
				}
				message, err = io.ReadAll(reader)
				_ = reader.Close()
				if err != nil {
					return nil, fmt.Errorf("read gzip grpc response: %w", err)
				}
			}
			result.Message = message
			result.MessageB64 = base64.StdEncoding.EncodeToString(message)
		}
	}
	if result.GrpcStatus != 0 && result.GrpcMessage != "" {
		result.GrpcMessage = decodeGrpcMessage(result.GrpcMessage)
	}
	return result, nil
}

// frame wraps a protobuf message in the gRPC 5-byte prefix.
func frame(payload []byte, compress bool) ([]byte, error) {
	message := payload
	if compress {
		var buf bytes.Buffer
		writer := gzip.NewWriter(&buf)
		if _, err := writer.Write(payload); err != nil {
			return nil, err
		}
		if err := writer.Close(); err != nil {
			return nil, err
		}
		message = buf.Bytes()
	}
	out := make([]byte, 5+len(message))
	if compress {
		out[0] = 1
	}
	binary.BigEndian.PutUint32(out[1:5], uint32(len(message)))
	copy(out[5:], message)
	return out, nil
}

func decodeGrpcMessage(msg string) string {
	// gRPC percent-encodes trailer messages.
	out := strings.Builder{}
	for i := 0; i < len(msg); i++ {
		if msg[i] == '%' && i+2 < len(msg) {
			var v int
			if _, err := fmt.Sscanf(msg[i+1:i+3], "%02x", &v); err == nil {
				out.WriteByte(byte(v))
				i += 2
				continue
			}
		}
		out.WriteByte(msg[i])
	}
	return out.String()
}

// Headers builds the Bilibili gRPC metadata headers from a credential,
// mirroring PiliPlus utils/accounts/grpc_headers.dart.
func Headers(cred *bili.Credential) map[string]string {
	accessKey := ""
	buvid := ""
	mid := int64(0)
	if cred != nil {
		accessKey = cred.AccessKey
		buvid = cred.Buvid()
		mid = cred.Mid
	}
	metadata := (&protoWriter{})
	metadata.String(1, accessKey)
	metadata.String(2, bili.GrpcMobiApp)
	metadata.String(3, bili.GrpcDevice)
	metadata.Int(4, 2001100)
	metadata.String(5, bili.GrpcChannel)
	metadata.String(6, buvid)
	metadata.String(7, bili.GrpcDevice)

	device := (&protoWriter{})
	device.Int(1, bili.GrpcAppID)
	device.Int(2, 2001100)
	device.String(3, buvid)
	device.String(4, bili.GrpcMobiApp)
	device.String(5, bili.GrpcDevice)
	device.String(6, bili.GrpcDevice)
	device.String(7, bili.GrpcChannel)
	device.String(8, bili.GrpcDevice)
	device.String(9, bili.GrpcDevice)
	device.String(10, bili.GrpcOSVersion)
	device.String(13, bili.GrpcVersionName)

	fawkes := (&protoWriter{})
	fawkes.String(1, bili.GrpcMobiApp)
	fawkes.String(2, "prod")
	fawkes.String(3, randomString(8))

	localeIds := (&protoWriter{})
	localeIds.String(1, "zh")
	localeIds.String(2, "Hans")
	localeIds.String(3, "CN")
	locale := (&protoWriter{})
	locale.Message(1, localeIds.Bytes())
	locale.Message(2, localeIds.Bytes())
	locale.String(4, "Asia/Shanghai")

	network := (&protoWriter{})
	network.Uint(1, 1) // NetworkType.WIFI

	headers := map[string]string{
		"user-agent":            bili.UserAgent,
		"x-bili-gaia-vtoken":    "",
		"x-bili-aurora-zone":    "",
		"x-bili-trace-id":       bili.TraceIDDefault,
		"bili-http-engine":      "cronet",
		"x-bili-network-bin":    base64.StdEncoding.EncodeToString(network.Bytes()),
		"x-bili-locale-bin":     base64.StdEncoding.EncodeToString(locale.Bytes()),
		"x-bili-exps-bin":       "",
		"x-bili-fawkes-req-bin": base64.StdEncoding.EncodeToString(fawkes.Bytes()),
		"x-bili-device-bin":     base64.StdEncoding.EncodeToString(device.Bytes()),
		"x-bili-metadata-bin":   base64.StdEncoding.EncodeToString(metadata.Bytes()),
		"buvid":                 buvid,
	}
	if accessKey != "" {
		headers["authorization"] = "identify_v1 " + accessKey
	}
	if mid != 0 {
		headers["x-bili-mid"] = fmt.Sprintf("%d", mid)
	}
	return headers
}

func randomString(n int) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "bbcore00"
	}
	for i := range buf {
		buf[i] = alphabet[int(buf[i])%len(alphabet)]
	}
	return string(buf[:n])
}
