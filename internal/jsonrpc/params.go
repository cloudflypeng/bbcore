package jsonrpc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// Params is a decoded JSON-RPC params object. Numbers stay as json.Number so
// large ids (mid/cid) survive round-trips.
type Params map[string]any

// ParseParams decodes params, accepting an object or a one-element array.
func ParseParams(raw json.RawMessage) (Params, *Error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return Params{}, nil
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err == nil {
		if m == nil {
			m = map[string]any{}
		}
		return Params(m), nil
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(trimmed, &arr); err == nil && len(arr) == 1 {
		return ParseParams(arr[0])
	}
	return nil, Errorf(CodeInvalidParams, "params must be a JSON object")
}

func (p Params) Has(key string) bool {
	_, ok := p[key]
	return ok
}

func (p Params) Raw(key string) (any, bool) {
	v, ok := p[key]
	return v, ok
}

// Value returns the raw value or nil.
func (p Params) Value(key string) any {
	v, _ := p[key]
	return v
}

func (p Params) String(key string) string {
	return p.StringDefault(key, "")
}

func (p Params) StringDefault(key, def string) string {
	v, ok := p[key]
	if !ok || v == nil {
		return def
	}
	switch t := v.(type) {
	case string:
		return t
	case json.Number:
		return t.String()
	case bool:
		return strconv.FormatBool(t)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		return fmt.Sprint(v)
	}
}

// ID returns a numeric id parameter as int64 (mid, aid, cid, room_id ...).
func (p Params) ID(key string) (int64, bool) {
	v, ok := p[key]
	if !ok || v == nil {
		return 0, false
	}
	switch t := v.(type) {
	case json.Number:
		n, err := t.Int64()
		if err != nil {
			f, ferr := t.Float64()
			if ferr != nil {
				return 0, false
			}
			return int64(f), true
		}
		return n, true
	case float64:
		return int64(t), true
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(t), 10, 64)
		if err != nil {
			return 0, false
		}
		return n, true
	case int64:
		return t, true
	case int:
		return int64(t), true
	default:
		return 0, false
	}
}

func (p Params) IntDefault(key string, def int64) int64 {
	if n, ok := p.ID(key); ok {
		return n
	}
	return def
}

func (p Params) Bool(key string, def bool) bool {
	v, ok := p[key]
	if !ok || v == nil {
		return def
	}
	switch t := v.(type) {
	case bool:
		return t
	case string:
		b, err := strconv.ParseBool(t)
		if err == nil {
			return b
		}
	case json.Number:
		n, err := t.Int64()
		if err == nil {
			return n != 0
		}
	case float64:
		return t != 0
	}
	return def
}

func (p Params) Map(key string) map[string]any {
	v, ok := p[key]
	if !ok {
		return nil
	}
	m, _ := v.(map[string]any)
	return m
}

// StringMap converts a JSON object of scalar values to a string map.
func (p Params) StringMap(key string) map[string]string {
	m := p.Map(key)
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		if v == nil {
			continue
		}
		out[k] = ScalarString(v)
	}
	return out
}

// Query converts the given param key (object) into url.Values. Array values
// become repeated keys.
func (p Params) Query(key string) url.Values {
	m := p.Map(key)
	if m == nil {
		return nil
	}
	values := url.Values{}
	for k, v := range m {
		switch t := v.(type) {
		case nil:
			continue
		case []any:
			for _, item := range t {
				values.Add(k, ScalarString(item))
			}
		case map[string]any:
			b, _ := json.Marshal(t)
			values.Add(k, string(b))
		default:
			values.Add(k, ScalarString(t))
		}
	}
	return values
}

// ScalarString renders a JSON scalar for use in a query string.
func ScalarString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case json.Number:
		return t.String()
	case bool:
		return strconv.FormatBool(t)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case nil:
		return ""
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return fmt.Sprint(t)
		}
		return string(b)
	}
}

// Object converts arbitrary JSON into Params semantics: nested maps are
// map[string]any, numbers are json.Number.
func Object(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return nil
}
