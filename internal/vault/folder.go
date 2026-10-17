package vault

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// checkFolderPath rejects folder names the tree would never show:
// hidden/ignored segments (dotfiles, editor temp suffixes) and a
// trailing ".md", which would be indistinguishable from a note.
func checkFolderPath(clean string) error {
	for _, seg := range strings.Split(clean, "/") {
		if Ignored(seg) {
			return fmt.Errorf("%q: hidden or temporary folder name", seg)
		}
		if strings.HasSuffix(strings.ToLower(seg), ".md") {
			return fmt.Errorf("%q: folder name may not end in .md", seg)
		}
	}
	return nil
}

// Mkdir creates a folder — and any missing parent levels — at a
// vault-relative path. Returns ErrConflict when the folder already
// exists; Rel validation errors propagate unchanged.
func (v *Vault) Mkdir(rel string) error {
	clean, err := Rel(rel)
	if err != nil {
		return err
	}
	if err := checkFolderPath(clean); err != nil {
		return err
	}
	parent, err := v.resolveParent(clean)
	if err != nil {
		return err
	}
	abs := filepath.Join(parent, path.Base(clean))
	if _, err := os.Lstat(abs); err == nil {
		return ErrConflict
	} else if !os.IsNotExist(err) {
		return err
	}
	mu := stripeFor(clean)
	mu.Lock()
	defer mu.Unlock()
	if err := os.Mkdir(abs, 0o755); err != nil {
		if os.IsExist(err) {
			return ErrConflict
		}
		return err
	}
	if d, err := os.Open(parent); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// RenameDir atomically moves a folder and every note under it to dst.
// dst must not exist; missing dst parents are created. Moving a folder
// into itself or a descendant is rejected with ErrConflict, and a src
// that is a file (or missing) yields ErrNotFile / ErrNotFound.
func (v *Vault) RenameDir(src, dst string) error {
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
	if strings.HasPrefix(cleanDst, cleanSrc+"/") {
		return ErrConflict
	}
	if err := checkFolderPath(cleanDst); err != nil {
		return fmt.Errorf("dst: %w", err)
	}

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
	if !fi.IsDir() {
		return ErrNotFile
	}

	dstParent, err := v.resolveParent(cleanDst)
	if err != nil {
		return err
	}
	dstAbs := filepath.Join(dstParent, path.Base(cleanDst))

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

	if _, err := os.Lstat(dstAbs); err == nil {
		return ErrConflict
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(srcAbs, dstAbs); err != nil {
		return err
	}
	if d, err := os.Open(dstParent); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	srcParent := filepath.Dir(srcAbs)
	if srcParent != dstParent {
		if d, err := os.Open(srcParent); err == nil {
			_ = d.Sync()
			_ = d.Close()
		}
	}
	return nil
}

// IsDir reports whether a vault-relative path exists and is a folder.
// Returns ErrNotFound when the path is absent.
func (v *Vault) IsDir(rel string) (bool, error) {
	clean, err := Rel(rel)
	if err != nil {
		return false, err
	}
	abs, err := v.resolve(clean)
	if err != nil {
		return false, err
	}
	fi, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return false, ErrNotFound
		}
		return false, err
	}
	return fi.IsDir(), nil
}

// RemoveDir removes a folder that holds no files — empty subfolders
// are removed with it (trashing a folder moves only its notes, leaving
// the directory skeleton behind). Returns ErrNotFound when it does not
// exist and ErrConflict when any file (including ignored ones) remains;
// in that case nothing is removed, so nothing is silently lost.
func (v *Vault) RemoveDir(rel string) error {
	clean, err := Rel(rel)
	if err != nil {
		return err
	}
	abs, err := v.resolve(clean)
	if err != nil {
		return err
	}
	fi, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return ErrNotFound
		}
		return err
	}
	if !fi.IsDir() {
		return ErrNotFile
	}
	mu := stripeFor(clean)
	mu.Lock()
	defer mu.Unlock()

	// Pre-order walk; anything that is not a plain directory (files,
	// symlinks) blocks removal before the first deletion happens.
	var dirs []string
	err = filepath.WalkDir(abs, func(p string, d os.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if !d.IsDir() {
			return ErrConflict
		}
		dirs = append(dirs, p)
		return nil
	})
	if err != nil {
		return err
	}
	// Children before parents. os.Remove refuses a non-empty directory,
	// so a file that appeared since the walk is never deleted.
	for i := len(dirs) - 1; i >= 0; i-- {
		if err := os.Remove(dirs[i]); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			if entries, rerr := os.ReadDir(dirs[i]); rerr == nil && len(entries) > 0 {
				return ErrConflict
			}
			return err
		}
	}
	if d, err := os.Open(filepath.Dir(abs)); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
