package vault

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ErrConflict reports a failed write precondition: the note changed (or
// appeared) underneath the base version the write was based on.
var ErrConflict = errors.New("precondition failed")

// WriteOpts tunes a single Write call.
type WriteOpts struct {
	// BaseSha is the hex sha256 the write is based on. "" disables the
	// check (unconditional write). A sha for a missing file never matches.
	BaseSha string
	// Actor records who made the change; used for git blame lines.
	Actor string
}

// WriteResult describes what a Write did.
type WriteResult struct {
	Sha     string // hex sha256 of the written bytes
	Size    int64
	ModTime time.Time
	Created bool
}

// writeStripes serializes concurrent writes to the same path. In-memory is
// sufficient: one notebook is served by one process.
var writeStripes [64]sync.Mutex

func stripeFor(clean string) *sync.Mutex {
	h := fnv.New32a()
	_, _ = h.Write([]byte(clean))
	return &writeStripes[h.Sum32()%uint32(len(writeStripes))]
}

func shaHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Write stores content at a vault-relative path atomically: temp file in
// the same directory, fsync, rename, directory fsync. No failure leaves a
// partial file that looks valid.
func (v *Vault) Write(rel string, content []byte, opts WriteOpts) (WriteResult, error) {
	clean, err := Rel(rel)
	if err != nil {
		return WriteResult{}, err
	}
	if len(content) > MaxReadBytes {
		return WriteResult{}, ErrTooLarge
	}

	mu := stripeFor(clean)
	mu.Lock()
	defer mu.Unlock()

	parent, err := v.resolveParent(clean)
	if err != nil {
		return WriteResult{}, err
	}
	target := filepath.Join(parent, path.Base(clean))

	created := false
	if opts.BaseSha != "" {
		cur, err := os.ReadFile(target)
		if err != nil {
			return WriteResult{}, ErrConflict
		}
		if shaHex(cur) != opts.BaseSha {
			return WriteResult{}, ErrConflict
		}
	} else if _, err := os.Lstat(target); err != nil {
		if !os.IsNotExist(err) {
			return WriteResult{}, err
		}
		created = true
	}

	tmp, err := os.CreateTemp(parent, "."+path.Base(clean)+".tmp.*")
	if err != nil {
		return WriteResult{}, err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return WriteResult{}, err
	}
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return WriteResult{}, err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return WriteResult{}, err
	}
	if err := tmp.Close(); err != nil {
		return WriteResult{}, err
	}
	if err := os.Rename(tmpName, target); err != nil {
		return WriteResult{}, err
	}
	if dir, err := os.Open(parent); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}

	fi, err := os.Stat(target)
	if err != nil {
		return WriteResult{}, err
	}
	return WriteResult{
		Sha:     shaHex(content),
		Size:    fi.Size(),
		ModTime: fi.ModTime(),
		Created: created,
	}, nil
}

// Rename atomically moves a vault-relative file from src to dst. dst must
// not already exist; its parent directories are created if needed.
// Returns ErrNotFound if src is absent, ErrConflict if dst exists, and
// ErrEscapes / an error from Rel if either path is invalid.
func (v *Vault) Rename(src, dst string) error {
	cleanSrc, err := Rel(src)
	if err != nil {
		return fmt.Errorf("src: %w", err)
	}
	cleanDst, err := Rel(dst)
	if err != nil {
		return fmt.Errorf("dst: %w", err)
	}
	if cleanSrc == cleanDst {
		return nil
	}

	// Resolve src (must exist inside vault).
	srcAbs, err := v.resolve(cleanSrc)
	if err != nil {
		return err
	}
	fi, err := os.Stat(srcAbs)
	if err != nil {
		if os.IsNotExist(err) {
			return ErrNotFound
		}
		return err
	}
	if fi.IsDir() {
		return ErrNotFile
	}

	// Dst parent directory (creates missing levels; stays inside vault
	// because cleanDst has no ".." components after Rel validation).
	dstParent, err := v.resolveParent(cleanDst)
	if err != nil {
		return err
	}
	dstAbs := filepath.Join(dstParent, path.Base(cleanDst))

	// Lock both stripes in a deterministic order so the dst-exists check
	// and the rename below are atomic against concurrent renames/writes
	// to dst.
	muSrc, muDst := stripeFor(cleanSrc), stripeFor(cleanDst)
	if muSrc == muDst {
		muSrc.Lock()
		defer muSrc.Unlock()
	} else if cleanSrc < cleanDst {
		muSrc.Lock()
		defer muSrc.Unlock()
		muDst.Lock()
		defer muDst.Unlock()
	} else {
		muDst.Lock()
		defer muDst.Unlock()
		muSrc.Lock()
		defer muSrc.Unlock()
	}

	// Fail if dst already exists.
	if _, err := os.Lstat(dstAbs); err == nil {
		return ErrConflict
	} else if !os.IsNotExist(err) {
		return err
	}

	if err := os.Rename(srcAbs, dstAbs); err != nil {
		return err
	}
	// Sync the parent dirs on both sides so the change is visible after a
	// crash (best-effort, errors are non-fatal).
	if dir, err := os.Open(dstParent); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	srcParent := filepath.Dir(srcAbs)
	if srcParent != dstParent {
		if dir, err := os.Open(srcParent); err == nil {
			_ = dir.Sync()
			_ = dir.Close()
		}
	}
	return nil
}

// resolveParent returns the absolute parent directory for a cleaned
// vault-relative path, creating missing levels. Every level is checked, so
// a symlink inside the vault can never divert the write outside it.
func (v *Vault) resolveParent(clean string) (string, error) {
	root, err := filepath.EvalSymlinks(v.root)
	if err != nil {
		if os.IsNotExist(err) {
			return "", ErrNotFound
		}
		return "", err
	}
	dir := path.Dir(clean)
	if dir == "." {
		return root, nil
	}
	cur := root
	for _, seg := range strings.Split(dir, "/") {
		next := filepath.Join(cur, seg)
		fi, err := os.Lstat(next)
		switch {
		case err == nil:
			if fi.Mode()&os.ModeSymlink != 0 {
				next, err = filepath.EvalSymlinks(next)
				if err != nil {
					if os.IsNotExist(err) {
						return "", ErrNotFound
					}
					return "", err
				}
			} else if !fi.IsDir() {
				return "", errors.New("parent is not a directory")
			}
		case os.IsNotExist(err):
			if err := os.Mkdir(next, 0o755); err != nil {
				return "", err
			}
		default:
			return "", err
		}
		if next != root && !strings.HasPrefix(next, root+string(filepath.Separator)) {
			return "", ErrEscapes
		}
		cur = next
	}
	return cur, nil
}
