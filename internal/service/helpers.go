package service

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"net/url"

	"github.com/cloudflypeng/bbcore/internal/jsonrpc"
)

// randomB64 mirrors PiliPlus Utils.base64EncodeRandomString: random bytes,
// standard base64, trailing two characters dropped.
func randomB64(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		for i := range buf {
			buf[i] = byte(i * 31)
		}
	}
	encoded := base64.StdEncoding.EncodeToString(buf)
	if len(encoded) > 2 {
		return encoded[:len(encoded)-2]
	}
	return encoded
}

func (s *Service) csrfFor(account string) string {
	cred, err := s.credential(account)
	if err != nil {
		return ""
	}
	return cred.CSRF()
}

// resolveAid converts a bvid to aid using the video detail endpoint.
func (s *Service) resolveAid(ctx context.Context, account, bvid string, aid int64) (int64, *jsonrpc.Error) {
	if aid > 0 {
		return aid, nil
	}
	if bvid == "" {
		return 0, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "bvid or aid is required")
	}
	result, err := s.endpointRequest(ctx, "videoIntro", requestOptions{
		account: account,
		query:   url.Values{"bvid": {bvid}},
	})
	if err != nil {
		return 0, err
	}
	m, _ := result.(map[string]any)
	if code := numberField(m, "code"); code != 0 {
		return 0, jsonrpc.Errorf(jsonrpc.CodeInternalError, "resolve bvid %s: %s (code %d)", bvid, stringField(m, "message"), code)
	}
	data, _ := m["data"].(map[string]any)
	id := numberField(data, "aid")
	if id <= 0 {
		return 0, jsonrpc.Errorf(jsonrpc.CodeInternalError, "resolve bvid %s: no aid in response", bvid)
	}
	return id, nil
}
