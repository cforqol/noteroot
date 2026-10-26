// Package lock implements the password material behind content locks:
// argon2id hashing, verification and opaque session tokens.
//
// A content lock is a UI gate, not secrecy. The note stays plaintext on
// disk, in git, in the search index and in backups; this package only
// decides whether a password matches so the API can withhold content.
// Real end-to-end encryption is a separate, larger effort.
package lock

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// argon2id parameters. Chosen as a middle ground for a self-hosted box:
// memory-hard enough to make offline guessing of a stored hash costly,
// light enough to verify in well under a second.
const (
	timeCost    = 1
	memoryCost  = 64 * 1024 // 64 MiB
	parallelism = 4
	keyLen      = 32
	saltLen     = 16
)

// MinPasswordLen is the shortest password a lock will accept. Locks are
// advisory, but a one-character "password" is worse than none.
const MinPasswordLen = 4

// ErrBadPassword reports a wrong password. ErrNoMatch is the same idea
// for path resolution.
var (
	ErrBadPassword = errors.New("wrong password")
	ErrTooShort    = errors.New("password too short")
)

// Hash derives argon2id material for a password. The salt is random per
// call, so the same password hashes differently every time.
func Hash(password string) (hash, salt string, err error) {
	if len(password) < MinPasswordLen {
		return "", "", ErrTooShort
	}
	s := make([]byte, saltLen)
	if _, err := rand.Read(s); err != nil {
		return "", "", err
	}
	key := argon2.IDKey([]byte(password), s, timeCost, memoryCost, parallelism, keyLen)
	return base64.RawStdEncoding.EncodeToString(key),
		base64.RawStdEncoding.EncodeToString(s), nil
}

// Verify reports whether password matches stored hash/salt in constant
// time. A malformed stored salt or hash simply fails.
func Verify(password, hash, salt string) bool {
	s, err := base64.RawStdEncoding.DecodeString(salt)
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(hash)
	if err != nil {
		return false
	}
	got := argon2.IDKey([]byte(password), s, timeCost, memoryCost, parallelism, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// NewToken returns an opaque, URL-safe session token.
func NewToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// CookieName is the per-notebook cookie that carries the unlock session
// tokens, concatenated with ".". Per-notebook so locking one notebook
// never leaks an unlock into another.
func CookieName(slug string) string {
	return "nr_lock_" + slug
}

// ParseTokens splits a cookie value into its individual tokens.
func ParseTokens(cookie string) []string {
	if cookie == "" {
		return nil
	}
	parts := strings.Split(cookie, ".")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// JoinTokens rebuilds a cookie value from tokens.
func JoinTokens(tokens []string) string { return strings.Join(tokens, ".") }

// AddToken returns tokens with t appended (or unchanged when present).
func AddToken(tokens []string, t string) []string {
	for _, x := range tokens {
		if x == t {
			return tokens
		}
	}
	return append(tokens, t)
}

// RemoveToken returns tokens without t.
func RemoveToken(tokens []string, t string) []string {
	out := make([]string, 0, len(tokens))
	for _, x := range tokens {
		if x != t {
			out = append(out, x)
		}
	}
	return out
}

// CoverPath reports whether a session on sessionPath unlocks target:
// a session on the notebook root ("") covers everything, a session on a
// folder covers its descendants, and a session on a note covers only it.
func CoverPath(sessionPath, target string) bool {
	if sessionPath == "" {
		return true
	}
	if target == sessionPath {
		return true
	}
	if strings.HasSuffix(sessionPath, "/") {
		return strings.HasPrefix(target, sessionPath)
	}
	return false
}

// Covers reports whether any session path unlocks target.
func Covers(sessionPaths []string, target string) bool {
	for _, sp := range sessionPaths {
		if CoverPath(sp, target) {
			return true
		}
	}
	return false
}

// NormalizeRel canonicalizes a lock path the same way locks are stored:
// "" (root), "dir/…/" for folders, "dir/…/name.md" for notes.
func NormalizeRel(kind, rel string) string {
	rel = strings.TrimSpace(rel)
	rel = strings.Trim(rel, "/")
	if rel == "" {
		return ""
	}
	if kind == "folder" || strings.HasSuffix(rel, "/") {
		return rel + "/"
	}
	return rel
}

// FormatParams is a human-readable summary of the hash parameters, for
// the settings/about surface.
func FormatParams() string {
	return fmt.Sprintf("argon2id t=%d m=%dKiB p=%d", timeCost, memoryCost, parallelism)
}
