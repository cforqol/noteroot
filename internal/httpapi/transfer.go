package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"noteroot/internal/notebooks"
)

// Resumable notebook import/export jobs.
//
// An export job builds the .tar.gz to a file under the notebook's
// transfers dir instead of streaming straight to the response, so the
// download itself is just a Range-capable file read: a dropped
// connection costs nothing but the bytes already sent, and the client
// resumes with a plain Range request.
//
// An import job is the mirror image on the way in: the client PATCHes
// the archive in chunks, each one checked against the server's own
// count of bytes received so far (offset-fenced, tus-style) so a lost
// connection mid-upload can only ever be resumed, never duplicated or
// silently gapped. "Complete" then runs the staged file through the
// exact same validate → (dry run) plan → apply flow as the direct,
// single-shot POST /n/{slug}/import.
//
// Every job is a self-contained directory under AppDir/transfers,
// referenced by no database — same disposability as the rest of the
// notebook's derived state (ADR 0002). Abandoned jobs are swept by the
// GC ticker (see sweepTransfers) rather than tracked for an explicit
// expiry, so staleness is judged from the staged data's own mtime.

const (
	transferTTL = 48 * time.Hour

	exportArchiveName = "archive.tar.gz"
	exportPartName    = "archive.tar.gz.part"
	exportErrorName   = "error"

	importDataName = "upload.part"

	// maxImportChunkSize bounds one PATCH body: large enough that a
	// reasonable client needs few round trips, small enough that the
	// server never buffers more than this much of one chunk in RAM.
	maxImportChunkSize = 64 << 20
	// maxImportJobBytes is a safety ceiling on one staged upload's total
	// size, independent of the per-entry/per-archive caps enforced at
	// parse time in importArchiveStream — this one exists purely to stop
	// an abandoned or careless client from filling the disk one chunk at
	// a time before anything ever reads the archive back.
	maxImportJobBytes = 20 << 30
)

func newTransferID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func validTransferID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, c := range id {
		if c < '0' || (c > '9' && c < 'a') || c > 'f' {
			return false
		}
	}
	return true
}

func exportJobDir(nb *notebooks.Notebook, id string) string {
	return filepath.Join(nb.Layout.Transfers(), "export", id)
}

func importJobDir(nb *notebooks.Notebook, id string) string {
	return filepath.Join(nb.Layout.Transfers(), "import", id)
}

// --- Export jobs ---

// createExportJob handles POST /n/{slug}/export/jobs — starts building the
// notebook's export archive in the background and returns immediately with
// a job id to poll, so the HTTP connection that requested the export never
// has to stay open for however long the archive takes to build.
func (s *Server) createExportJob(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	nb, ok := s.reg.Get(slug)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such notebook"})
		return
	}
	// Same reasoning as exportNotebook: a full export walks the whole
	// vault, so any unopened lock would leak the notes it protects.
	if !s.lockGuard(w, r, slug, "") {
		return
	}

	id := newTransferID()
	dir := exportJobDir(nb, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "job create failed"})
		return
	}
	includeHistory := r.URL.Query().Get("include_history") != "false"
	go s.runExportJob(nb, dir, includeHistory)

	writeJSON(w, http.StatusAccepted, map[string]any{"id": id, "status": "building"})
}

// runExportJob builds the archive into dir in the background. It always
// writes to the ".part" name first and renames into place on success, so
// a status check never observes a half-written archive as "done".
func (s *Server) runExportJob(nb *notebooks.Notebook, dir string, includeHistory bool) {
	partPath := filepath.Join(dir, exportPartName)
	f, err := os.Create(partPath)
	if err != nil {
		s.failExportJob(dir, err)
		return
	}
	buildErr := s.buildExportArchive(nb, f, includeHistory)
	closeErr := f.Close()
	if buildErr == nil {
		buildErr = closeErr
	}
	if buildErr != nil {
		s.failExportJob(dir, buildErr)
		return
	}
	if err := os.Rename(partPath, filepath.Join(dir, exportArchiveName)); err != nil {
		s.failExportJob(dir, err)
		return
	}
}

func (s *Server) failExportJob(dir string, err error) {
	_ = os.Remove(filepath.Join(dir, exportPartName))
	_ = os.WriteFile(filepath.Join(dir, exportErrorName), []byte(err.Error()), 0o644)
	s.log.Warn("export job failed", "dir", dir, "err", err)
}

// exportJobState reports what readExportJobStatus found on disk.
type exportJobState struct {
	Status string `json:"status"` // "building" | "done" | "error" | "not_found"
	Size   int64  `json:"size,omitempty"`
	Error  string `json:"error,omitempty"`
}

