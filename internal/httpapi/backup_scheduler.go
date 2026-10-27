package httpapi

import (
	"context"
	"time"

	"noteroot/internal/backup"
	"noteroot/internal/resources"
)

// startBackupScheduler launches a background goroutine that runs a backup
// of every notebook at the configured interval. Returns a no-op if
// BackupSchedule is empty or "0". Mirrors startGC in structure.
func (s *Server) startBackupScheduler() (stop func()) {
	if s.cfg.BackupSchedule == "" || s.cfg.BackupSchedule == "0" {
		return func() {}
	}
	d, err := time.ParseDuration(s.cfg.BackupSchedule)
	if err != nil || d <= 0 {
		s.log.Warn("backup scheduler: bad schedule, disabled", "schedule", s.cfg.BackupSchedule)
		return func() {}
	}

	done := make(chan struct{})
	go func() {
		t := time.NewTicker(d)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				s.runBackupSweep()
			case <-done:
				return
			}
		}
	}()
	s.log.Info("backup scheduler started", "interval", d)
	return func() { close(done) }
}

// runBackupSweep runs backup.Create for every notebook and, when S3 is
// configured, uploads the resulting files with backup.S3Uploader.
func (s *Server) runBackupSweep() {
	ctx := context.Background()

	var uploader *backup.S3Uploader
	if s.cfg.S3Enabled() {
		s3Cfg := resources.S3Config{
			Endpoint:  s.cfg.S3Endpoint,
			Bucket:    s.cfg.S3Bucket,
			Region:    s.cfg.S3Region,
			AccessKey: s.cfg.S3AccessKey,
			SecretKey: s.cfg.S3SecretKey,
			PathStyle: s.cfg.S3PathStyle,
			SSE:       s.cfg.S3SSE,
			SSEKeyID:  s.cfg.S3SSEKeyID,
		}
		store, err := resources.NewS3Store(s3Cfg)
		if err != nil {
			s.log.Error("backup sweep: open S3 store failed", "err", err)
		} else {
			uploader = backup.NewS3Uploader(store, "backups/")
		}
	}

	for _, nb := range s.reg.List() {
		slug := nb.Manifest.Slug
		m, err := backup.Create(ctx, nb.Layout.Vault(), nb.Layout.StateDB(), nb.Layout.Backups())
		if err != nil {
			s.log.Error("backup sweep: create failed", "notebook", slug, "err", err)
			continue
		}
		if len(m.Errors) > 0 {
			for _, e := range m.Errors {
				s.log.Warn("backup sweep: partial error", "notebook", slug, "warn", e)
			}
		}
		s.log.Info("backup sweep: created", "notebook", slug, "bundle", m.BundlePath)
		if uploader != nil {
			if err := uploader.Upload(ctx, m, nb.Layout.Backups()); err != nil {
				s.log.Error("backup sweep: S3 upload failed", "notebook", slug, "err", err)
			} else {
				s.log.Info("backup sweep: uploaded to S3", "notebook", slug)
			}
		}
	}
}
