package bili

import (
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestEncodeComponent(t *testing.T) {
	cases := map[string]string{
		"BV1xx411c7mD": "BV1xx411c7mD",
		"a b":          "a%20b",
		"!~*'()":       "!~*'()",
		"中文":           "%E4%B8%AD%E6%96%87",
		"a+b&c=d":      "a%2Bb%26c%3Dd",
	}
	for in, want := range cases {
		if got := encodeComponent(in); got != want {
			t.Errorf("encodeComponent(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEncodeQuerySorted(t *testing.T) {
	params := url.Values{"zoo": {"1"}, "bar": {"514"}, "foo": {"a b", "c"}}
	got := encodeQuery(params)
	want := "bar=514&foo=a%20b&foo=c&zoo=1"
	if got != want {
		t.Errorf("encodeQuery = %q, want %q", got, want)
	}
}

func TestMixinKey(t *testing.T) {
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	if got, want := mixinKey(alphabet), "UVsc1ixGpYkF6dTJBRfXHjQtDCoNmMPn"; got != want {
		t.Errorf("mixinKey = %q, want %q", got, want)
	}
	if mixinKey("short") != "" {
		t.Error("mixinKey should reject keys shorter than the permutation table")
	}
}

func TestAppSign(t *testing.T) {
	timeNow = func() time.Time { return time.Unix(1700000000, 0) }
	defer func() { timeNow = time.Now }()

	params := url.Values{"foo": {"114"}, "bar": {"514"}}
	appSign(params)
	if params.Get("ts") != "1700000000" {
		t.Fatalf("ts = %q", params.Get("ts"))
	}
	if params.Get("appkey") != AppKey {
		t.Fatalf("appkey = %q", params.Get("appkey"))
	}
	canonical := "appkey=" + encodeComponent(AppKey) + "&bar=514&foo=114&ts=1700000000"
	if got, want := params.Get("sign"), md5Hex(canonical+AppSec); got != want {
		t.Errorf("sign = %q, want %q", got, want)
	}
}

func TestWBISign(t *testing.T) {
	timeNow = func() time.Time { return time.Unix(1700000000, 0) }
	defer func() { timeNow = time.Now }()

	const mixin = "UVsc1ixGpYkF6dTJBRfXHjQtDCoNmMPn"
	params := url.Values{"foo": {"114"}, "bar": {"514"}, "zoo": {"1919810"}}
	wbiSign(params, mixin)

	canonical := "bar=514&foo=114&wts=1700000000&zoo=1919810"
	if got, want := params.Get("w_rid"), md5Hex(canonical+mixin); got != want {
		t.Errorf("w_rid = %q, want %q", got, want)
	}
}

func TestWBISignStripsFilterCharsFromSignatureOnly(t *testing.T) {
	timeNow = func() time.Time { return time.Unix(1700000000, 0) }
	defer func() { timeNow = time.Now }()

	params := url.Values{"q": {"a!b'c(d)e*f"}}
	wbiSign(params, "mixin")
	if params.Get("q") != "a!b'c(d)e*f" {
		t.Errorf("q was mutated: %q", params.Get("q"))
	}
	canonical := "q=abcdef&wts=1700000000"
	if got, want := params.Get("w_rid"), md5Hex(canonical+"mixin"); got != want {
		t.Errorf("w_rid = %q, want %q", got, want)
	}
}

func TestEndpointRegistry(t *testing.T) {
	if len(EndpointList) != 315 {
		t.Fatalf("EndpointList has %d entries, want 315", len(EndpointList))
	}
	if len(Endpoints) != len(EndpointList) {
		t.Fatalf("Endpoints map has %d entries, list has %d", len(Endpoints), len(EndpointList))
	}
	for _, ep := range EndpointList {
		if ep.Name == "" || ep.Path == "" || ep.URL == "" {
			t.Errorf("incomplete endpoint: %+v", ep)
		}
		if ep.Base == "url" {
			if !strings.HasPrefix(ep.Path, "http") {
				t.Errorf("endpoint %s uses absolute base but path is %q", ep.Name, ep.Path)
			}
			continue
		}
		if _, ok := baseURLs[ep.Base]; !ok {
			t.Errorf("endpoint %s uses unknown base %q", ep.Name, ep.Base)
		}
		if _, ok := Endpoints[ep.Name]; !ok {
			t.Errorf("endpoint %s missing from map", ep.Name)
		}
	}
}

func TestAuroraEID(t *testing.T) {
	if AuroraEID(0) != "" {
		t.Error("AuroraEID(0) should be empty")
	}
	if enc := AuroraEID(123456); enc == "" || enc == "123456" {
		t.Errorf("AuroraEID did not encode: %q", enc)
	}
}
