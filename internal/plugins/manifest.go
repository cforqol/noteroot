// Package plugins is the sandboxed in-process Lua extension runtime
// (stage 9 phase 3): one Lua VM per notebook plugin, serialized behind
// a per-plugin mutex, every invocation timeout-bounded by the VM
// context and counted by a circuit breaker, so no plugin can block a
// save or take the server down with it.
//
// Plugins are read-only by default: every capability call checks the
// manifest's declared permissions (the same resource:action[:qualifier]
// grammar API tokens use) at call time.
package plugins

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"

	"noteroot/internal/platform"
)

const (
	// ManifestFile names the manifest inside a plugin directory.
	ManifestFile = "plugin.toml"
	// DefaultEntry is the Lua entry script when manifest.entry is unset.
	DefaultEntry = "plugin.lua"
)

// Manifest is plugin.toml: identity, entry script, declared
// permissions. Identity is validated against the directory name so a
// plugin cannot masquerade under another's directory.
type Manifest struct {
	Name        string   `toml:"name"`
	Version     string   `toml:"version"`
	Description string   `toml:"description"`
	Entry       string   `toml:"entry"`
	Permissions []string `toml:"permissions"`
	Enabled     *bool    `toml:"enabled"` // nil = enabled
}

var nameRe = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// IsEnabled defaults to true: dropping a plugin in is approval to run
// it; `enabled = false` is the opt-out.
func (m Manifest) IsEnabled() bool {
	return m.Enabled == nil || *m.Enabled
}

// Grants parses the declared permissions. Parse is strict (platform
// rejects anything ambiguous).
func (m Manifest) Grants() ([]platform.Grant, error) {
	return platform.ParseGrants(strings.Join(m.Permissions, " "))
}

// ParseManifest decodes plugin.toml without touching the filesystem.
func ParseManifest(data []byte) (Manifest, error) {
	var m Manifest
	if err := toml.Unmarshal(data, &m); err != nil {
		return Manifest{}, fmt.Errorf("parse %s: %w", ManifestFile, err)
	}
	return m, nil
}

// Validate checks the manifest against the directory it lives in.
// dir is the plugin's directory; the returned entry is the absolute
// path of the Lua script, guaranteed inside dir.
func (m Manifest) Validate(dir string) (entry string, err error) {
	if !nameRe.MatchString(m.Name) {
		return "", fmt.Errorf("bad plugin name %q: want lower-case ident", m.Name)
	}
	if base := filepath.Base(dir); base != m.Name {
		return "", fmt.Errorf("manifest name %q does not match directory %q", m.Name, base)
	}
	if m.Version == "" {
		return "", fmt.Errorf("plugin %s: version required", m.Name)
	}
	if _, err := m.Grants(); err != nil {
		return "", fmt.Errorf("plugin %s: %w", m.Name, err)
	}

	rel := m.Entry
	if rel == "" {
		rel = DefaultEntry
	}
	if filepath.IsAbs(rel) || filepath.Clean(rel) != rel || rel == "." || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("plugin %s: bad entry %q", m.Name, m.Entry)
	}
	abs := filepath.Join(dir, rel)
	if !strings.HasPrefix(abs, filepath.Clean(dir)+string(filepath.Separator)) {
		return "", fmt.Errorf("plugin %s: entry escapes directory", m.Name)
	}
	return abs, nil
}

// Found is one discovered plugin directory: everything needed to list
// it, whether or not it loads.
type Found struct {
	Dir      string
	Manifest Manifest
	// Problem is a load-blocking validation error ("" when valid).
	Problem string
}

// Scan lists immediate subdirectories of root that carry a manifest.
// A directory without plugin.toml is ignored (not an error — users
// keep notes and drafts around); a malformed manifest is recorded as
// a Problem, never a boot failure.
func Scan(root string) []Found {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []Found
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		data, err := os.ReadFile(filepath.Join(dir, ManifestFile))
		if err != nil {
			continue // no manifest: not a plugin
		}
		f := Found{Dir: dir}
		m, err := ParseManifest(data)
		if err != nil {
			f.Problem = err.Error()
		} else {
			f.Manifest = m
			if _, vErr := m.Validate(dir); vErr != nil {
				f.Problem = vErr.Error()
			}
		}
		out = append(out, f)
	}
	return out
}
