package httpapi

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"noteroot/internal/config"
)

// buildTarGz creates an in-memory .tar.gz with the given path→content entries.
func buildTarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		hdr := &tar.Header{
			Name:     name,
			Typeflag: tar.TypeReg,
			Mode:     0o644,
			Size:     int64(len(content)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

// postImport uploads an archive to /n/{slug}/import.
func postImport(t *testing.T, srv *Server, slug string, archive []byte, dryRun bool) *httptest.ResponseRecorder {
	t.Helper()
	url := "/n/" + slug + "/import"
	if dryRun {
		url += "?dry_run=true"
	}
	req := httptest.NewRequest(http.MethodPost, url, bytes.NewReader(archive))
	req.Header.Set("Content-Type", "application/gzip")
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, req)
	return rec
}

func TestExport_returnsGzip(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{
		"notes/hello.md":   "# Hello",
		"notes/world.md":   "# World",
		"ignored/.swp.swp": "garbage",
	}))

	req := httptest.NewRequest(http.MethodGet, "/n/work/export", nil)
	rr := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/gzip" {
		t.Errorf("Content-Type = %q", ct)
	}
	if cd := rr.Header().Get("Content-Disposition"); cd == "" {
		t.Error("missing Content-Disposition")
	}

	// Decompress and list paths.
	gz, err := gzip.NewReader(rr.Body)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	var paths []string
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		paths = append(paths, hdr.Name)
	}

	found := map[string]bool{}
	for _, p := range paths {
		found[p] = true
	}
	if !found["notes/hello.md"] || !found["notes/world.md"] {
		t.Errorf("expected notes in archive, got %v", paths)
	}
	if found["ignored/.swp.swp"] {
		t.Error("ignored files should not be in archive")
	}
}

func TestExport_unknownNotebook(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"a.md": "x"}))
	req := httptest.NewRequest(http.MethodGet, "/n/nope/export", nil)
	rr := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

func TestImport_createNew(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"existing.md": "old"}))

	archive := buildTarGz(t, map[string]string{
		"new.md": "# New Note",
		"a/b.md": "nested",
	})
	rec := postImport(t, srv, "work", archive, false)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	decodeJSON(t, rec, &resp)
	entries, _ := resp["entries"].([]any)
	statusCount := map[string]int{}
	for _, e := range entries {
		em := e.(map[string]any)
		statusCount[em["status"].(string)]++
	}
	if statusCount["created"] != 2 {
		t.Errorf("created = %d, want 2; statuses: %v", statusCount["created"], statusCount)
	}
}

func TestImport_idempotent(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{
		"same.md": "unchanged content",
	}))

	archive := buildTarGz(t, map[string]string{"same.md": "unchanged content"})
	rec := postImport(t, srv, "work", archive, false)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var resp map[string]any
	decodeJSON(t, rec, &resp)
	entries, _ := resp["entries"].([]any)
	for _, e := range entries {
		em := e.(map[string]any)
		if em["status"] != "skipped" {
			t.Errorf("identical file should be skipped, got %v", em["status"])
		}
	}
}

func TestImport_dryRun(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"existing.md": "old"}))

	archive := buildTarGz(t, map[string]string{
		"newfile.md":  "brand new",
		"existing.md": "changed",
	})
	rec := postImport(t, srv, "work", archive, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var resp map[string]any
	decodeJSON(t, rec, &resp)
	if dry, _ := resp["dryRun"].(bool); !dry {
		t.Error("dryRun should be true")
	}
	// Verify that newfile was not actually created.
	getReq := httptest.NewRequest(http.MethodGet, "/n/work/note?path=newfile.md", nil)
	getRec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(getRec, getReq)
	if getRec.Code == http.StatusOK {
		t.Error("dry_run should not write files")
	}
}

func TestImport_rejectsNonMDFiles(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"a.md": "x"}))

	archive := buildTarGz(t, map[string]string{
		"photo.jpg":  "data",
		"notes.md":   "valid",
		"readme.txt": "nope",
	})
	rec := postImport(t, srv, "work", archive, false)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var resp map[string]any
	decodeJSON(t, rec, &resp)
	entries, _ := resp["entries"].([]any)
	statusCount := map[string]int{}
	for _, e := range entries {
		em := e.(map[string]any)
		statusCount[em["status"].(string)]++
	}
	if statusCount["error"] != 2 {
		t.Errorf("expected 2 errors (jpg + txt), got: %v", statusCount)
	}
	if statusCount["created"] != 1 {
		t.Errorf("expected 1 created (.md), got: %v", statusCount)
	}
}

func TestImport_rejectsPathTraversal(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"a.md": "x"}))

	archive := buildTarGz(t, map[string]string{
		"../escape.md":      "bad",
		"valid/note.md":     "ok",
		"/absolute/path.md": "bad",
	})
	rec := postImport(t, srv, "work", archive, false)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var resp map[string]any
	decodeJSON(t, rec, &resp)
	entries, _ := resp["entries"].([]any)
	statusCount := map[string]int{}
	for _, e := range entries {
		em := e.(map[string]any)
		statusCount[em["status"].(string)]++
	}
	// Path traversal and absolute paths must be rejected.
	if statusCount["error"] < 1 {
		t.Errorf("expected errors for unsafe paths, got: %v", statusCount)
	}
}

func TestImport_multipart(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"a.md": "x"}))

	archive := buildTarGz(t, map[string]string{"imported.md": "# Imported"})

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "export.tar.gz")
	_, _ = fw.Write(archive)
	_ = mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/n/work/import", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("multipart import = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	decodeJSON(t, rec, &resp)
	entries, _ := resp["entries"].([]any)
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	if s := entries[0].(map[string]any)["status"]; s != "created" {
		t.Errorf("status = %v, want created", s)
	}
}

func TestImport_unknownNotebook(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"a.md": "x"}))
	rec := postImport(t, srv, "nope", buildTarGz(t, map[string]string{"a.md": "x"}), false)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestImport_notGzip(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"a.md": "x"}))
	req := httptest.NewRequest(http.MethodPost, "/n/work/import", bytes.NewReader([]byte("not gzip")))
	req.Header.Set("Content-Type", "application/gzip")
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestExportImportRoundtrip(t *testing.T) {
	// Export from source, import to a fresh notebook, verify notes present.
	reg := notesFixture(t, map[string]string{
		"index.md":   "# Index",
		"notes/a.md": "# A",
		"notes/b.md": "# B",
	})
	srv := newTestServer(t, config.AuthNone, reg)

	// Export.
	exportReq := httptest.NewRequest(http.MethodGet, "/n/work/export", nil)
	exportRec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(exportRec, exportReq)
	if exportRec.Code != http.StatusOK {
		t.Fatalf("export = %d", exportRec.Code)
	}
	archiveBytes := exportRec.Body.Bytes()

	// Import to same notebook (all will be skipped since content identical).
	importRec := postImport(t, srv, "work", archiveBytes, false)
	if importRec.Code != http.StatusOK {
		t.Fatalf("import = %d, body = %s", importRec.Code, importRec.Body.String())
	}
	var resp map[string]any
	decodeJSON(t, importRec, &resp)
	entries, _ := resp["entries"].([]any)
	for _, e := range entries {
		em := e.(map[string]any)
		if em["path"] == "notebook.toml" {
			continue
		}
		if em["status"] != "skipped" {
			t.Errorf("%v: status = %v, want skipped (round-trip idempotency)", em["path"], em["status"])
		}
	}
}
