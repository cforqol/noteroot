package httpapi

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"noteroot/internal/git"
	"noteroot/internal/markdown"
	"noteroot/internal/notebooks"
	"noteroot/internal/resources"
	"noteroot/internal/vault"
)

// Attachments travel in the archive as resources/<sha> blobs plus a
// resources.json manifest (filename and MIME per blob). The manifest is
// written first so an importer can stream the blobs that follow.
const (
	resourceManifestName = "resources.json"
	resourceDirPrefix    = "resources/"
	historyBundleName    = "history.bundle"
)

// shaPattern matches 64-character lowercase hex strings (SHA-256 digests).
var shaPattern = regexp.MustCompile(`\b[0-9a-f]{64}\b`)

type resourceMeta struct {
	SHA      string `json:"sha"`
	Filename string `json:"filename"`
	MIME     string `json:"mime"`
	Size     int64  `json:"size"`
}

// exportResources writes the notebook's committed attachments into tw.
// Trouble here is logged and skipped: the notes already written must
// still come out as a valid archive.
func (s *Server) exportResources(r *http.Request, slug string, tw *tar.Writer) {
	nb, ok := s.reg.Get(slug)
	if !ok {
		return
	}
	store, journal, cleanup, err := s.openResources(r.Context(), nb)
	if err != nil {
		s.log.Warn("export: resources unavailable", "notebook", slug, "err", err)
		return
	}
	defer cleanup()
	entries, err := journal.List(resources.StateCommitted, 1<<20)
	if err != nil {
		s.log.Warn("export: journal read failed", "notebook", slug, "err", err)
		return
	}
	var metas []resourceMeta
	seen := map[string]bool{}
	for _, e := range entries { // newest first: the newest filename wins
		if e.SHA == "" || seen[e.SHA] || !store.Has(e.SHA) {
			continue
		}
		size, serr := store.Size(e.SHA)
		if serr != nil {
			continue
		}
		seen[e.SHA] = true
		metas = append(metas, resourceMeta{SHA: e.SHA, Filename: e.Filename, MIME: e.MIME, Size: size})
	}
	if len(metas) == 0 {
		return
	}
	mf, _ := json.Marshal(metas)
	if tw.WriteHeader(&tar.Header{Name: resourceManifestName, Mode: 0o644, Size: int64(len(mf))}) != nil {
		return
	}
	if _, err := tw.Write(mf); err != nil {
		return
	}
	for _, m := range metas {
		rc, gerr := store.Get(m.SHA)
		if gerr != nil {
			// Listed in the manifest but unreadable: stop rather than
			// write a short entry that would corrupt the stream.
			s.log.Warn("export: blob unreadable", "sha", m.SHA, "err", gerr)
			return
		}
		herr := tw.WriteHeader(&tar.Header{Name: resourceDirPrefix + m.SHA, Mode: 0o644, Size: m.Size})
		if herr == nil {
			_, herr = io.CopyN(tw, rc, m.Size)
		}
		_ = rc.Close()
		if herr != nil {
			s.log.Warn("export: blob copy failed", "sha", m.SHA, "err", herr)
			return
		}
	}
}

// noteResourceSHAs returns the set of resource SHAs referenced in content
// that are present as committed blobs in the store. The scan is a simple
// substring search: any 64-char hex run that is a known blob is included.
func noteResourceSHAs(content []byte, store resources.Store, journal *resources.Journal) []resourceMeta {
	matches := shaPattern.FindAll(content, -1)
	if len(matches) == 0 {
		return nil
	}
	var shas []string
	seen := map[string]bool{}
	for _, m := range matches {
		s := string(m)
		if !seen[s] && store.Has(s) {
			seen[s] = true
			shas = append(shas, s)
		}
	}
	if len(shas) == 0 {
		return nil
	}
	mimeMap, err := journal.MIMEBySHA(shas...)
	if err != nil {
		mimeMap = map[string]string{}
	}
	entries, err := journal.List(resources.StateCommitted, 1<<20)
	if err != nil {
		entries = nil
	}
	// Build filename lookup from journal entries (newest filename wins).
	type meta struct{ filename, mime string }
	filenameBySHA := map[string]meta{}
	for _, e := range entries {
		if _, ok := seen[e.SHA]; ok {
			if _, already := filenameBySHA[e.SHA]; !already {
				filenameBySHA[e.SHA] = meta{filename: e.Filename, mime: e.MIME}
			}
		}
	}
	var out []resourceMeta
	for _, sha := range shas {
		size, serr := store.Size(sha)
		if serr != nil {
			continue
		}
		m := filenameBySHA[sha]
		mime := m.mime
		if mime == "" {
			mime = mimeMap[sha]
		}
		filename := m.filename
		if filename == "" {
			filename = sha
		}
		out = append(out, resourceMeta{SHA: sha, Filename: filename, MIME: mime, Size: size})
	}
	return out
}

