// Package peer implements the zero-config direct peering protocol,
// wire-compatible with the Python reference implementation (src/lanbus/peer.py).
package peer

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Canonical renders v exactly like Python's
// json.dumps(v, ensure_ascii=False, sort_keys=True, separators=(",", ":")).
// Numbers must be json.Number (or Go ints/floats via PyFloat) so the byte
// output round-trips through Python's parser without changing the literal.
func Canonical(v any) ([]byte, error) {
	var buf bytes.Buffer
	if err := encodeValue(&buf, v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ParseObject decodes a JSON object preserving number literals (json.Number),
// so re-encoding with Canonical reproduces the sender's canonical bytes.
func ParseObject(data []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("JSON 必须是对象")
	}
	return obj, nil
}

// PyFloat formats f exactly like Python's repr/json.dumps: integral values
// become ints, fixed-point shortest decimal for 1e-4 <= |f| < 1e16, and
// Python-style exponent notation outside that range.
func PyFloat(f float64) json.Number {
	if f == math.Trunc(f) && math.Abs(f) < 1e16 {
		return json.Number(strconv.FormatInt(int64(f), 10))
	}
	abs := math.Abs(f)
	if abs >= 1e-4 && abs < 1e16 {
		return json.Number(strconv.FormatFloat(f, 'f', -1, 64))
	}
	return json.Number(strconv.FormatFloat(f, 'e', -1, 64))
}

func encodeValue(buf *bytes.Buffer, v any) error {
	switch x := v.(type) {
	case nil:
		buf.WriteString("null")
	case bool:
		if x {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
	case string:
		encodeString(buf, x)
	case json.Number:
		buf.WriteString(x.String())
	case int:
		buf.WriteString(strconv.Itoa(x))
	case int64:
		buf.WriteString(strconv.FormatInt(x, 10))
	case float64:
		buf.WriteString(PyFloat(x).String())
	case []any:
		buf.WriteByte('[')
		for i, item := range x {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := encodeValue(buf, item); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	case []string:
		buf.WriteByte('[')
		for i, item := range x {
			if i > 0 {
				buf.WriteByte(',')
			}
			encodeString(buf, item)
		}
		buf.WriteByte(']')
	case []map[string]any:
		buf.WriteByte('[')
		for i, item := range x {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := encodeValue(buf, item); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		buf.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			encodeString(buf, k)
			buf.WriteByte(':')
			if err := encodeValue(buf, x[k]); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
	default:
		return fmt.Errorf("canonical: unsupported type %T", v)
	}
	return nil
}

// encodeString matches Python json.dumps(ensure_ascii=False): raw UTF-8,
// short escapes for \b \t \n \f \r, \u00xx for other control chars, no
// escaping of < > & / U+2028 U+2029.
func encodeString(buf *bytes.Buffer, s string) {
	buf.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			buf.WriteString("\\\"")
		case '\\':
			buf.WriteString("\\\\")
		case '\b':
			buf.WriteString("\\b")
		case '\f':
			buf.WriteString("\\f")
		case '\n':
			buf.WriteString("\\n")
		case '\r':
			buf.WriteString("\\r")
		case '\t':
			buf.WriteString("\\t")
		default:
			if r < 0x20 {
				fmt.Fprintf(buf, "\\u%04x", r)
			} else if r == utf8.RuneError {
				buf.WriteRune(r)
			} else {
				buf.WriteRune(r)
			}
		}
	}
	buf.WriteByte('"')
}

// stripKey returns a shallow copy of obj without key (for signature payloads).
func stripKey(obj map[string]any, key string) map[string]any {
	out := make(map[string]any, len(obj))
	for k, v := range obj {
		if k != key {
			out[k] = v
		}
	}
	return out
}

func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func asBool(v any, def bool) bool {
	if b, ok := v.(bool); ok {
		return b
	}
	return def
}

func asFloat(v any) float64 {
	switch x := v.(type) {
	case json.Number:
		f, _ := x.Float64()
		return f
	case float64:
		return x
	case int:
		return float64(x)
	}
	return 0
}

func trimSpace(s string) string { return strings.TrimSpace(s) }