package vault

import (
	"fmt"
	"path"
	"strings"

	"golang.org/x/text/unicode/norm"
)

var reserved = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true,
	"COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true,
	"LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
}

// maxDepth caps folder nesting: user → notebook → folders → notes.
const maxDepth = 5

// Rel validates and normalizes a vault-relative path.
func Rel(p string) (string, error) {
	p = norm.NFC.String(strings.TrimSpace(p))
	p = strings.ReplaceAll(p, `\`, "/")

	if p == "" {
		return "", fmt.Errorf("empty path")
	}
	if strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("absolute path %q", p)
	}
	if p != path.Clean(p) {
		p = path.Clean(p)
	}
	if p == "." || strings.HasPrefix(p, "../") || p == ".." {
		return "", fmt.Errorf("path escapes vault: %q", p)
	}
	segs := strings.Split(p, "/")
	if len(segs) > maxDepth {
		return "", fmt.Errorf("path too deep (max %d levels)", maxDepth)
	}
	for _, seg := range segs {
		if err := checkSegment(seg); err != nil {
			return "", fmt.Errorf("%q: %w", seg, err)
		}
	}
	return p, nil
}

func checkSegment(seg string) error {
	if seg == "" {
		return fmt.Errorf("empty segment")
	}
	// The vault root is a git repo; .git must never be addressable, or a
	// write could corrupt version history. Listing already skips it.
	if seg == ".git" {
		return fmt.Errorf("reserved name")
	}
	for _, r := range seg {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("control character")
		}
	}
	if strings.HasSuffix(seg, ".") || strings.HasSuffix(seg, " ") {
		return fmt.Errorf("trailing dot or space")
	}
	base := seg
	if i := strings.IndexByte(seg, '.'); i >= 0 {
		base = seg[:i]
	}
	if reserved[strings.ToUpper(base)] {
		return fmt.Errorf("reserved name")
	}
	return nil
}

func Ignored(rel string) bool {
	base := path.Base(rel)
	if base == ".git" || strings.HasPrefix(base, ".") {
		return true
	}
	switch {
	case strings.HasSuffix(base, ".swp"),
		strings.HasSuffix(base, ".swo"),
		strings.HasSuffix(base, "~"),
		strings.HasSuffix(base, ".tmp"):
		return true
	}
	return false
}

func IsNote(rel string) bool {
	return strings.HasSuffix(rel, ".md") && !Ignored(rel)
}
