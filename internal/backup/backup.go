// Package backup creates per-notebook point-in-time backups consisting of:
//   - a git bundle of the vault (all refs)
//   - a SQLite VACUUM INTO snapshot of state.db
//
// v1 writes backups locally to .notebook/backups/. No remote destination.
package backup

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	_ "modernc.org/sqlite"
)

// Manifest describes one backup run.
type Manifest struct {
	// CreatedAt is when the backup was taken (UTC).
	CreatedAt time.Time `json:"createdAt"`
	// BundlePath is the path to the git bundle, relative to the backup dir.
	BundlePath string `json:"bundlePath,omitempty"`
	// BundleSHA is the sha256 hex digest of the bundle file.
	BundleSHA string `json:"bundleSHA,omitempty"`
	// StatePath is the path to the state DB snapshot.
	StatePath string `json:"statePath,omitempty"`
	// StateSHA is the sha256 hex digest of the state snapshot.
	StateSHA string `json:"stateSHA,omitempty"`
	// Errors contains non-fatal warnings from the backup run.
	Errors []string `json:"errors,omitempty"`
}

// Create takes a backup of vaultDir (git) and stateDB (SQLite) into outDir.
// The timestamp prefix keeps multiple backups in the same directory.
// Partial failures are recorded in Manifest.Errors rather than aborting.
func Create(ctx context.Context, vaultDir, stateDB, outDir string) (Manifest, error) {
	if err := os.MkdirAll(outDir, 0o750); err != nil {
		return Manifest{}, fmt.Errorf("backup mkdir: %w", err)
	}

	ts := time.Now().UTC()
	m := Manifest{CreatedAt: ts}

	// Two backups in the same second must not share filenames. Probe up
	// to 100 suffixed stamps; beyond that something else is wrong.
	stamp := ""
	for i := 0; i < 100; i++ {
		candidate := ts.Format("20060102T150405Z")
		if i > 0 {
			candidate += "-" + strconv.Itoa(i+1)
		}
		if _, err := os.Lstat(filepath.Join(outDir, candidate+"-vault.bundle")); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			return Manifest{}, fmt.Errorf("backup probe: %w", err)
		}
		if _, err := os.Lstat(filepath.Join(outDir, candidate+"-state.db")); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			return Manifest{}, fmt.Errorf("backup probe: %w", err)
		}
		stamp = candidate
		break
	}
	if stamp == "" {
		return Manifest{}, fmt.Errorf("backup stamp exhausted")
	}

	// git bundle.
	bundleName := stamp + "-vault.bundle"
	bundlePath := filepath.Join(outDir, bundleName)
	if err := gitBundle(ctx, vaultDir, bundlePath); err != nil {
		m.Errors = append(m.Errors, fmt.Sprintf("git bundle: %v", err))
	} else {
		sha, err := sha256File(bundlePath)
		if err != nil {
			m.Errors = append(m.Errors, fmt.Sprintf("bundle sha: %v", err))
		}
		m.BundlePath = bundleName
		m.BundleSHA = sha
	}

	// SQLite VACUUM INTO snapshot.
	stateName := stamp + "-state.db"
	statePath := filepath.Join(outDir, stateName)
	if err := vacuumInto(ctx, stateDB, statePath); err != nil {
		m.Errors = append(m.Errors, fmt.Sprintf("vacuum into: %v", err))
	} else {
		sha, err := sha256File(statePath)
		if err != nil {
			m.Errors = append(m.Errors, fmt.Sprintf("state sha: %v", err))
		}
		m.StatePath = stateName
		m.StateSHA = sha
	}

	return m, nil
}

// gitBundle runs "git bundle create <dst> --all" inside vaultDir.
func gitBundle(ctx context.Context, vaultDir, dst string) error {
	cmd := exec.CommandContext(ctx, "git", "bundle", "create", dst, "--all")
	cmd.Dir = vaultDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, out)
	}
	return nil
}

// vacuumInto snapshots src SQLite database to dst using VACUUM INTO.
// VACUUM INTO requires a read-write connection even though it only reads
// from src (SQLite limitation with the pure-Go driver).
func vacuumInto(ctx context.Context, src, dst string) error {
	db, err := sql.Open("sqlite", "file:"+src+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return fmt.Errorf("open src: %w", err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.ExecContext(ctx, `VACUUM INTO ?`, dst); err != nil {
		return fmt.Errorf("vacuum into: %w", err)
	}
	return nil
}

// sha256File returns the hex sha256 of a file.
func sha256File(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
