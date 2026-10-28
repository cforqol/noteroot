package integrations

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"noteroot/internal/platform"
	"noteroot/internal/plugins"
)

// Hook is one live inbound webhook. Verification and the write both
// run in the caller's request (no queue): the sender is already
// blocked on its own HTTP round trip, and the write path is the same
// one plugins use — identity minting, index, git, audit, event bus —
// with hook attribution so the chain depth keeps climbing and the
// bus's limit terminates a careless echo.
type Hook struct {
	cfg   InboundConfig
	slug  string
	store plugins.NoteStore
	log   *slog.Logger

	mu     sync.Mutex
	secret string
}

func newHook(cfg InboundConfig, slug string, store plugins.NoteStore, log *slog.Logger) *Hook {
	return &Hook{cfg: cfg, slug: slug, store: store, log: log}
}

func (h *Hook) setSecret(s string) {
	h.mu.Lock()
	h.secret = s
	h.mu.Unlock()
}

func (h *Hook) getSecret() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.secret
}

// Handle verifies the signature over body and writes the note. sig is
// the X-Noteroot-Signature (or X-Hub-Signature-256) header value;
// depth is the X-Noteroot-Depth header (0 when absent — a fresh
// external sender starts a fresh chain).
func (h *Hook) Handle(ctx context.Context, body []byte, sig string, depth int) (string, error) {
	secret := h.getSecret()
	if secret == "" {
		// Inert until a secret is set: report not-found so an
		// unconfigured endpoint stays indistinguishable from a
		// typo'd name.
		return "", ErrHookNotFound
	}
	if !Verify(secret, body, sig) {
		return "", ErrBadSignature
	}
	if depth >= platform.DefaultMaxDepth {
		// The write's event would land past the bus limit: this is
		// an echo loop that survived one hop too many. Reject the
		// content rather than let spam accumulate.
		return "", ErrDepthExceeded
	}

	existing := ""
	if h.cfg.Mode != "replace" {
		if cur, err := h.store.Read(ctx, h.cfg.Path); err == nil {
			existing = cur
		} else if !isNotFound(err) {
			return "", err
		}
	}
	content := string(body)
	if h.cfg.Mode != "replace" {
		var b strings.Builder
		b.WriteString(existing)
		if existing != "" {
			if !strings.HasSuffix(existing, "\n") {
				b.WriteString("\n")
			}
			b.WriteString("\n---\n\n")
		}
		b.WriteString("### ")
		b.WriteString(time.Now().UTC().Format(time.RFC3339))
		b.WriteString("\n\n")
		b.WriteString(string(body))
		content = b.String()
	}

	actor := "hook:" + h.cfg.Name
	err := h.store.Write(ctx, plugins.WriteRequest{
		Actor:   actor,
		Origin:  actor,
		Depth:   depth + 1,
		Path:    h.cfg.Path,
		Content: content,
	})
	if err != nil {
		return "", err
	}
	return h.cfg.Path, nil
}

func isNotFound(err error) bool {
	// plugins.ErrNotFound is the store's missing-path sentinel; the
	// adapter maps vault.ErrNotFound onto it.
	return err == plugins.ErrNotFound
}
