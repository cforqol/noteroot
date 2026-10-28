// Package resources implements content-addressed blob storage for notebooks.
//
// The pipeline is pure Policy → Pipeline → ResourceStore. Policy is a set of
// validation functions with no I/O; the store never consults policy. Callers
// (the HTTP layer) run policy first, then hand validated bytes to the store.
package resources

import (
	"errors"
	"fmt"
	"mime"
	"path"
	"strings"
)

// Policy is the set of rules a resource must satisfy before it reaches the
// store. Every field is optional; a zero Policy allows everything.
type Policy struct {
	// MaxSize caps the blob size in bytes. 0 = unlimited.
	MaxSize int64
	// AllowedMIME lists accepted MIME types. Empty = accept all.
	// Entries may end in "/*" to match a type prefix (e.g. "image/*").
	AllowedMIME []string
	// MaxNameLen caps the sanitized filename length. 0 = default (255).
	MaxNameLen int
}

// DefaultMaxSize is 50 MiB — large enough for phone photos, small enough
// that a runaway upload cannot fill a disk before the journal aborts it.
const DefaultMaxSize = 50 << 20

// DefaultMaxNameLen matches most filesystems' 255-byte filename limit.
const DefaultMaxNameLen = 255

// DefaultPolicy returns the recommended production policy.
func DefaultPolicy() Policy {
	return Policy{
		MaxSize: DefaultMaxSize,
		AllowedMIME: []string{
			"image/*",
			"video/*",
			"audio/*",
			"application/pdf",
			"text/plain",
		},
		MaxNameLen: DefaultMaxNameLen,
	}
}

// ErrTooLarge, ErrBadMIME, ErrBadName are stable sentinel errors so the HTTP
// layer can map them to 4xx without string matching.
var (
	ErrTooLarge = errors.New("resource exceeds size limit")
	ErrBadMIME  = errors.New("mime type not allowed")
	ErrBadName  = errors.New("invalid filename")
)

// Validate runs the full policy check. It returns the sanitized filename on
// success, or a descriptive error on failure.
func (p Policy) Validate(filename string, size int64, mime string) (string, error) {
	maxSize := p.MaxSize
	if maxSize <= 0 {
		maxSize = DefaultMaxSize
	}
	if size > maxSize {
		return "", fmt.Errorf("%w: %d bytes > %d", ErrTooLarge, size, maxSize)
	}

	if err := p.checkMIME(mime); err != nil {
		return "", err
	}

	name := SanitizeName(filename, p.MaxNameLen)
	if name == "" {
		return "", fmt.Errorf("%w: %q", ErrBadName, filename)
	}
	return name, nil
}

func (p Policy) checkMIME(m string) error {
	if len(p.AllowedMIME) == 0 {
		return nil
	}
	m = strings.ToLower(strings.TrimSpace(m))
	if m == "" {
		return fmt.Errorf("%w: empty", ErrBadMIME)
	}
	// SVG is XML and executes script same-origin; it must never pass as
	// an image, even when "image/*" is allowed.
	if m == "image/svg+xml" {
		return fmt.Errorf("%w: %q", ErrBadMIME, m)
	}
	for _, allowed := range p.AllowedMIME {
		allowed = strings.ToLower(strings.TrimSpace(allowed))
		if allowed == m {
			return nil
		}
		if strings.HasSuffix(allowed, "/*") && strings.HasPrefix(m, allowed[:len(allowed)-1]) {
			return nil
		}
	}
	return fmt.Errorf("%w: %q", ErrBadMIME, m)
}

// SanitizeName reduces an arbitrary filename to a safe, filesystem-friendly
// basename. It strips path components, control characters, leading dots
// (hidden files), and truncates to max bytes (default 255). The result is
// never empty for a non-empty input that contains at least one alphanumeric;
// callers must treat "" as an error.
func SanitizeName(name string, max int) string {
	if max <= 0 {
		max = DefaultMaxNameLen
	}
	// Strip any directory components.
	name = path.Base(strings.ReplaceAll(name, `\`, "/"))
	// Drop leading dots (hidden files / dotfiles).
	name = strings.TrimLeft(name, ".")
	// Replace path separators and control characters with underscores.
	var b strings.Builder
	for _, r := range name {
		if r < 0x20 || r == 0x7f || r == '/' || r == '\\' {
			b.WriteByte('_')
			continue
		}
		b.WriteRune(r)
	}
	name = strings.TrimSpace(b.String())
	if name == "" {
		return ""
	}
	// Truncate to max bytes without splitting a multi-byte rune.
	if len(name) > max {
		off := 0
		for i := range name {
			if i > max {
				break
			}
			off = i
		}
		// off is the byte offset of the last rune that still fits.
		// If even the first rune exceeds max, hard-cut at max.
		if off == 0 {
			off = max
		}
		name = name[:off]
	}
	// Never leave a trailing dot or space — Windows and some tools choke.
	name = strings.TrimRight(name, ". ")
	if name == "" {
		return ""
	}
	return name
}

// Ext returns the lowercase extension (including the dot) of a filename,
// or "" if none.
func Ext(name string) string {
	return strings.ToLower(path.Ext(name))
}

// SniffMIME guesses a MIME type from the filename extension. It never
// returns "application/octet-stream" for a known extension; unknown
// extensions return "application/octet-stream".
func SniffMIME(name string) string {
	ext := Ext(name)
	if ext == "" {
		return "application/octet-stream"
	}
	if t := mime.TypeByExtension(ext); t != "" {
		return t
	}
	return "application/octet-stream"
}