// readExportJobStatus derives job state from the files present in dir —
// no separate metadata file to drift from what actually happened, same
// "files are truth" reasoning as the rest of the notebook's derived state.
func readExportJobStatus(dir string) exportJobState {
	if fi, err := os.Stat(filepath.Join(dir, exportArchiveName)); err == nil {
		return exportJobState{Status: "done", Size: fi.Size()}
	}
	if b, err := os.ReadFile(filepath.Join(dir, exportErrorName)); err == nil {
		return exportJobState{Status: "error", Error: string(b)}
	}
	// The job directory is created synchronously before the client ever
	// sees the id; the background builder may not have written its
	// ".part" file yet by the time the first status check lands, so the
	// directory's own existence — not the part file's — is what "still
	// building" actually means.
	if _, err := os.Stat(dir); err == nil {
		return exportJobState{Status: "building"}
	}
	return exportJobState{Status: "not_found"}
}

// exportJobStatus handles GET /n/{slug}/export/jobs/{id}.
func (s *Server) exportJobStatus(w http.ResponseWriter, r *http.Request) {
	nb, ok := s.reg.Get(r.PathValue("slug"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such notebook"})
		return
	}
	id := r.PathValue("id")
	if !validTransferID(id) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such job"})
		return
	}
	info := readExportJobStatus(exportJobDir(nb, id))
	if info.Status == "not_found" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such job"})
		return
	}
	writeJSON(w, http.StatusOK, info)
}

// downloadExportJob handles GET /n/{slug}/export/jobs/{id}/download. The
// finished archive is a plain file, so http.ServeContent gives the
// response Range/If-Range support for free — the piece that actually
// makes a multi-GB download resumable.
func (s *Server) downloadExportJob(w http.ResponseWriter, r *http.Request) {
	nb, ok := s.reg.Get(r.PathValue("slug"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such notebook"})
		return
	}
	id := r.PathValue("id")
	if !validTransferID(id) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such job"})
		return
	}
	dir := exportJobDir(nb, id)
	info := readExportJobStatus(dir)
	switch info.Status {
	case "not_found":
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such job"})
		return
	case "error":
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": info.Error})
		return
	case "building":
		writeJSON(w, http.StatusConflict, map[string]string{"error": "export not ready", "status": "building"})
		return
	}

	f, err := os.Open(filepath.Join(dir, exportArchiveName))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "job read failed"})
		return
	}
	defer func() { _ = f.Close() }()
	var modTime time.Time
	if fi, statErr := f.Stat(); statErr == nil {
		modTime = fi.ModTime()
	}

	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+safeFilename(nb.Manifest.Slug)+`.tar.gz"`)
	http.ServeContent(w, r, exportArchiveName, modTime, f)
}

// deleteExportJob handles DELETE /n/{slug}/export/jobs/{id} — cancels a
// build in progress or discards a finished one the client no longer needs.
func (s *Server) deleteExportJob(w http.ResponseWriter, r *http.Request) {
	nb, ok := s.reg.Get(r.PathValue("slug"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such notebook"})
		return
	}
	id := r.PathValue("id")
	if !validTransferID(id) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such job"})
		return
	}
	dir := exportJobDir(nb, id)
	if _, err := os.Stat(dir); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such job"})
		return
	}
	if err := os.RemoveAll(dir); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "job delete failed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

// --- Import jobs ---

// createImportJob handles POST /n/{slug}/import/jobs — opens a staging
// area for a chunked upload and returns its id and starting offset (0).
func (s *Server) createImportJob(w http.ResponseWriter, r *http.Request) {
	nb, ok := s.reg.Get(r.PathValue("slug"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such notebook"})
		return
	}
	id := newTransferID()
	dir := importJobDir(nb, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "job create failed"})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "offset": 0})
}

// importJobOffset is the number of bytes received so far: the staged
// file's own size, never a separately tracked counter that could drift
// from it.
func importJobOffset(dir string) (int64, error) {
	fi, err := os.Stat(filepath.Join(dir, importDataName))
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return fi.Size(), nil
}

// appendImportJob handles PATCH /n/{slug}/import/jobs/{id} — appends one
// chunk of the archive being uploaded.
//
// The caller declares the offset it believes the upload is at via the
// X-Noteroot-Offset request header; a mismatch means the client's view is
// stale (its last append never reached the server, or reached it twice)
// and gets 409 with the server's true offset instead of a silent gap or
// a duplicate write — the same fencing tus.io uses for resumable uploads.
func (s *Server) appendImportJob(w http.ResponseWriter, r *http.Request) {
	nb, ok := s.reg.Get(r.PathValue("slug"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such notebook"})
		return
	}
	id := r.PathValue("id")
	if !validTransferID(id) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such job"})
		return
	}
	dir := importJobDir(nb, id)
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such job"})
		return
	}

	offset, offsetErr := parseOffsetHeader(r.Header.Get("X-Noteroot-Offset"))
	if offsetErr != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing or invalid X-Noteroot-Offset"})
		return
	}
	current, err := importJobOffset(dir)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "job read failed"})
		return
	}
	if offset != current {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "offset mismatch", "offset": current})
		return
	}

	chunk, readErr := io.ReadAll(io.LimitReader(r.Body, maxImportChunkSize+1))
	if readErr != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "read failed: " + readErr.Error()})
		return
	}
	if int64(len(chunk)) > maxImportChunkSize {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "chunk too large"})
		return
	}
	if current+int64(len(chunk)) > maxImportJobBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "upload too large"})
		return
	}

	f, err := os.OpenFile(filepath.Join(dir, importDataName), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "job write failed"})
		return
	}
	_, writeErr := f.Write(chunk)
	closeErr := f.Close()
	if writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "write failed: " + writeErr.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"offset": current + int64(len(chunk))})
}

