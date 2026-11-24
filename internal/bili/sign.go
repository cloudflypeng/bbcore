package bili

import (
	"crypto/md5"
	"encoding/hex"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// encodeComponent mirrors Dart's Uri.encodeComponent (JavaScript
// encodeURIComponent): unreserved characters plus !~*'() stay literal.
func encodeComponent(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9':
			b.WriteByte(c)
		case c == '-', c == '_', c == '.', c == '!', c == '~', c == '*', c == '\'', c == '(', c == ')':
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			const hexDigits = "0123456789ABCDEF"
			b.WriteByte(hexDigits[c>>4])
			b.WriteByte(hexDigits[c&0xf])
		}
	}
	return b.String()
}

// encodeQuery renders url.Values with RFC3986/encodeURIComponent escaping so
// that what is sent matches what is signed.
func encodeQuery(values url.Values) string {
	if len(values) == 0 {
		return ""
	}
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		for _, v := range values[k] {
			if b.Len() > 0 {
				b.WriteByte('&')
			}
			b.WriteString(encodeComponent(k))
			b.WriteByte('=')
			b.WriteString(encodeComponent(v))
		}
	}
	return b.String()
}

func md5Hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

// appSign adds appkey/ts/sign to params following PiliPlus utils/app_sign.dart.
func appSign(params url.Values) {
	params.Set("appkey", AppKey)
	params.Set("ts", strconv.FormatInt(timeNow().Unix(), 10))
	params.Del("sign")
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		for _, v := range params[k] {
			if b.Len() > 0 {
				b.WriteByte('&')
			}
			b.WriteString(encodeComponent(k))
			b.WriteByte('=')
			b.WriteString(encodeComponent(v))
		}
	}
	params.Set("sign", md5Hex(b.String()+AppSec))
}

// mixinKey permutes imgKey+subKey with the fixed table.
func mixinKey(orig string) string {
	out := make([]byte, 0, len(MixinKeyEncTab))
	for _, idx := range MixinKeyEncTab {
		if idx < 0 || idx >= len(orig) {
			return ""
		}
		out = append(out, orig[idx])
	}
	return string(out)
}

var wbiFilter = strings.NewReplacer("!", "", "'", "", "(", "", ")", "", "*", "")

// timeNow is replaceable in tests to pin wts/ts.
var timeNow = time.Now

// wbiSign adds wts/w_rid to params using the daily mixin key.
func wbiSign(params url.Values, mixin string) {
	params.Set("wts", strconv.FormatInt(timeNow().Unix(), 10))
	keys := make([]string, 0, len(params))
	for k := range params {
		if k == "w_rid" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		for _, v := range params[k] {
			if b.Len() > 0 {
				b.WriteByte('&')
			}
			b.WriteString(encodeComponent(k))
			b.WriteByte('=')
			b.WriteString(encodeComponent(wbiFilter.Replace(v)))
		}
	}
	params.Set("w_rid", md5Hex(b.String()+mixin))
}
