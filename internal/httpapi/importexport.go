package httpapi

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"noteroot/internal/git"
	"noteroot/internal/vault"
)

// exportNotebook handles GET /n/{slug}/export — streams a .tar.gz of all
// markdown notes in the vault plus notebook.toml.
func (s *Server) exportNotebook(w http.ResponseWriter, r *http.Request) {
	nb, ok := s.reg.Get(r.PathValue("slug"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such notebook"})
		return
	}

	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition",
		`attachment; filename="`+nb.Manifest.Slug+`.tar.gz"`)
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

	// Include notebook.toml.
	if mf, readErr := os.ReadFile(nb.Layout.Manifest()); readErr == nil {
		hdr := &tar.Header{Name: "notebook.toml", Mode: 0o644, Size: int64(len(mf))}
		if twErr := tw.WriteHeader(hdr); twErr == nil {
			_, _ = tw.Write(mf)
		}
	}

	_ = tw.Close()
	_ = gz.Close()
}

// importEntry describes the result of processing one archive member.
type importEntry struct {
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	SHA     string `json:"sha,omitempty"`
	Status  string `json:"status"` // "created"|"updated"|"skipped"|"error"
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

	s.reindex(r.Context(), nb, v)
	s.markGitDirty(nb, git.Op{Action: "import", Actor: actorFrom(r)})
	s.emit(r, nb, "notebook.import", "", map[string]any{"entries": len(results)})

	writeJSON(w, http.StatusOK, map[string]any{"dryRun": false, "entries": results})
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
