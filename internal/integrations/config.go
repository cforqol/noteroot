// Package integrations wires noteroot to the world outside it:
// outbound webhooks that deliver notebook events to an external
// service, and inbound webhooks that let an external service write a
// note. Both are configured by files under .notebook/ (git-friendly,
// never containing a secret — HMAC secrets live in platform.db and
// are set over the authenticated API), and both attribute their
// writes so the audit trail names the integration, not a human.
//
// Loop suppression: an outbound delivery carries the triggering
// event's hop depth in X-Noteroot-Depth; an inbound hook continues
// the chain at depth+1, and the bus's depth limit terminates the
// cycle even when a service blindly echoes deliveries back.
package integrations

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"
)

// ConfigFile is the config file name inside each directory.
const ConfigFileExt = ".toml"

var (
	nameRe  = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	eventRe = regexp.MustCompile(`^(\*|[a-z]+(\.[a-z_]+)+)$`)
)

// OutboundConfig is one delivery target: an external URL that
// receives signed event JSON for the configured event types.
type OutboundConfig struct {
	Name    string   `toml:"-"` // directory entry base name
	URL     string   `toml:"url"`
	Events  []string `toml:"events"` // ["note.write", …] or ["*"]
	Enabled *bool    `toml:"enabled"`
}

// IsEnabled defaults to true (a config file is a declaration of
// intent; opt out with enabled = false).
func (c OutboundConfig) IsEnabled() bool { return c.Enabled == nil || *c.Enabled }

// Matches reports whether the integration subscribes to an event type.
func (c OutboundConfig) Matches(typ string) bool {
	for _, e := range c.Events {
		if e == "*" || e == typ {
			return true
		}
	}
	return false
}

// Validate checks the config; name must match the file it came from.
func (c OutboundConfig) Validate() error {
	if !nameRe.MatchString(c.Name) {
		return fmt.Errorf("invalid name %q", c.Name)
	}
	u, err := url.Parse(c.URL)
	if err != nil {
		return fmt.Errorf("invalid url: %w", err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("url must be http(s) with a host")
	}
	if len(c.Events) == 0 {
		return fmt.Errorf("events must not be empty")
	}
	for _, e := range c.Events {
		if !eventRe.MatchString(e) {
			return fmt.Errorf("invalid event type %q", e)
		}
	}
	return nil
}

// InboundConfig is one receive target: an external service POSTs the
// request body to /hooks/{slug}/{name}, signed with the integration
// secret, and noteroot writes it to Path.
type InboundConfig struct {
	Name    string `toml:"-"`    // directory entry base name
	Path    string `toml:"path"` // note path, relative to the vault
	Mode    string `toml:"mode"` // "append" (default) or "replace"
	Enabled *bool  `toml:"enabled"`
}

// IsEnabled defaults to true.
func (c InboundConfig) IsEnabled() bool { return c.Enabled == nil || *c.Enabled }

// Validate checks the config.
func (c InboundConfig) Validate() error {
	if !nameRe.MatchString(c.Name) {
		return fmt.Errorf("invalid name %q", c.Name)
	}
	if c.Path == "" {
		return fmt.Errorf("path must not be empty")
	}
	clean := filepath.ToSlash(filepath.Clean(c.Path))
	if clean != filepath.ToSlash(c.Path) || strings.HasPrefix(clean, "../") || clean == "." || strings.HasPrefix(clean, "/") {
		return fmt.Errorf("path must be a clean relative note path")
	}
	switch c.Mode {
	case "", "append", "replace":
	default:
		return fmt.Errorf("mode must be append or replace")
	}
	return nil
}

// Found is one discovered config file with its parse/validation state.
// A bad file is listed with a Problem and never fails boot — same
// degradation rule as plugins.
type Found struct {
	Name    string
	Problem string
	Out     *OutboundConfig
	In      *InboundConfig
}

// scanOut reads dir for outbound configs. A directory that does not
// exist is empty, not an error.
func scanOut(dir string) []Found {
	return scan(dir, false)
}

// scanIn reads dir for inbound configs.
func scanIn(dir string) []Found {
	return scan(dir, true)
}

func scan(dir string, inbound bool) []Found {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []Found
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ConfigFileExt) {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ConfigFileExt)
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			out = append(out, Found{Name: name, Problem: err.Error()})
			continue
		}
		if inbound {
			var c InboundConfig
			if _, err := toml.Decode(string(raw), &c); err != nil {
				out = append(out, Found{Name: name, Problem: err.Error()})
				continue
			}
			c.Name = name
			if err := c.Validate(); err != nil {
				out = append(out, Found{Name: name, Problem: err.Error()})
				continue
			}
			out = append(out, Found{Name: name, In: &c})
			continue
		}
		var c OutboundConfig
		if _, err := toml.Decode(string(raw), &c); err != nil {
			out = append(out, Found{Name: name, Problem: err.Error()})
			continue
		}
		c.Name = name
		if err := c.Validate(); err != nil {
			out = append(out, Found{Name: name, Problem: err.Error()})
			continue
		}
		out = append(out, Found{Name: name, Out: &c})
	}
	return out
}

// ErrIntegrationNotFound: no outbound config (or secret-less inert
// twin) with that name. ErrHookNotFound likewise for inbound; a hook
// without a secret is inert and reports not-found so unconfigured
// endpoints stay unguessable.
var (
	ErrIntegrationNotFound = errors.New("integration not found")
	ErrHookNotFound        = errors.New("hook not found")
	// ErrBadSignature: HMAC over the body did not match the secret.
	ErrBadSignature = errors.New("bad signature")
	// ErrDepthExceeded: the delivery's chain depth is already at the
	// bus limit — accepting the write would create content the event
	// bus is about to drop, the classic echo-loop failure mode.
	ErrDepthExceeded = errors.New("event chain depth exceeded")
)
