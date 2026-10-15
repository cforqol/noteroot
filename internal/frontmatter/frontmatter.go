// Package frontmatter parses note frontmatter.
//
// Frontmatter is load-bearing: it carries note identity. This parser must
// never fail — a malformed note still has to open. Problems are recorded in
// Doc.Err for diagnostics; the caller decides what to do.
package frontmatter

import (
	"bytes"
	"errors"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	delim    = "---"
	maxBlock = 64 << 10
)

var ErrMalformedID = errors.New("malformed id")

// idRe matches a ULID (Crockford base32) plus a 2-char checksum.
var idRe = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{28}$`)

type Doc struct {
	Body  []byte
	Meta  map[string]any
	ID    string
	Title string
	Err   error
}

// Split separates a leading frontmatter block from the body.
// ok=false means there is no well-formed block; the whole input is body.
func Split(src []byte) (block, body []byte, ok bool) {
	if !bytes.HasPrefix(src, []byte(delim)) {
		return nil, src, false
	}
	nl := bytes.IndexByte(src, '\n')
	if nl < 0 {
		return nil, src, false
	}
	if strings.TrimRight(string(src[:nl]), "\r") != delim {
		return nil, src, false
	}

	rest := src[nl+1:]
	if len(rest) > maxBlock {
		return nil, src, false
	}

	off := 0
	for off < len(rest) {
		lineEnd := bytes.IndexByte(rest[off:], '\n')
		var line []byte
		if lineEnd < 0 {
			line = rest[off:]
		} else {
			line = rest[off : off+lineEnd]
		}
		if strings.TrimRight(string(line), "\r") == delim {
			block = rest[:off]
			if lineEnd >= 0 {
				body = rest[off+lineEnd+1:]
			}
			return block, body, true
		}
		if lineEnd < 0 {
			break
		}
		off += lineEnd + 1
	}
	// Unterminated block: keep everything as body so nothing is lost.
	return nil, src, false
}

// Parse never returns an error. Problems land in Doc.Err.
func Parse(src []byte) Doc {
	block, body, ok := Split(src)
	if !ok {
		return Doc{Body: src, Meta: map[string]any{}}
	}

	d := Doc{Body: body, Meta: map[string]any{}}
	if err := yaml.Unmarshal(block, &d.Meta); err != nil {
		d.Err = err
		d.Meta = map[string]any{}
		return d
	}

	d.ID = str(d.Meta["id"])
	if d.ID != "" && !idRe.MatchString(d.ID) {
		d.Err = ErrMalformedID
		d.ID = "" // caller assigns one
	}
	d.Title = str(d.Meta["title"])
	return d
}

// str coerces a decoded YAML value to a string. Needed because YAML turns
// all-digit values into ints — and an early ULID can be all digits.
func str(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(t)
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case uint:
		return strconv.FormatUint(uint64(t), 10)
	case uint64:
		return strconv.FormatUint(t, 10)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		if t {
			return "true"
		}
		return "false"
	default:
		return ""
	}
}

// Strings extracts a []string, tolerating a bare scalar.
func Strings(meta map[string]any, key string) []string {
	switch t := meta[key].(type) {
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			if s := str(e); s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		if s := str(meta[key]); s != "" {
			return []string{s}
		}
	}
	return nil
}