// exportNoteArchive handles GET /n/{slug}/note/export/archive?path=... —
// streams a .tar.gz containing the note's markdown plus any attachment blobs
// it references (same layout as the notebook-level export).
func (s *Server) exportNoteArchive(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	nb, ok := s.reg.Get(slug)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such notebook"})
		return
	}
	path := r.URL.Query().Get("path")
	if path == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing path"})
		return
	}
	if !s.lockGuard(w, r, slug, path) {
		return
	}

	v := vault.Open(nb.Layout.Vault())
	content, err := v.Read(path)
	if err != nil {
		writeJSON(w, statusFor(err), map[string]string{"error": publicError(err)})
		return
	}

	baseName := strings.TrimSuffix(filepath.Base(path), ".md")
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+safeFilename(baseName)+`.tar.gz"`)
	w.WriteHeader(http.StatusOK)

	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)

	// Write the note itself.
	_ = tw.WriteHeader(&tar.Header{Name: path, Mode: 0o644, Size: int64(len(content))})
	_, _ = tw.Write(content)

	// Write referenced attachments (best-effort: failures are logged and skipped).
	store, journal, cleanup, resErr := s.openResources(r.Context(), nb)
	if resErr == nil {
		defer cleanup()
		metas := noteResourceSHAs(content, store, journal)
		if len(metas) > 0 {
			mf, _ := json.Marshal(metas)
			if tw.WriteHeader(&tar.Header{Name: resourceManifestName, Mode: 0o644, Size: int64(len(mf))}) == nil {
				_, _ = tw.Write(mf)
			}
			for _, m := range metas {
				rc, gerr := store.Get(m.SHA)
				if gerr != nil {
					s.log.Warn("note export: blob unreadable", "sha", m.SHA, "err", gerr)
					continue
				}
				herr := tw.WriteHeader(&tar.Header{Name: resourceDirPrefix + m.SHA, Mode: 0o644, Size: m.Size})
				if herr == nil {
					_, herr = io.CopyN(tw, rc, m.Size)
				}
				_ = rc.Close()
				if herr != nil {
					s.log.Warn("note export: blob copy failed", "sha", m.SHA, "err", herr)
				}
			}
		}
	}

	_ = tw.Close()
	_ = gz.Close()
}

