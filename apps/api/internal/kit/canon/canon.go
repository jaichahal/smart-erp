// Package canon produces the canonical JSON form used for hashing (05 "Hash chain").
//
// Rules: object keys sorted bytewise, no insignificant whitespace, numbers are
// forbidden at the top level of money and quantity fields (callers pass decimals as
// strings), timestamps are RFC 3339 UTC with no sub-second trailing zeros trimmed
// (callers pass time.Time; we format), child arrays keep caller order (callers sort
// by line number), and the fields "prev_hash" and "hash" are always dropped.
//
// Hash is sha256(canonical || prev_hash) as lowercase hex.
package canon

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"time"
)

// Excluded keys never participate in the canonical form.
var excluded = map[string]bool{"prev_hash": true, "hash": true}

// Marshal returns the canonical JSON bytes for v. v may be a struct, map, slice,
// or scalar; structs are first round-tripped through encoding/json so their
// `json` tags decide field names.
func Marshal(v any) ([]byte, error) {
	generic, err := toGeneric(v)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := write(&buf, generic); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Hash returns hex(sha256(canonical || prevHash)).
func Hash(canonical []byte, prevHash string) string {
	h := sha256.New()
	h.Write(canonical)
	h.Write([]byte(prevHash))
	return hex.EncodeToString(h.Sum(nil))
}

// MarshalAndHash is the common path: canonicalise v, then hash with prevHash.
func MarshalAndHash(v any, prevHash string) (canonical []byte, hash string, err error) {
	canonical, err = Marshal(v)
	if err != nil {
		return nil, "", err
	}
	return canonical, Hash(canonical, prevHash), nil
}

func toGeneric(v any) (any, error) {
	switch t := v.(type) {
	case time.Time:
		return t.UTC().Format(time.RFC3339Nano), nil
	case json.RawMessage:
		var out any
		dec := json.NewDecoder(bytes.NewReader(t))
		dec.UseNumber()
		if err := dec.Decode(&out); err != nil {
			return nil, fmt.Errorf("canon: raw json: %w", err)
		}
		return out, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("canon: marshal: %w", err)
	}
	var out any
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := dec.Decode(&out); err != nil {
		return nil, fmt.Errorf("canon: decode: %w", err)
	}
	return out, nil
}

func write(buf *bytes.Buffer, v any) error {
	switch t := v.(type) {
	case nil:
		buf.WriteString("null")
	case bool:
		if t {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
	case string:
		// Any RFC 3339 timestamp is normalised to UTC so the same instant hashes
		// identically regardless of the zone the producer used.
		if ts, err := time.Parse(time.RFC3339Nano, t); err == nil {
			t = ts.UTC().Format(time.RFC3339Nano)
		}
		enc, err := json.Marshal(t)
		if err != nil {
			return err
		}
		buf.Write(enc)
	case json.Number:
		// Normalise numeric text so 1.0 and 1.00 hash identically.
		r, ok := new(big.Rat).SetString(t.String())
		if !ok {
			return fmt.Errorf("canon: bad number %q", t)
		}
		if r.IsInt() {
			buf.WriteString(r.Num().String())
		} else {
			f, _ := r.Float64()
			buf.WriteString(trimFloat(f))
		}
	case []any:
		buf.WriteByte('[')
		for i, e := range t {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := write(buf, e); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			if excluded[k] {
				continue
			}
			keys = append(keys, k)
		}
		sort.Strings(keys)
		buf.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			enc, err := json.Marshal(k)
			if err != nil {
				return err
			}
			buf.Write(enc)
			buf.WriteByte(':')
			if err := write(buf, t[k]); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
	default:
		return fmt.Errorf("canon: unsupported type %T", v)
	}
	return nil
}

func trimFloat(f float64) string {
	return strconv.FormatFloat(f, 'g', -1, 64)
}
