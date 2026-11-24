package bili

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Config wires the client into the process.
type Config struct {
	// CacheDir stores the daily WBI mixin key.
	CacheDir string
	// Timeout is the per-request timeout (default 30s).
	Timeout time.Duration
	// OnCookies receives Set-Cookie values from every response. mid is 0 for
	// the anonymous jar.
	OnCookies func(mid int64, cookies []*http.Cookie)
	// Anon returns the current anonymous credential (buvid cookies).
	Anon func() *Credential
	// Logger receives debug output (stderr).
	Logger *slog.Logger
	// Debug logs every request URL and response code.
	Debug bool
}

// Client is the Bilibili HTTP transport shared by all RPC methods.
type Client struct {
	cfg  Config
	http *http.Client
	log  *slog.Logger
	wbi  *wbiCache
}

// NewClient builds a client. Transport keeps default proxy/HTTP2 behavior.
func NewClient(cfg Config) *Client {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultHTTPTimeout
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	}
	return &Client{
		cfg:  cfg,
		log:  logger,
		http: &http.Client{Timeout: timeout},
		wbi:  &wbiCache{path: filepath.Join(cfg.CacheDir, "wbi.json"), log: logger},
	}
}

// AnonCredential returns the anonymous credential, creating an empty one when
// no provider is configured.
func (c *Client) AnonCredential() *Credential {
	if c.cfg.Anon != nil {
		if cred := c.cfg.Anon(); cred != nil {
			return cred
		}
	}
	return AnonymousCredential()
}

// EnsureBuvid bootstraps buvid3/buvid4/b_nut cookies on the anonymous jar if
// they are missing. Safe to call repeatedly.
func (c *Client) EnsureBuvid(ctx context.Context) error {
	anon := c.AnonCredential()
	if anon.Cookie("buvid3") != "" {
		return nil
	}
	result, err := c.Do(ctx, Spec{
		Base:       BaseAPI,
		Method:     http.MethodGet,
		Path:       AnonymousBuvidEndpoint,
		Sign:       SignNone,
		Credential: anon,
	})
	if err != nil {
		return fmt.Errorf("buvid bootstrap: %w", err)
	}
	data, _ := result.JSON.(map[string]any)
	payload, _ := data["data"].(map[string]any)
	b3 := stringField(payload, "b_3")
	b4 := stringField(payload, "b_4")
	if b3 == "" {
		// Fall back to a locally generated buvid so requests still work.
		b3 = "XY" + strings.ToUpper(fmt.Sprintf("%x", time.Now().UnixNano()))
		if len(b3) < 35 {
			b3 = (b3 + "0000000000000000000000000000000000")[:35]
		}
	}
	cookies := map[string]string{
		"buvid3": b3,
		"b_nut":  fmt.Sprintf("%d", time.Now().Unix()),
	}
	if b4 != "" {
		cookies["buvid4"] = b4
	}
	c.applyCookies(c.anonMid(), cookies)
	return nil
}

// mergeAnonCookies fills missing buvid-related cookies on a credential.
func (c *Client) mergeAnonCookies(cred *Credential) *Credential {
	if cred == nil {
		return c.AnonCredential()
	}
	if !cred.LoggedIn {
		return cred.WithFallbackCookies(nil)
	}
	return cred.WithFallbackCookies(c.AnonCredential().Cookies)
}

func (c *Client) anonMid() int64 { return 0 }

// applyCookies hands new cookie values to the persistence callback. A nil
// cookie value means deletion.
func (c *Client) applyCookies(mid int64, values map[string]string) {
	if c.cfg.OnCookies == nil || len(values) == 0 {
		return
	}
	now := time.Now()
	cookies := make([]*http.Cookie, 0, len(values))
	for k, v := range values {
		cookie := &http.Cookie{Name: k, Value: v, Path: "/", Domain: ".bilibili.com"}
		if v == "" {
			cookie.MaxAge = -1
			cookie.Expires = now.Add(-time.Hour)
		}
		cookies = append(cookies, cookie)
	}
	c.cfg.OnCookies(mid, cookies)
}

func stringField(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	switch v := m[key].(type) {
	case string:
		return v
	case json.Number:
		return v.String()
	case float64:
		return fmt.Sprintf("%v", v)
	}
	return ""
}

func numberField(m map[string]any, key string) int64 {
	if m == nil {
		return 0
	}
	switch v := m[key].(type) {
	case json.Number:
		n, _ := v.Int64()
		return n
	case float64:
		return int64(v)
	case int64:
		return v
	case string:
		var n int64
		fmt.Sscan(v, &n)
		return n
	}
	return 0
}

// mixinKey returns the current WBI mixin key, fetching it at most once per day.
type wbiCache struct {
	mu    sync.Mutex
	mixin string
	day   string
	path  string
	log   *slog.Logger
}

type wbiFile struct {
	Day   string `json:"day"`
	Mixin string `json:"mixin"`
}

func (w *wbiCache) key(ctx context.Context, c *Client) (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	today := time.Now().Format("2006-01-02")
	if w.mixin != "" && w.day == today {
		return w.mixin, nil
	}
	if w.mixin == "" && w.path != "" {
		if raw, err := os.ReadFile(w.path); err == nil {
			var f wbiFile
			if json.Unmarshal(raw, &f) == nil && f.Day == today && f.Mixin != "" {
				w.mixin, w.day = f.Mixin, f.Day
				return w.mixin, nil
			}
		}
	}
	mixin, err := w.fetch(ctx, c)
	if err != nil {
		return "", err
	}
	w.mixin, w.day = mixin, today
	if w.path != "" {
		if raw, err := json.Marshal(wbiFile{Day: today, Mixin: mixin}); err == nil {
			_ = os.MkdirAll(filepath.Dir(w.path), 0o755)
			_ = os.WriteFile(w.path, raw, 0o600)
		}
	}
	return mixin, nil
}

func (w *wbiCache) fetch(ctx context.Context, c *Client) (string, error) {
	anon := c.AnonCredential()
	result, err := c.Do(ctx, Spec{
		Base:       BaseAPI,
		Method:     http.MethodGet,
		Path:       NavEndpoint,
		Sign:       SignNone,
		Credential: anon,
	})
	if err != nil {
		return "", err
	}
	root, _ := result.JSON.(map[string]any)
	data, _ := root["data"].(map[string]any)
	wbiImg, _ := data["wbi_img"].(map[string]any)
	imgURL := stringField(wbiImg, "img_url")
	subURL := stringField(wbiImg, "sub_url")
	if imgURL == "" || subURL == "" {
		return "", fmt.Errorf("nav response missing wbi_img (code=%v message=%v)", root["code"], root["message"])
	}
	mixin := mixinKey(fileBase(imgURL) + fileBase(subURL))
	if mixin == "" {
		return "", fmt.Errorf("invalid wbi keys: %s %s", imgURL, subURL)
	}
	return mixin, nil
}

// fileBase returns the base name without extension, mirroring Utils.getFileName.
func fileBase(raw string) string {
	name := raw
	if i := strings.LastIndexAny(name, "/\\"); i >= 0 {
		name = name[i+1:]
	}
	for _, suffix := range []string{"?x-oss-process=image/resize,w_100", "?x-oss-process=image/resize,m_fill,w_100", "?x-oss-process=image/resize,w_100/format,png"} {
		name = strings.TrimSuffix(name, suffix)
	}
	if i := strings.LastIndex(name, "?"); i >= 0 {
		name = name[:i]
	}
	if i := strings.LastIndex(name, "."); i >= 0 {
		name = name[:i]
	}
	return name
}