// importNoteArchive handles POST /n/{slug}/note/import — accepts a .tar.gz
// produced by exportNoteArchive and writes the note and its attachments.
// Supports dry_run=true. Returns the same importEntry slice as importNotebook.
func (s *Server) importNoteArchive(w http.ResponseWriter, r *http.Request) {
	nb, ok := s.reg.Get(r.PathValue("slug"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such notebook"})
		return
	}
	dryRun := r.URL.Query().Get("dry_run") == "true"

	var archiveReader io.Reader
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad multipart"})
			return
		}
		f, _, err := r.FormFile("file")
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing file field"})
			return
		}
		defer func() { _ = f.Close() }()
		archiveReader = f
	} else {
		archiveReader = r.Body
	}

	gz, err := gzip.NewReader(archiveReader)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "not a gzip archive: " + err.Error()})
		return
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)

	const maxEntrySize = 8 << 20

	type staged struct {
		path    string
		content []byte
		sha     string
	}
	var validNote *staged
	var results []importEntry

	var (
		resStore   resources.Store
		resJournal *resources.Journal
		resOpenErr error
		resCleanup func()
		manifest   = map[string]resourceMeta{}
	)
	defer func() {
		if resCleanup != nil {
			resCleanup()
		}
	}()
	openRes := func() bool {
		if resStore != nil {
			return true
		}
		if resOpenErr != nil {
			return false
		}
		resStore, resJournal, resCleanup, resOpenErr = s.openResources(r.Context(), nb)
		if resOpenErr != nil {
			resStore = nil
			return false
		}
		return true
	}

	for {
		hdr, nextErr := tr.Next()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "corrupt archive: " + nextErr.Error()})
			return
		}
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != 0 {
			continue
		}
		rel := filepath.ToSlash(filepath.Clean(hdr.Name))
		if rel == resourceManifestName {
			var metas []resourceMeta
			if json.NewDecoder(io.LimitReader(tr, 8<<20)).Decode(&metas) == nil {
				for _, m := range metas {
					manifest[m.SHA] = m
				}
			}
			_, _ = io.Copy(io.Discard, tr)
			continue
		}
		if strings.HasPrefix(rel, resourceDirPrefix) {
			results = append(results, s.importResource(r, tr, hdr, rel, dryRun, manifest, openRes, &resStore, &resJournal))
			continue
		}

		entry := importEntry{Path: rel, Size: hdr.Size}
		cleanRel, pathErr := vault.Rel(rel)
		if pathErr != nil || !vault.IsNote(cleanRel) {
			if pathErr != nil {
				entry.Message = fmt.Sprintf("invalid path: %v", pathErr)
			} else {
				entry.Message = "not a note (.md) file"
			}
			entry.Status = "error"
			results = append(results, entry)
			_, _ = io.Copy(io.Discard, tr)
			continue
		}
		if hdr.Size > maxEntrySize {
			entry.Status = "error"
			entry.Message = "file too large"
			results = append(results, entry)
			_, _ = io.Copy(io.Discard, tr)
			continue
		}
		content, readErr := io.ReadAll(io.LimitReader(tr, maxEntrySize+1))
		if readErr != nil {
			entry.Status = "error"
			entry.Message = "read error: " + readErr.Error()
			results = append(results, entry)
			continue
		}
		sum := sha256.Sum256(content)
		entry.SHA = hex.EncodeToString(sum[:])
		entry.Size = int64(len(content))
		validNote = &staged{path: cleanRel, content: content, sha: entry.SHA}
		results = append(results, entry)
	}

	if validNote == nil {
		// No note found; errors from resource entries (if any) are reported.
		if len(results) == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no note file in archive"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"dryRun": dryRun, "entries": results})
		return
	}

	v := vault.Open(nb.Layout.Vault())
	noteIdx := -1
	for i := range results {
		if results[i].Kind == "" && results[i].SHA == validNote.sha {
			noteIdx = i
			break
		}
	}

	if dryRun {
		for i := range results {
			if results[i].Status != "" {
				continue
			}
			if i == noteIdx {
				existing, readErr := v.Read(validNote.path)
				switch {
				case readErr == nil && sha256Hex(existing) == validNote.sha:
					results[i].Status = "skipped"
					results[i].Message = "identical content"
				case readErr == nil:
					results[i].Status = "updated"
				case errors.Is(readErr, vault.ErrNotFound):
					results[i].Status = "created"
				default:
					results[i].Status = "error"
					results[i].Message = readErr.Error()
				}
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"dryRun": true, "entries": results})
		return
	}

	// Apply the note.
	if noteIdx >= 0 {
		existing, readErr := v.Read(validNote.path)
		if readErr == nil && sha256Hex(existing) == validNote.sha {
			results[noteIdx].Status = "skipped"
			results[noteIdx].Message = "identical content"
		} else {
			_, writeErr := v.Write(validNote.path, validNote.content, vault.WriteOpts{})
			switch {
			case writeErr != nil:
				results[noteIdx].Status = "error"
				results[noteIdx].Message = "write failed: " + writeErr.Error()
			case errors.Is(readErr, vault.ErrNotFound):
				results[noteIdx].Status = "created"
			default:
				results[noteIdx].Status = "updated"
			}
		}
	}

	s.reindex(r.Context(), nb, v)
	s.markGitDirty(nb, git.Op{Action: "import", Actor: actorFrom(r)})
	s.emit(r, nb, "notebook.import", validNote.path, map[string]any{"entries": len(results)})

	writeJSON(w, http.StatusOK, map[string]any{"dryRun": false, "entries": results})
}

