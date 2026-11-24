package bili

import (
	"encoding/base64"
	"sort"
	"strconv"
	"strings"
)

// Credential is an immutable snapshot of the authentication state used for one
// request. LoggedIn=false means the anonymous cookie jar.
type Credential struct {
	LoggedIn  bool
	Mid       int64
	Name      string
	AccessKey string
	Cookies   map[string]string
}

// AnonymousCredential returns an empty anonymous credential.
func AnonymousCredential() *Credential {
	return &Credential{Cookies: map[string]string{}}
}

func (c *Credential) Cookie(name string) string {
	if c == nil {
		return ""
	}
	return c.Cookies[name]
}

// CSRF returns the bili_jct cookie used by web POST endpoints.
func (c *Credential) CSRF() string { return c.Cookie("bili_jct") }

// Buvid returns buvid3, the device id required by most app endpoints.
func (c *Credential) Buvid() string { return c.Cookie("buvid3") }

// Clone returns a deep copy.
func (c *Credential) Clone() *Credential {
	if c == nil {
		return AnonymousCredential()
	}
	cookies := make(map[string]string, len(c.Cookies))
	for k, v := range c.Cookies {
		cookies[k] = v
	}
	clone := *c
	clone.Cookies = cookies
	return &clone
}

// WithFallbackCookies fills missing cookies from other (e.g. anonymous buvid).
func (c *Credential) WithFallbackCookies(other map[string]string) *Credential {
	clone := c.Clone()
	for k, v := range other {
		if clone.Cookies[k] == "" {
			clone.Cookies[k] = v
		}
	}
	return clone
}

// CookieHeader renders the cookie header, determining a stable order.
func (c *Credential) CookieHeader() string {
	if c == nil || len(c.Cookies) == 0 {
		return ""
	}
	keys := make([]string, 0, len(c.Cookies))
	for k := range c.Cookies {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteString("; ")
		}
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(c.Cookies[k])
	}
	return b.String()
}

// AuroraEID derives the x-bili-aurora-eid header value from a mid.
func AuroraEID(mid int64) string {
	if mid == 0 {
		return ""
	}
	raw := []byte(strconv.FormatInt(mid, 10))
	for i := range raw {
		raw[i] ^= AuroraKey[i%len(AuroraKey)]
	}
	return strings.TrimRight(base64.StdEncoding.EncodeToString(raw), "=")
}
