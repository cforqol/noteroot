// Package frontmatter parses note frontmatter.
//
// Frontmatter is load-bearing: it carries note identity. This parser must
// never fail — a malformed note still has to open. Problems are recorded in
// Doc.Err for diagnostics; the caller decides what to do.
package frontmatter

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"time"

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

	// The closing delimiter must start within maxBlock bytes. The limit
	// bounds the block, not the note: a large body after a small block
	// is still frontmatter (bounding the whole rest made writers prepend
	// a second block to every note over 64 KiB).
	rest := src[nl+1:]
	off := 0
	for off < len(rest) && off <= maxBlock {
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
		// A YAML slip elsewhere in the block must not cost the note its
		// identity: writers mint (and WithID overwrites) when ID is
		// empty, so recover a well-formed top-level id line textually.
		if id := textualID(block); idRe.MatchString(id) {
			d.ID = id
		}
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

// textualID returns the unquoted value of the first top-level "id:" line
// of a frontmatter block (the line WithID would rewrite), or "".
func textualID(block []byte) string {
	for _, ln := range strings.Split(string(block), "\n") {
		if len(ln) == 0 || ln[0] == ' ' || ln[0] == '\t' {
			continue
		}
		key, val, found := strings.Cut(ln, ":")
		if !found || strings.TrimSpace(key) != "id" {
			continue
		}
		val = strings.TrimSpace(val)
		if i := strings.Index(val, " #"); i >= 0 {
			val = strings.TrimSpace(val[:i])
		}
		return strings.Trim(val, `"'`)
	}
	return ""
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

// crock is the Crockford base32 alphabet: no I, L, O, U.
const crock = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// ValidID reports whether id is a well-formed 28-char ULID+checksum.
func ValidID(id string) bool { return idRe.MatchString(id) }

// NewID returns a fresh note identity: a 26-char ULID (48-bit
// millisecond timestamp + 80 bits crypto/rand) followed by a 2-char
// CRC32 checksum. Sortable by creation time; the checksum catches typos
// in copied ids (not a security property — ids are not capabilities).
func NewID() string {
	var rnd [10]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		panic("frontmatter: crypto/rand unavailable: " + err.Error())
	}
	u := encodeULID(uint64(time.Now().UnixMilli()), rnd)
	return u + ulidChecksum(u)
}

// WithID injects id into src's frontmatter: prepending a block when there
// is none, inserting after the opening --- when the block has no top-level
// id line, or replacing the existing (valid or malformed) id line. Textual
// — the author's formatting, key order, and comments outside the id line
// are preserved.
func WithID(src []byte, id string) []byte {
	block, body, ok := Split(src)
	if !ok {
		header := "---\nid: " + id + "\n---\n"
		if len(body) > 0 && body[0] != '\n' {
			header += "\n"
		}
		return append([]byte(header), body...)
	}
	// The block ends with its last line's newline; splitting that keeps
	// an empty final piece, which joined back as a blank line before the
	// closing delimiter. Blank lines the author wrote are kept.
	var lines []string
	if text := strings.TrimSuffix(string(block), "\n"); text != "" {
		lines = strings.Split(text, "\n")
	}
	replaced := false
	for i, ln := range lines {
		// Only match top-level keys: the line must start at column 0.
		// Indented lines belong to nested mappings and must not be touched.
		if len(ln) == 0 || ln[0] == ' ' || ln[0] == '\t' {
			continue
		}
		// Match "id:", "id :", "id  :", etc. (whitespace before the colon).
		key := strings.SplitN(ln, ":", 2)
		if strings.TrimSpace(key[0]) == "id" {
			lines[i] = "id: " + id
			replaced = true
			break
		}
	}
	if !replaced {
		lines = append([]string{"id: " + id}, lines...)
	}
	out := "---\n" + strings.Join(lines, "\n") + "\n---\n"
	if len(body) > 0 {
		out += string(body)
	}
	return []byte(out)
}

// encodeULID encodes a 48-bit timestamp and 80 bits of entropy as a
// 26-char Crockford base32 ULID.
func encodeULID(ts uint64, rnd [10]byte) string {
	var raw [16]byte
	binary.BigEndian.PutUint64(raw[0:8], ts<<16)
	copy(raw[6:], rnd[:])
	n := new(big.Int).SetBytes(raw[:])
	base := big.NewInt(32)
	out := make([]byte, 26)
	mod := new(big.Int)
	for i := 25; i >= 0; i-- {
		n.DivMod(n, base, mod)
		out[i] = crock[mod.Int64()]
	}
	return string(out)
}

// ulidChecksum is CRC32 (IEEE) of the 26-char ULID, reduced to 10 bits
// and encoded as 2 Crockford chars — enough to catch typos and
// single-character corruption in copied ids.
func ulidChecksum(u26 string) string {
	c := crc32.ChecksumIEEE([]byte(u26)) % 1024
	return string([]byte{crock[c/32], crock[c%32]})
}