// exportNotebook handles GET /n/{slug}/export — streams a .tar.gz of all
// markdown notes in the vault plus notebook.toml.
func (s *Server) exportNotebook(w http.ResponseWriter, r *http.Request) {
	nb, ok := s.reg.Get(r.PathValue("slug"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such notebook"})
		return
	}
	// An export walks the whole vault, so any unopened lock would leak
	// the notes it protects.
	if !s.lockGuard(w, r, r.PathValue("slug"), "") {
		return
	}

	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition",
		`attachment; filename="`+safeFilename(nb.Manifest.Slug)+`.tar.gz"`)
	w.WriteHeader(http.StatusOK)

	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)

	vaultRoot := nb.Layout.Vault()
	_ = filepath.WalkDir(vaultRoot, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, relErr := filepath.Rel(vaultRoot, p)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if vault.Ignored(rel) || !vault.IsNote(rel) {
			return nil
		}
		content, readErr := os.ReadFile(p)
		if readErr != nil {
			return nil
		}
		hdr := &tar.Header{Name: rel, Mode: 0o644, Size: int64(len(content))}
		if twErr := tw.WriteHeader(hdr); twErr != nil {
			return twErr
		}
		_, writeErr := tw.Write(content)
		return writeErr
	})

	s.exportResources(r, nb.Manifest.Slug, tw)

	// Include notebook.toml.
	if mf, readErr := os.ReadFile(nb.Layout.Manifest()); readErr == nil {
		hdr := &tar.Header{Name: "notebook.toml", Mode: 0o644, Size: int64(len(mf))}
		if twErr := tw.WriteHeader(hdr); twErr == nil {
			_, _ = tw.Write(mf)
		}
	}

	// Include git history unless explicitly disabled.
	if r.URL.Query().Get("include_history") != "false" {
		s.exportHistoryBundle(nb.Layout.Vault(), tw)
	}

	_ = tw.Close()
	_ = gz.Close()
}

// exportHistoryBundle writes the git history as a bundle entry into tw.
// Errors are logged and skipped so a missing/empty history never aborts export.
func (s *Server) exportHistoryBundle(vaultDir string, tw *tar.Writer) {
	var buf bytes.Buffer
	if err := git.Bundle(vaultDir, &buf); err != nil {
		if err != git.ErrNoHistory {
			s.log.Warn("export: git bundle failed", "err", err)
		}
		return
	}
	data := buf.Bytes()
	hdr := &tar.Header{Name: historyBundleName, Mode: 0o644, Size: int64(len(data))}
	if tw.WriteHeader(hdr) == nil {
		_, _ = tw.Write(data)
	}
}

// importEntry describes the result of processing one archive member.
type importEntry struct {
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	SHA     string `json:"sha,omitempty"`
	Status  string `json:"status"`         // "created"|"updated"|"skipped"|"error"
	Kind    string `json:"kind,omitempty"` // "resource" for attachments; empty = note
	Message string `json:"message,omitempty"`
}

