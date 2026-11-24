package service

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

func stringField(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case json.Number:
		return t.String()
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	default:
		return fmt.Sprint(v)
	}
}

func numberField(m map[string]any, key string) int64 {
	if m == nil {
		return 0
	}
	return toInt64(m[key])
}

func toInt64(v any) int64 {
	switch t := v.(type) {
	case json.Number:
		n, _ := t.Int64()
		return n
	case float64:
		return int64(t)
	case int:
		return int64(t)
	case int64:
		return t
	case string:
		n, _ := strconv.ParseInt(t, 10, 64)
		return n
	}
	return 0
}

func toInt64Map(m map[string]string, key string) int64 {
	if m == nil {
		return 0
	}
	n, _ := strconv.ParseInt(m[key], 10, 64)
	return n
}

func values(kv map[string]any) url.Values {
	out := url.Values{}
	for k, v := range kv {
		if v == nil {
			continue
		}
		out.Set(k, stringify(v))
	}
	return out
}

func joinList(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case []any:
		parts := make([]string, 0, len(t))
		for _, item := range t {
			if s := stringify(item); s != "" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, ",")
	default:
		return stringify(v)
	}
}