func parseOffsetHeader(v string) (int64, error) {
	if v == "" {
		return 0, errors.New("missing offset")
	}
	var n int64
	for _, c := range v {
		if c < '0' || c > '9' {
			return 0, errors.New("invalid offset")
		}
		n = n*10 + int64(c-'0')
	}
	return n, nil
}

// importJobStatus handles GET /n/{slug}/import/jobs/{id} — the offset a
// resuming client should continue from.
func (s *Server) importJobStatus(w http.ResponseWriter, r *http.Request) {
	nb, ok := s.reg.Get(r.PathValue("slug"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such notebook"})
		return
	}
	id := r.PathValue("id")
	if !validTransferID(id) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such job"})
		return
	}
	dir := importJobDir(nb, id)
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such job"})
		return
	}
	offset, err := importJobOffset(dir)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "job read failed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "offset": offset})
}

// completeImportJob handles POST /n/{slug}/import/jobs/{id}/complete —
// runs the staged upload through the same validate → (dry run) plan →
// apply flow as the direct POST /n/{slug}/import, then returns exactly
// the same response shape. A dry run leaves the job in place (nothing
// was consumed, and the staged bytes are still good for a real complete
// right after); applying removes it, since the archive has now done its
// one job.
func (s *Server) completeImportJob(w http.ResponseWriter, r *http.Request) {
	nb, ok := s.reg.Get(r.PathValue("slug"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such notebook"})
		return
	}
	id := r.PathValue("id")
	if !validTransferID(id) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such job"})
		return
	}
	dir := importJobDir(nb, id)
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such job"})
		return
	}

	f, err := os.Open(filepath.Join(dir, importDataName))
	if errors.Is(err, os.ErrNotExist) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no data uploaded"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "job read failed"})
		return
	}
	defer func() { _ = f.Close() }()

	dryRun := r.URL.Query().Get("dry_run") == "true"
	ok2 := s.importArchiveStream(w, r, nb, f, dryRun)
	if ok2 && !dryRun {
		_ = os.RemoveAll(dir)
	}
}

// deleteImportJob handles DELETE /n/{slug}/import/jobs/{id} — cancels an
// upload in progress and discards its staged bytes.
func (s *Server) deleteImportJob(w http.ResponseWriter, r *http.Request) {
	nb, ok := s.reg.Get(r.PathValue("slug"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such notebook"})
		return
	}
	id := r.PathValue("id")
	if !validTransferID(id) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such job"})
		return
	}
	dir := importJobDir(nb, id)
	if _, err := os.Stat(dir); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such job"})
		return
	}
	if err := os.RemoveAll(dir); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "job delete failed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

// --- GC ---

// sweepTransfers removes abandoned import/export job directories whose
// staged data has gone untouched past transferTTL. Staleness is judged
// from the data file's own mtime (bumped by every chunk append, or by
// the rename that finishes a build) rather than the job directory's,
// which most filesystems leave alone across writes to an existing child
// file; a job that never got as far as its first byte falls back to the
// directory's own mtime from creation.
func (s *Server) sweepTransfers(nb *notebooks.Notebook) int {
	n := sweepTransferKind(filepath.Join(nb.Layout.Transfers(), "export"),
		[]string{exportArchiveName, exportPartName, exportErrorName})
	n += sweepTransferKind(filepath.Join(nb.Layout.Transfers(), "import"), []string{importDataName})
	return n
}

func sweepTransferKind(baseDir string, dataNames []string) int {
	entries, err := os.ReadDir(baseDir)
	if err != nil {
		return 0
	}
	cutoff := time.Now().Add(-transferTTL)
	n := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(baseDir, e.Name())
		modTime, ok := latestDataModTime(dir, dataNames)
		if !ok {
			continue
		}
		if modTime.Before(cutoff) {
			if os.RemoveAll(dir) == nil {
				n++
			}
		}
	}
	return n
}

// latestDataModTime finds the first present file in dataNames and
// reports its mtime, falling back to the job directory's own mtime when
// none of them have been written yet.
func latestDataModTime(dir string, dataNames []string) (time.Time, bool) {
	for _, name := range dataNames {
		if fi, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return fi.ModTime(), true
		}
	}
	if fi, err := os.Stat(dir); err == nil {
		return fi.ModTime(), true
	}
	return time.Time{}, false
}