// importNotebook handles POST /n/{slug}/import — accepts a .tar.gz (multipart
// "file" field or raw body) and writes markdown files into the vault.
//
// Flow: validate ALL entries → (if dry_run) return plan → apply.
// Idempotent: files whose content is identical to the vault copy are skipped.
// Per-item isolation: one bad entry never aborts the rest.
func (s *Server) importNotebook(w http.ResponseWriter, r *http.Request) {
	nb, ok := s.reg.Get(r.PathValue("slug"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such notebook"})
		return
	}
	dryRun := r.URL.Query().Get("dry_run") == "true"

	var archiveReader io.Reader
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad multipart"})
			return
		}
		f, _, err := r.FormFile("file")
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing file field"})
			return
		}
		defer func() { _ = f.Close() }()
		archiveReader = f
	} else {
		archiveReader = r.Body
	}

	gz, err := gzip.NewReader(archiveReader)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "not a gzip archive: " + err.Error()})
		return
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)

	const maxEntrySize = 8 << 20
	const maxEntries = 10000
	// maxTotalSize bounds the staged bytes held in RAM. Per-entry caps
	// alone still allow 10000 x 8MiB ≈ 80GiB before any write.
	const maxTotalSize = 64 << 20

	type staged struct {
		path    string
		content []byte
		sha     string
	}
	var validEntries []staged
	var results []importEntry
	var totalSize int64
	var historyBundle []byte
	const maxHistoryBundleSize = 128 << 20

	// Attachments are streamed straight into the store (never held in
	// RAM); the store is opened on the first one.
	var (
		resStore   resources.Store
		resJournal *resources.Journal
		resOpenErr error
		resCleanup func()
		manifest   = map[string]resourceMeta{}
	)
	defer func() {
		if resCleanup != nil {
			resCleanup()
		}
	}()
	openRes := func() bool {
		if resStore != nil {
			return true
		}
		if resOpenErr != nil {
			return false
		}
		resStore, resJournal, resCleanup, resOpenErr = s.openResources(r.Context(), nb)
		if resOpenErr != nil {
			resStore = nil
			return false
		}
		return true
	}

	for {
		hdr, nextErr := tr.Next()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "corrupt archive: " + nextErr.Error()})
			return
		}
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != 0 {
			continue
		}
		rel := filepath.ToSlash(filepath.Clean(hdr.Name))
		if rel == "notebook.toml" {
			_, _ = io.Copy(io.Discard, tr)
			continue
		}
		if rel == historyBundleName {
			if hdr.Size > maxHistoryBundleSize {
				_, _ = io.Copy(io.Discard, tr)
				s.log.Warn("import: history bundle too large", "size", hdr.Size)
				continue
			}
			if b, err := io.ReadAll(io.LimitReader(tr, maxHistoryBundleSize)); err == nil {
				historyBundle = b
			}
			continue
		}

		if rel == resourceManifestName {
			var metas []resourceMeta
			if json.NewDecoder(io.LimitReader(tr, 8<<20)).Decode(&metas) == nil {
				for _, m := range metas {
					manifest[m.SHA] = m
				}
			}
			_, _ = io.Copy(io.Discard, tr)
			continue
		}
		if strings.HasPrefix(rel, resourceDirPrefix) {
			results = append(results, s.importResource(r, tr, hdr, rel, dryRun, manifest, openRes, &resStore, &resJournal))
			continue
		}

		entry := importEntry{Path: rel, Size: hdr.Size}
		cleanRel, pathErr := vault.Rel(rel)
		if pathErr != nil || !vault.IsNote(cleanRel) {
			if pathErr != nil {
				entry.Message = fmt.Sprintf("invalid path: %v", pathErr)
			} else {
				entry.Message = "not a note (.md) file"
			}
			entry.Status = "error"
			results = append(results, entry)
			_, _ = io.Copy(io.Discard, tr)
			continue
		}
		if hdr.Size > maxEntrySize {
			entry.Status = "error"
			entry.Message = "file too large"
			results = append(results, entry)
			_, _ = io.Copy(io.Discard, tr)
			continue
		}
		if len(validEntries) >= maxEntries {
			entry.Status = "error"
			entry.Message = "import limit exceeded"
			results = append(results, entry)
			_, _ = io.Copy(io.Discard, tr)
			continue
		}

		content, readErr := io.ReadAll(io.LimitReader(tr, maxEntrySize+1))
		if readErr != nil {
			entry.Status = "error"
			entry.Message = "read error: " + readErr.Error()
			results = append(results, entry)
			continue
		}
		if totalSize+int64(len(content)) > maxTotalSize {
			entry.Status = "error"
			entry.Message = "import too large"
			results = append(results, entry)
			continue
		}
		totalSize += int64(len(content))
		sum := sha256.Sum256(content)
		entry.SHA = hex.EncodeToString(sum[:])
		entry.Size = int64(len(content))
		validEntries = append(validEntries, staged{path: cleanRel, content: content, sha: entry.SHA})
		results = append(results, entry)
	}

	v := vault.Open(nb.Layout.Vault())

	if dryRun {
		for i := range results {
			if results[i].Status != "" {
				continue
			}
			existing, readErr := v.Read(results[i].Path)
			switch {
			case readErr == nil && sha256Hex(existing) == results[i].SHA:
				results[i].Status = "skipped"
				results[i].Message = "identical content"
			case readErr == nil:
				results[i].Status = "updated"
			case errors.Is(readErr, vault.ErrNotFound):
				results[i].Status = "created"
			default:
				results[i].Status = "error"
				results[i].Message = readErr.Error()
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"dryRun": true, "entries": results})
		return
	}

	// Apply.
	validIdx := 0
	for i := range results {
		if results[i].Status != "" {
			continue
		}
		e := validEntries[validIdx]
		validIdx++

		existing, readErr := v.Read(e.path)
		if readErr == nil && sha256Hex(existing) == e.sha {
			results[i].Status = "skipped"
			results[i].Message = "identical content"
			continue
		}
		_, writeErr := v.Write(e.path, e.content, vault.WriteOpts{})
		switch {
		case writeErr != nil:
			results[i].Status = "error"
			results[i].Message = "write failed: " + writeErr.Error()
		case errors.Is(readErr, vault.ErrNotFound):
			results[i].Status = "created"
		default:
			results[i].Status = "updated"
		}
	}

	// Apply the bundled git history after the notes are written so the
	// working tree matches what the bundle's commits expect. A failure here
	// is non-fatal: the note writes are truth, history is recovery.
	if len(historyBundle) > 0 {
		if err := s.applyHistoryBundle(nb, historyBundle); err != nil {
			s.log.Warn("import: history bundle apply failed", "notebook", nb.Manifest.Slug, "err", err)
		}
	}

	s.reindex(r.Context(), nb, v)
	s.markGitDirty(nb, git.Op{Action: "import", Actor: actorFrom(r)})
	s.emit(r, nb, "notebook.import", "", map[string]any{"entries": len(results)})

	writeJSON(w, http.StatusOK, map[string]any{"dryRun": false, "entries": results})
}

