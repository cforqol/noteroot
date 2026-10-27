package backup

import (
	"context"
	"fmt"
	"os"
	"path"

	"noteroot/internal/resources"
)

// S3Uploader uploads a completed backup manifest to S3-compatible storage.
// It uses resources.S3Store.PutObject so that the store's SSE configuration
// is applied to every backup object.
type S3Uploader struct {
	store  *resources.S3Store
	prefix string // key prefix, e.g. "backups/"
}

// NewS3Uploader creates an uploader against an existing S3Store.
// prefix is prepended to every object key (must end with "/").
func NewS3Uploader(store *resources.S3Store, prefix string) *S3Uploader {
	if prefix == "" {
		prefix = "backups/"
	}
	return &S3Uploader{store: store, prefix: prefix}
}

// Upload copies the files described by m (from the local backup directory outDir)
// to S3.  Both bundle and state are uploaded when present; partial failures are
// collected and returned as a combined error so neither file blocks the other.
func (u *S3Uploader) Upload(ctx context.Context, m Manifest, outDir string) error {
	var errs []error
	if m.BundlePath != "" {
		if err := u.uploadFile(ctx, outDir, m.BundlePath); err != nil {
			errs = append(errs, fmt.Errorf("bundle: %w", err))
		}
	}
	if m.StatePath != "" {
		if err := u.uploadFile(ctx, outDir, m.StatePath); err != nil {
			errs = append(errs, fmt.Errorf("state: %w", err))
		}
	}
	switch len(errs) {
	case 0:
		return nil
	case 1:
		return errs[0]
	default:
		return fmt.Errorf("%v; %v", errs[0], errs[1])
	}
}

func (u *S3Uploader) uploadFile(ctx context.Context, dir, relPath string) error {
	localPath := relPath
	if dir != "" {
		localPath = dir + "/" + relPath
	}
	f, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("open %s: %w", localPath, err)
	}
	defer func() { _ = f.Close() }()

	fi, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat %s: %w", localPath, err)
	}

	key := u.prefix + path.Base(relPath)
	ct := contentTypeFor(relPath)
	return u.store.PutObject(ctx, key, f, fi.Size(), ct)
}

func contentTypeFor(name string) string {
	switch {
	case len(name) > 7 && name[len(name)-7:] == ".bundle":
		return "application/octet-stream"
	case len(name) > 3 && name[len(name)-3:] == ".db":
		return "application/x-sqlite3"
	default:
		return "application/octet-stream"
	}
}
