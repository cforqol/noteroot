package integrations

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"noteroot/internal/platform"
)

const (
	defaultQueueSize       = 64
	defaultSendTimeout     = 10 * time.Second
	defaultBreakerStrikes  = 3
	defaultBreakerCooldown = 30 * time.Second
)

// Integration is one live outbound webhook: a bounded queue, a serial
// delivery loop, and a circuit breaker so a dead endpoint stops
// accumulating latency for everyone else's events. Delivery never
// blocks the bus — enqueue drops when the queue is full.
type Integration struct {
	cfg OutboundConfig
	log *slog.Logger

	sendTimeout time.Duration

	secretMu sync.Mutex
	secret   string

	queue chan platform.Event

	mu     sync.Mutex
	brk    breaker
	closed bool
	sendMu sync.Mutex
}

func newIntegration(cfg OutboundConfig, log *slog.Logger) *Integration {
	return &Integration{
		cfg:         cfg,
		log:         log,
		sendTimeout: defaultSendTimeout,
		queue:       make(chan platform.Event, defaultQueueSize),
		brk:         newBreaker(),
	}
}

// enqueue never blocks: a full queue drops the delivery (logged),
// which is the correct trade — events are recovery-grade, a stuck
// receiver must not turn into memory growth or a stalled save.
func (ig *Integration) enqueue(ev platform.Event) {
	ig.sendMu.Lock()
	defer ig.sendMu.Unlock()
	if ig.closed {
		return
	}
	select {
	case ig.queue <- ev:
	default:
		ig.log.Warn("integration queue full, delivery dropped",
			"integration", ig.cfg.Name, "event", ev.Type)
	}
}

func (ig *Integration) eventLoop() {
	for ev := range ig.queue {
		ig.deliver(ev)
	}
}

func (ig *Integration) close() {
	ig.sendMu.Lock()
	if !ig.closed {
		ig.closed = true
		close(ig.queue)
	}
	ig.sendMu.Unlock()
}

func (ig *Integration) setSecret(s string) {
	ig.secretMu.Lock()
	ig.secret = s
	ig.secretMu.Unlock()
}

func (ig *Integration) getSecret() string {
	ig.secretMu.Lock()
	defer ig.secretMu.Unlock()
	return ig.secret
}

func (ig *Integration) breakerOpen() bool {
	ig.mu.Lock()
	defer ig.mu.Unlock()
	return ig.brk.open()
}

// delivery is the JSON body a receiving service gets.
type delivery struct {
	Event    string         `json:"event"`
	Slug     string         `json:"slug"`
	Path     string         `json:"path,omitempty"`
	Actor    string         `json:"actor"`
	Origin   string         `json:"origin"`
	Depth    int            `json:"depth"`
	Count    int            `json:"count"`
	Data     map[string]any `json:"data,omitempty"`
	Delivery string         `json:"delivery"`
	TS       string         `json:"ts"`
}

func (ig *Integration) deliver(ev platform.Event) {
	secret := ig.getSecret()
	if secret == "" {
		// Config without a stored secret: inert, never unsigned.
		return
	}
	ig.mu.Lock()
	if !ig.brk.allow() {
		ig.mu.Unlock()
		return
	}
	ig.mu.Unlock()

	id := newDeliveryID()
	body, err := json.Marshal(delivery{
		Event:    ev.Type,
		Slug:     ev.Slug,
		Path:     ev.Path,
		Actor:    ev.Actor,
		Origin:   ev.Origin,
		Depth:    ev.Depth,
		Count:    ev.Count,
		Data:     ev.Data,
		Delivery: id,
		TS:       time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		ig.log.Warn("integration marshal failed", "integration", ig.cfg.Name, "err", err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), ig.sendTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ig.cfg.URL, bytes.NewReader(body))
	if err != nil {
		ig.log.Warn("integration request build failed", "integration", ig.cfg.Name, "err", err)
		ig.strike()
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Noteroot-Event", ev.Type)
	req.Header.Set("X-Noteroot-Slug", ev.Slug)
	req.Header.Set("X-Noteroot-Depth", fmt.Sprint(ev.Depth))
	req.Header.Set("X-Noteroot-Delivery", id)
	req.Header.Set("X-Noteroot-Signature", Sign(secret, body))

	resp, err := ig.client().Do(req)
	if err != nil {
		ig.log.Warn("integration delivery failed", "integration", ig.cfg.Name, "err", err)
		ig.strike()
		return
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		ig.log.Warn("integration delivery rejected",
			"integration", ig.cfg.Name, "status", resp.StatusCode)
		ig.strike()
		return
	}
	ig.mu.Lock()
	ig.brk.ok()
	ig.mu.Unlock()
}

// client never follows redirects: a 30x to another host would resend
// the signed body (and its signature) to an address the config never
// approved. A redirect is simply a failed delivery.
func (ig *Integration) client() *http.Client {
	return &http.Client{
		Timeout: ig.sendTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func (ig *Integration) strike() {
	ig.mu.Lock()
	ig.brk.fail()
	open := ig.brk.open()
	ig.mu.Unlock()
	if open {
		ig.log.Warn("integration breaker opened", "integration", ig.cfg.Name)
	}
}

// Sign computes the X-Noteroot-Signature value for a body.
func Sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// Verify checks a signature header (ours or GitHub's X-Hub-Signature-
// 256 format — same scheme) against the body, constant-time.
func Verify(secret string, body []byte, header string) bool {
	if secret == "" || header == "" {
		return false
	}
	return hmac.Equal([]byte(Sign(secret, body)), []byte(header))
}

func newDeliveryID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("t-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// breaker is the same consecutive-failure circuit the plugin runtime
// uses: strikes open the circuit, a cooldown half-opens it for one
// probe, the first success closes it.
type breaker struct {
	strikes   int
	threshold int
	opened    time.Time
	cooldown  time.Duration
	isOpen    bool
}

func (b *breaker) open() bool {
	if !b.isOpen {
		return false
	}
	if time.Since(b.opened) >= b.cooldown {
		b.isOpen = false // half-open: allow one probe
		b.strikes = 0
		return false
	}
	return true
}

func (b *breaker) allow() bool { return !b.open() }

func (b *breaker) fail() {
	b.strikes++
	if b.strikes >= b.threshold {
		b.isOpen = true
		b.opened = time.Now()
	}
}

func (b *breaker) ok() {
	b.strikes = 0
	b.isOpen = false
}

func newBreaker() breaker {
	return breaker{threshold: defaultBreakerStrikes, cooldown: defaultBreakerCooldown}
}