// applyHistoryBundle writes a git bundle to a temp file and unbundles it
// into the notebook's vault git repo, grafting the imported history onto it.
// The repo is (re)initialised first so unbundle works on a notebook whose
// git repo was never created.
func (s *Server) applyHistoryBundle(nb *notebooks.Notebook, bundle []byte) error {
	vaultDir := nb.Layout.Vault()
	if err := git.Init(vaultDir); err != nil {
		return err
	}
	tmp, err := os.CreateTemp("", "noteroot-history-*.bundle")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if _, err := tmp.Write(bundle); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return git.Unbundle(vaultDir, tmpPath)
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// importResource handles one resources/<sha> archive member. It always
// consumes the member's body and never aborts the import: every failure
// becomes an "error" entry for that blob alone.
func (s *Server) importResource(
	r *http.Request, tr *tar.Reader, hdr *tar.Header, rel string, dryRun bool,
	manifest map[string]resourceMeta, openRes func() bool,
	store *resources.Store, journal **resources.Journal,
) importEntry {
	sha := strings.TrimPrefix(rel, resourceDirPrefix)
	entry := importEntry{Path: rel, Size: hdr.Size, SHA: sha, Kind: "resource"}
	fail := func(msg string) importEntry {
		entry.Status = "error"
		entry.Message = msg
		_, _ = io.Copy(io.Discard, tr)
		return entry
	}
	if !resources.ValidSHA(sha) {
		return fail("invalid attachment name")
	}
	policy := s.resourcePolicy()
	maxSize := policy.MaxSize
	if maxSize <= 0 {
		maxSize = resources.DefaultMaxSize
	}
	if hdr.Size > maxSize {
		return fail("attachment too large")
	}
	if !openRes() {
		return fail("attachments unavailable")
	}
	meta := manifest[sha]
	filename := meta.Filename
	if filename == "" {
		filename = sha
	}
	mime := meta.MIME
	if mime == "" {
		mime = resources.SniffMIME(filename)
	}
	if (*store).Has(sha) {
		if m, err := (*journal).MIMEBySHA(sha); err == nil && m[sha] != "" {
			entry.Status = "skipped"
			entry.Message = "already present"
			_, _ = io.Copy(io.Discard, tr)
			return entry
		}
	}
	// The archive member order is not guaranteed: resources.json may appear
	// after this blob, so the manifest MIME is not always available yet. The
	// content hash is authoritative, so hash the body first and only then
	// judge the (possibly sniffed) MIME. A blob whose bytes do not hash to
	// its name must fail on that, never on a fallback MIME guess.
	if dryRun {
		// Hash the stream to confirm the name matches the content before
		// judging the (possibly sniffed) MIME — the manifest may not have
		// been read yet. A dry run must not write to the store.
		h := sha256.New()
		if _, err := io.Copy(h, io.LimitReader(tr, maxSize+1)); err != nil {
			return fail("read failed: " + err.Error())
		}
		if hex.EncodeToString(h.Sum(nil)) != sha {
			return fail("content does not match its name")
		}
		if _, err := policy.Validate(filename, hdr.Size, mime); err != nil {
			return fail(err.Error())
		}
		entry.Status = "created"
		return entry
	}
	// Stream the body into the content-addressed store, then verify it
	// hashes to its declared name before deciding anything else.
	got, _, created, err := (*store).Put(io.LimitReader(tr, maxSize+1))
	if err != nil {
		return fail("store failed: " + err.Error())
	}
	if got != sha {
		if created {
			_ = (*store).Delete(got)
		}
		return fail("content does not match its name")
	}
	name, err := policy.Validate(filename, hdr.Size, mime)
	if err != nil {
		if created {
			_ = (*store).Delete(got)
		}
		return fail(err.Error())
	}
	id, err := s.reserveQuota(r.Context(), *journal, name, mime, hdr.Size)
	if err != nil {
		if created {
			_ = (*store).Delete(got)
		}
		if errors.Is(err, ErrQuotaExceeded) {
			return fail("quota exceeded")
		}
		return fail("journal begin failed")
	}
	if err := (*journal).Commit(id, sha); err != nil {
		s.log.Warn("import: journal commit failed", "sha", sha, "err", err)
	}
	entry.Status = "created"
	return entry
}

// --- HTML Export ---

// exportNoteHTML renders a single note to HTML and returns it.
func (s *Server) exportNoteHTML(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	nb, ok := s.reg.Get(slug)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such notebook"})
		return
	}
	path := r.URL.Query().Get("path")
	if path == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing path"})
		return
	}
	if !s.lockGuard(w, r, slug, path) {
		return
	}

	v := vault.Open(nb.Layout.Vault())
	content, err := v.Read(path)
	if err != nil {
		writeJSON(w, statusFor(err), map[string]string{"error": publicError(err)})
		return
	}

	// Resolve wikilinks to note URLs
	resolve := func(target string) (string, bool) {
		targetPath, ok := s.resolveWikilink(nb, target)
		if !ok {
			return "", false
		}
		return fmt.Sprintf("/n/%s/note?path=%s", slug, url.QueryEscape(targetPath)), true
	}

	renderedHTML, err := markdown.New().RenderWithResolver(content, resolve)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "render failed"})
		return
	}

	// Wrap in full HTML page with minimal styling
	title := path
	if idx := strings.LastIndex(title, "/"); idx >= 0 {
		title = title[idx+1:]
	}
	title = strings.TrimSuffix(title, ".md")

	page := fmt.Sprintf(`<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<title>%s</title>
<meta name="viewport" content="width=device-width, initial-scale=1">
<style>
body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; line-height: 1.7; max-width: 800px; margin: 2rem auto; padding: 0 1rem; }
pre { background: #f4f4f4; border: 1px solid #ddd; padding: 1rem; overflow-x: auto; border-radius: 4px; }
code { font-family: monospace; background: #f4f4f4; padding: 0.1rem 0.4rem; border-radius: 3px; }
pre code { background: none; padding: 0; }
img { max-width: 100%%; border-radius: 4px; }
a { color: #0066cc; }
blockquote { margin: 1rem 0; padding: 0.5rem 1rem; border-left: 3px solid #ddd; color: #666; }
table { border-collapse: collapse; margin: 1rem 0; }
th, td { border: 1px solid #ddd; padding: 0.5rem 1rem; }
th { background: #f4f4f4; text-align: left; }
hr { border: 0; border-top: 1px solid #ddd; margin: 2rem 0; }
</style>
</head>
<body>
%s
</body>
</html>`, html.EscapeString(title), renderedHTML)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.html"`, safeFilename(strings.TrimSuffix(filepath.Base(path), ".md"))))
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(page))
}

