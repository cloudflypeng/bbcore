package bili

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/andybalholm/brotli"
)

// Signature modes used by Spec.Sign / endpoint defaults.
const (
	SignNone = "none"
	SignWBI  = "wbi"
	SignApp  = "app"
)

// Spec describes one outgoing request.
type Spec struct {
	// Base selects the base URL when Path is not absolute.
	Base string
	// Method defaults to GET.
	Method string
	// Path is a path ("/x/...") or an absolute URL.
	Path string
	// Query is merged into the URL query. It is also what WBI signs.
	Query url.Values
	// Form is sent as application/x-www-form-urlencoded when set.
	Form url.Values
	// JSON is sent as an application/json body when set (and Form is nil).
	JSON any
	// Body is sent verbatim when set (takes precedence over Form/JSON).
	Body []byte
	// ContentType overrides the inferred content type.
	ContentType string
	// Sign is one of SignNone, SignWBI, SignApp. Empty means none.
	Sign string
	// Headers override generated headers.
	Headers map[string]string
	// Credential is the account snapshot for cookies/access_key. Nil means
	// anonymous.
	Credential *Credential
	// NoCookies suppresses the Cookie header.
	NoCookies bool
	// SkipAccessKey omits access_key from app-signed requests.
	SkipAccessKey bool
}

// Result is a completed upstream response.
type Result struct {
	Status      int         `json:"status"`
	Header      http.Header `json:"-"`
	JSON        any         `json:"-"`
	Body        []byte      `json:"-"`
	ContentType string      `json:"content_type,omitempty"`
	FinalURL    string      `json:"-"`
}

// Do executes a request and returns the decoded response.
func (c *Client) Do(ctx context.Context, spec Spec) (*Result, error) {
	if spec.Method == "" {
		spec.Method = http.MethodGet
	}
	spec.Method = strings.ToUpper(spec.Method)

	cred := spec.Credential
	if cred == nil {
		cred = c.AnonCredential()
	}
	cred = c.mergeAnonCookies(cred)

	if err := c.applySigning(ctx, &spec, cred); err != nil {
		return nil, err
	}

	target, err := buildURL(spec)
	if err != nil {
		return nil, err
	}

	var bodyReader io.Reader
	contentType := spec.ContentType
	switch {
	case spec.Body != nil:
		bodyReader = bytes.NewReader(spec.Body)
	case spec.Form != nil:
		encoded := encodeQuery(spec.Form)
		bodyReader = strings.NewReader(encoded)
		if contentType == "" {
			contentType = "application/x-www-form-urlencoded"
		}
	case spec.JSON != nil:
		raw, err := json.Marshal(spec.JSON)
		if err != nil {
			return nil, fmt.Errorf("encode json body: %w", err)
		}
		bodyReader = bytes.NewReader(raw)
		if contentType == "" {
			contentType = "application/json"
		}
	}

	req, err := http.NewRequestWithContext(ctx, spec.Method, target.String(), bodyReader)
	if err != nil {
		return nil, err
	}

	applyHeaders(req, spec, cred, contentType)

	if c.cfg.Debug && c.log != nil {
		c.log.Debug("request", "method", spec.Method, "url", target.String())
	}

	started := time.Now()
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", spec.Method, target.Redacted(), err)
	}
	defer resp.Body.Close()

	var reader io.Reader = io.LimitReader(resp.Body, maxResponseBytes)
	if strings.Contains(strings.ToLower(resp.Header.Get("Content-Encoding")), "br") {
		reader = brotli.NewReader(reader)
	}
	raw, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if c.cfg.Debug && c.log != nil {
		c.log.Debug("response", "status", resp.StatusCode, "bytes", len(raw), "elapsed", time.Since(started).String())
	}

	if cookies := resp.Cookies(); len(cookies) > 0 && c.cfg.OnCookies != nil {
		c.cfg.OnCookies(cred.Mid, cookies)
	}

	result := &Result{
		Status:      resp.StatusCode,
		Header:      resp.Header,
		Body:        raw,
		ContentType: resp.Header.Get("Content-Type"),
		FinalURL:    resp.Request.URL.String(),
	}
	if decoded, ok := decodeJSON(raw); ok {
		result.JSON = decoded
	}
	return result, nil
}

func (c *Client) applySigning(ctx context.Context, spec *Spec, cred *Credential) error {
	switch spec.Sign {
	case SignApp:
		carrier := spec.Form
		if carrier == nil {
			if spec.Query == nil {
				spec.Query = url.Values{}
			}
			carrier = spec.Query
		}
		if cred.LoggedIn && cred.AccessKey != "" && !spec.SkipAccessKey {
			carrier.Set("access_key", cred.AccessKey)
		}
		appSign(carrier)
	case SignWBI:
		if spec.Query == nil {
			spec.Query = url.Values{}
		}
		mixin, err := c.wbi.key(ctx, c)
		if err != nil {
			// Signing is additive; an unsigned request is still valid for the
			// many endpoints that do not enforce WBI.
			if c.log != nil {
				c.log.Warn("wbi signing unavailable, sending unsigned", "error", err)
			}
			return nil
		}
		wbiSign(spec.Query, mixin)
	}
	if spec.Form != nil && spec.Method == http.MethodPost &&
		cred.LoggedIn && cred.CSRF() != "" && !spec.Form.Has("csrf") &&
		spec.Base != BaseApp && spec.Base != BasePassport {
		spec.Form.Set("csrf", cred.CSRF())
	}
	return nil
}

func buildURL(spec Spec) (*url.URL, error) {
	raw := spec.Path
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		base := baseURLs[spec.Base]
		if base == "" {
			base = baseURLs[BaseAPI]
		}
		raw = strings.TrimRight(base, "/") + "/" + strings.TrimLeft(raw, "/")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid url %q: %w", raw, err)
	}
	merged := u.Query()
	for k, values := range spec.Query {
		for _, v := range values {
			merged.Add(k, v)
		}
	}
	u.RawQuery = encodeQuery(merged)
	return u, nil
}

func applyHeaders(req *http.Request, spec Spec, cred *Credential, contentType string) {
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("env", "prod")
	req.Header.Set("app-key", appKeyHdr)
	req.Header.Set("x-bili-aurora-zone", auroraZone)
	// No default Referer: the app UA paired with a web Referer trips risk
	// control (HTTP 412) on some web endpoints such as playInfo.
	if cred != nil && cred.LoggedIn {
		req.Header.Set("x-bili-mid", fmt.Sprintf("%d", cred.Mid))
		req.Header.Set("x-bili-aurora-eid", AuroraEID(cred.Mid))
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if !spec.NoCookies && spec.Base != BaseApp {
		if cookie := cred.CookieHeader(); cookie != "" {
			req.Header.Set("Cookie", cookie)
		}
	}
	for k, v := range spec.Headers {
		req.Header.Set(k, v)
	}
}

func decodeJSON(raw []byte) (any, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, false
	}
	if trimmed[0] != '{' && trimmed[0] != '[' {
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()
	var out any
	if err := dec.Decode(&out); err != nil {
		return nil, false
	}
	return out, true
}

// JSONMap returns the decoded body as an object (nil when not an object).
func (r *Result) JSONMap() map[string]any {
	if m, ok := r.JSON.(map[string]any); ok {
		return m
	}
	return nil
}

// BiliCode returns the upstream "code" field.
func (r *Result) BiliCode() int64 {
	return numberField(r.JSONMap(), "code")
}

// BiliMessage returns the upstream "message" field.
func (r *Result) BiliMessage() string {
	return stringField(r.JSONMap(), "message")
}
