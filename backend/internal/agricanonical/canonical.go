// Package agricanonical validates the bounded rfc8785-safe-v1 input domain.
// Number serialization and UTF-16 property ordering belong to the pinned JCS library.
package agricanonical

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	jcs "github.com/cyberphone/json-canonicalization/go/src/webpki.org/jsoncanonicalizer"
)

var ErrInvalid = errors.New("invalid rfc8785-safe-v1 JSON")

// Canonicalize accepts an object, at most 1 MiB, depth 32 (root is one),
// 256 keys per object and 10000 value nodes. It never returns partial output.
func Canonicalize(raw []byte) ([]byte, error) {
	if len(raw) == 0 || len(raw) > 1<<20 || !utf8.Valid(raw) || !json.Valid(raw) || !validEscapes(raw) {
		return nil, ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	nodes := 0
	if err := walk(d, 1, &nodes, true); err != nil {
		return nil, ErrInvalid
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, ErrInvalid
	}
	out, err := jcs.Transform(raw)
	if err != nil {
		return nil, ErrInvalid
	}
	return out, nil
}

// encoding/json replaces malformed surrogate escapes, so reject them before decoding.
func validEscapes(raw []byte) bool {
	depth := 0
	inString := false
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if !inString {
			if c == '"' {
				inString = true
			}
			if c == '{' || c == '[' {
				depth++
				if depth > 32 {
					return false
				}
			}
			if c == '}' || c == ']' {
				depth--
			}
			continue
		}
		if c == '"' {
			inString = false
			continue
		}
		if c != '\\' {
			continue
		}
		i++
		if raw[i] != 'u' {
			continue
		}
		u, _ := strconv.ParseUint(string(raw[i+1:i+5]), 16, 16)
		i += 4
		if u >= 0xdc00 && u <= 0xdfff {
			return false
		}
		if u >= 0xd800 && u <= 0xdbff {
			if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
				return false
			}
			v, err := strconv.ParseUint(string(raw[i+3:i+7]), 16, 16)
			if err != nil || v < 0xdc00 || v > 0xdfff {
				return false
			}
			i += 6
		}
	}
	return true
}

func walk(d *json.Decoder, depth int, nodes *int, root bool) error {
	*nodes++
	if depth > 32 || *nodes > 10000 {
		return ErrInvalid
	}
	t, err := d.Token()
	if err != nil {
		return err
	}
	if root && t != json.Delim('{') {
		return ErrInvalid
	}
	switch v := t.(type) {
	case json.Number:
		if !safeNumber(string(v)) {
			return ErrInvalid
		}
	case json.Delim:
		switch v {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				key, e := d.Token()
				if e != nil {
					return e
				}
				s, ok := key.(string)
				if !ok || seen[s] || len(seen) == 256 {
					return ErrInvalid
				}
				seen[s] = true
				if e := walk(d, depth+1, nodes, false); e != nil {
					return e
				}
			}
			end, e := d.Token()
			if e != nil || end != json.Delim('}') {
				return ErrInvalid
			}
		case '[':
			for d.More() {
				if e := walk(d, depth+1, nodes, false); e != nil {
					return e
				}
			}
			end, e := d.Token()
			if e != nil || end != json.Delim(']') {
				return ErrInvalid
			}
		default:
			return ErrInvalid
		}
	}
	return nil
}

// Compare the exact decimal token before binary64 rounding, without allocating
// exponent-sized integers. Saturation exceeds any possible 1 MiB significand.
func safeNumber(token string) bool {
	s := strings.TrimPrefix(token, "-")
	exp := int64(0)
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		e := s[i+1:]
		s = s[:i]
		negative := strings.HasPrefix(e, "-")
		e = strings.TrimLeft(strings.TrimLeft(e, "+-"), "0")
		if len(e) > 7 {
			exp = 2000000
		} else {
			exp, _ = strconv.ParseInt(e, 10, 64)
			if exp > 2000000 {
				exp = 2000000
			}
		}
		if negative {
			exp = -exp
		}
	}
	if i := strings.IndexByte(s, '.'); i >= 0 {
		exp -= int64(len(s) - i - 1)
		s = s[:i] + s[i+1:]
	}
	s = strings.TrimLeft(s, "0")
	if s != "" {
		position := int64(len(s)) + exp
		if position > 16 {
			return false
		}
		if position == 16 {
			whole := s
			if len(whole) < 16 {
				whole += strings.Repeat("0", 16-len(whole))
			} else {
				whole = whole[:16]
			}
			if whole > "9007199254740991" {
				return false
			}
			if whole == "9007199254740991" && len(s) > 16 && strings.Trim(s[16:], "0") != "" {
				return false
			}
		}
	}
	f, err := strconv.ParseFloat(token, 64)
	return err == nil && !math.IsInf(f, 0) && !math.IsNaN(f)
}