// exportNotebookHTML exports the entire notebook as a zip of HTML files.
func (s *Server) exportNotebookHTML(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	nb, ok := s.reg.Get(slug)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such notebook"})
		return
	}
	if !s.lockGuard(w, r, slug, "") {
		return
	}

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-html.zip"`, safeFilename(nb.Manifest.Slug)))
	w.WriteHeader(http.StatusOK)

	zw := zip.NewWriter(w)
	defer zw.Close()

	v := vault.Open(nb.Layout.Vault())
	vaultRoot := nb.Layout.Vault()

	err := filepath.WalkDir(vaultRoot, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, relErr := filepath.Rel(vaultRoot, p)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if vault.Ignored(rel) || !vault.IsNote(rel) {
			return nil
		}

		content, readErr := v.Read(rel)
		if readErr != nil {
			return nil
		}

		resolve := func(target string) (string, bool) {
			targetPath, ok := s.resolveWikilink(nb, target)
			if !ok {
				return "", false
			}
			return fmt.Sprintf("%s.html", strings.TrimSuffix(targetPath, ".md")), true
		}

		renderedHTML, err := markdown.New().RenderWithResolver(content, resolve)
		if err != nil {
			return nil
		}

		title := strings.TrimSuffix(rel, ".md")
		page := fmt.Sprintf(`<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<title>%s</title>
<meta name="viewport" content="width=device-width, initial-scale=1">
<style>
body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; line-height: 1.7; max-width: 800px; margin: 2rem auto; padding: 0 1rem; }
pre { background: #f4f4f4; border: 1px solid #ddd; padding: 1rem; overflow-x: auto; border-radius: 4px; }
code { font-family: monospace; background: #f4f4f4; padding: 0.1rem 0.4rem; border-radius: 3px; }
pre code { background: none; padding: 0; }
img { max-width: 100%%; border-radius: 4px; }
a { color: #0066cc; }
blockquote { margin: 1rem 0; padding: 0.5rem 1rem; border-left: 3px solid #ddd; color: #666; }
table { border-collapse: collapse; margin: 1rem 0; }
th, td { border: 1px solid #ddd; padding: 0.5rem 1rem; }
th { background: #f4f4f4; text-align: left; }
hr { border: 0; border-top: 1px solid #ddd; margin: 2rem 0; }
</style>
</head>
<body>
%s
</body>
</html>`, html.EscapeString(title), renderedHTML)

		zipPath := strings.TrimSuffix(rel, ".md") + ".html"
		f, err := zw.Create(zipPath)
		if err != nil {
			return err
		}
		_, err = f.Write([]byte(page))
		return err
	})
	if err != nil {
		s.log.Warn("export HTML zip failed", "notebook", slug, "err", err)
	}
}

// safeFilename removes characters that are invalid in a quoted
// Content-Disposition filename parameter. A double-quote inside
// filename="…" terminates the value prematurely; stripping them
// prevents header corruption without changing anything meaningful
// because " is not a valid POSIX filename character anyway.
func safeFilename(name string) string {
	return strings.ReplaceAll(name, `"`, "")
}
