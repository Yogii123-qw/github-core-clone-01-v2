// JSON helpers: rendering with JavaScript-compatible escaping, and
// strictly typed accessors for decoded request bodies. Decoded JSON
// values use encoding/json's default representation: float64 for numbers,
// string, bool, nil, []any, map[string]any.
package main

import (
	"bytes"
	"encoding/json"
	"math"
)

// marshalJSON renders v as compact JSON without HTML escaping, matching
// JavaScript JSON.stringify for the shapes this forge emits. It never
// appends a trailing newline.
func marshalJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// mustMarshalJSON renders v and panics on error. It is only used for
// values built from constants and validated state, where encoding cannot
// fail (no channels, functions, NaN, or cyclic maps).
func mustMarshalJSON(v any) []byte {
	b, err := marshalJSON(v)
	if err != nil {
		panic(err)
	}
	return b
}

// unmarshalJSON parses a JSON document. Trailing data after the first
// value is rejected, matching JSON.parse.
func unmarshalJSON(data []byte) (any, error) {
	var v any
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	// Reject trailing non-whitespace content.
	var extra any
	if err := dec.Decode(&extra); err == nil {
		return nil, errTrailingJSON
	}
	return v, nil
}

type jsonError string

func (e jsonError) Error() string { return string(e) }

const errTrailingJSON = jsonError("trailing data after JSON value")

// asObject returns v as a JSON object, or nil when it is not one.
func asObject(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return nil
}

// asArray returns v as a JSON array, or nil when it is not one.
func asArray(v any) []any {
	if a, ok := v.([]any); ok {
		return a
	}
	return nil
}

// asString returns v as a string, or "" when it is not one. The second
// result reports whether v was a string.
func asString(v any) (string, bool) {
	s, ok := v.(string)
	return s, ok
}

// asBool returns v as a bool, or false when it is not one. The second
// result reports whether v was a bool.
func asBool(v any) (bool, bool) {
	b, ok := v.(bool)
	return b, ok
}

// asFloat returns v as a float64, or 0 when it is not a JSON number. The
// second result reports whether v was a number.
func asFloat(v any) (float64, bool) {
	f, ok := v.(float64)
	return f, ok
}

// asInt returns v as an int when it is a JSON number with an integral
// value in int range, mirroring Number.isInteger. Otherwise ok is false.
func asInt(v any) (int, bool) {
	f, ok := v.(float64)
	if !ok || math.Trunc(f) != f || f > float64(math.MaxInt) || f < float64(math.MinInt) {
		return 0, false
	}
	return int(f), true
}

// obj is a shorthand for building JSON objects.
type obj = map[string]any
