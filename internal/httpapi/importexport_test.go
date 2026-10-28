package httpapi

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"

	"noteroot/internal/config"
)

// buildTarGzOrdered creates a .tar.gz writing entries in the exact order
// given. Unlike buildTarGz (which ranges a map, so order is random) this
// pins the layout, so tests can force resources.json after a blob.
func buildTarGzOrdered(t *testing.T, names []string, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, name := range names {
		content := files[name]
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

func TestImport_totalSizeCap(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"existing.md": "old"}))

	// Nine 8MiB entries: the ninth pushes staged bytes past the 64MiB
	// total and must be rejected, not staged into RAM.
	files := map[string]string{}
	for i := 0; i < 9; i++ {
		files["big"+string(rune('a'+i))+".md"] = strings.Repeat("x", 8<<20)
	}
	rec := postImport(t, srv, "work", buildTarGz(t, files), false)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	decodeJSON(t, rec, &resp)
	capped := 0
	for _, e := range resp["entries"].([]any) {
		em := e.(map[string]any)
		if em["status"] == "error" && strings.Contains(em["message"].(string), "too large") {
			capped++
		}
	}
	if capped == 0 {
		t.Error("no entry rejected by the total-size cap")
	}
}

// tarMembers lists member name -> bytes of a .tar.gz.
func tarMembers(t *testing.T, archive []byte) map[string][]byte {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	out := map[string][]byte{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(tr)
		out[h.Name] = b
	}
}

func TestExport_includesAttachmentsAndImportRestoresThem(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"a.md": "# A"}))
	png := []byte("\x89PNG\r\n\x1a\nfake-image-bytes")
	up := postResource(t, srv, "work", "pic.png", "image/png", png)
	if up.Code != http.StatusCreated {
		t.Fatalf("upload = %d %s", up.Code, up.Body.String())
	}
	var ur struct{ SHA string }
	decodeJSON(t, up, &struct {
		SHA *string `json:"sha"`
	}{&ur.SHA})

	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/n/work/export", nil))
	members := tarMembers(t, rec.Body.Bytes())
	if !bytes.Equal(members["resources/"+ur.SHA], png) {
		t.Fatalf("export lacks the attachment blob: %v", keys(members))
	}
	if !strings.Contains(string(members["resources.json"]), `"filename":"pic.png"`) {
		t.Fatalf("manifest = %s", members["resources.json"])
	}

	// Dry run into a notebook without the blob plans a "created" attachment.
	other := notebookReq(t, srv, http.MethodPost, "/api/v1/notebooks", `{"slug":"copy","name":"Copy"}`)
	if other.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", other.Code, other.Body.String())
	}
	dry := postImport(t, srv, "copy", rec.Body.Bytes(), true)
	if !strings.Contains(dry.Body.String(), `"kind":"resource"`) || !strings.Contains(dry.Body.String(), `"status":"created"`) {
		t.Fatalf("dry run = %s", dry.Body.String())
	}
	if g := get(t, srv, "/n/copy/resources/"+ur.SHA, nil); g.Code != http.StatusNotFound {
		t.Fatalf("dry run must not store the blob, got %d", g.Code)
	}

	imp := postImport(t, srv, "copy", rec.Body.Bytes(), false)
	if imp.Code != http.StatusOK || strings.Contains(imp.Body.String(), `"status":"error"`) {
		t.Fatalf("import = %d %s", imp.Code, imp.Body.String())
	}
	g := get(t, srv, "/n/copy/resources/"+ur.SHA, nil)
	if g.Code != http.StatusOK || !bytes.Equal(g.Body.Bytes(), png) {
		t.Fatalf("restored blob = %d", g.Code)
	}
	if ct := g.Header().Get("Content-Type"); ct != "image/png" {
		t.Fatalf("restored MIME = %q", ct)
	}

	// Importing again changes nothing.
	again := postImport(t, srv, "copy", rec.Body.Bytes(), false)
	if strings.Contains(again.Body.String(), `"status":"created"`) && strings.Contains(again.Body.String(), "resources/") &&
		!strings.Contains(again.Body.String(), "already present") {
		t.Fatalf("second import = %s", again.Body.String())
	}
}

func TestImport_rejectsMismatchedAttachment(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"a.md": "# A"}))
	name := "resources/" + strings.Repeat("b", 64)
	rec := postImport(t, srv, "work", buildTarGz(t, map[string]string{
		"resources.json": `[{"sha":"` + strings.Repeat("b", 64) + `","filename":"x.png","mime":"image/png"}]`,
		name:             "not the bytes that hash to bbbb",
	}), false)
	if !strings.Contains(rec.Body.String(), "does not match") {
		t.Fatalf("got %s", rec.Body.String())
	}
	if g := get(t, srv, "/n/work/"+name, nil); g.Code == http.StatusOK {
		t.Fatal("mismatched blob must not be stored")
	}
}

// A mismatched blob must fail on its hash even when resources.json has not
// been read yet (blob precedes the manifest in the archive). Before the fix
// the sniffed fallback MIME — application/octet-stream — was judged first
// and the import reported a MIME error instead of the real problem.
func TestImport_mismatchedAttachmentBeforeManifest(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"a.md": "# A"}))
	blob := "resources/" + strings.Repeat("b", 64)
	files := map[string]string{
		blob:             "not the bytes that hash to bbbb",
		"resources.json": `[{"sha":"` + strings.Repeat("b", 64) + `","filename":"x.png","mime":"image/png"}]`,
	}
	rec := postImport(t, srv, "work", buildTarGzOrdered(t, []string{blob, "resources.json"}, files), false)
	if !strings.Contains(rec.Body.String(), "does not match") {
		t.Fatalf("got %s", rec.Body.String())
	}
	if g := get(t, srv, "/n/work/"+blob, nil); g.Code == http.StatusOK {
		t.Fatal("mismatched blob must not be stored")
	}
}

// A blob whose content hashes correctly but whose declared MIME is not
// allowed must still be rejected, and nothing may be left in the store.
func TestImport_rejectsDisallowedMIME(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"a.md": "# A"}))
	content := []byte("#!/bin/sh\nrm -rf /\n")
	sha := sha256Hex(content)
	blob := "resources/" + sha
	files := map[string]string{
		blob:             string(content),
		"resources.json": `[{"sha":"` + sha + `","filename":"evil.sh","mime":"application/x-sh"}]`,
	}
	rec := postImport(t, srv, "work", buildTarGzOrdered(t, []string{blob, "resources.json"}, files), false)
	if !strings.Contains(rec.Body.String(), "mime") {
		t.Fatalf("got %s", rec.Body.String())
	}
	if g := get(t, srv, "/n/work/"+blob, nil); g.Code == http.StatusOK {
		t.Fatal("disallowed blob must not be stored")
	}
}

func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// makeCommit ensures dir is a git repo and creates a commit so history
// exists for export tests.
func makeCommit(t *testing.T, dir, msg string) {
	t.Helper()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir,
			"-c", "user.name=noteroot", "-c", "user.email=noteroot@local"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("add", "-A")
	run("commit", "-q", "-m", msg)
}

func TestExportNoteArchive_includesNoteAndAttachments(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"a.md": "# A"}))
	png := []byte("\x89PNG\r\n\x1a\nfake-image-bytes")
	up := postResource(t, srv, "work", "pic.png", "image/png", png)
	if up.Code != http.StatusCreated {
		t.Fatalf("upload = %d %s", up.Code, up.Body.String())
	}
	var sha string
	decodeJSON(t, up, &struct {
		SHA *string `json:"sha"`
	}{&sha})

	noteBody := "# A\n\n![pic](/n/work/resources/" + sha + ")\n"
	req := httptest.NewRequest(http.MethodPut, "/n/work/note?path=a.md", strings.NewReader(noteBody))
	req.Header.Set("Content-Type", "text/markdown")
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK && rec.Code != http.StatusNoContent {
		t.Fatalf("put note = %d %s", rec.Code, rec.Body.String())
	}
	// The server mints frontmatter on first write; the archive carries the
	// stored bytes, not the body we sent.
	raw := get(t, srv, "/n/work/raw?path=a.md", nil)
	stored := raw.Body.Bytes()

	exp := httptest.NewRequest(http.MethodGet, "/n/work/note/export/archive?path=a.md", nil)
	expRec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(expRec, exp)
	if expRec.Code != http.StatusOK {
		t.Fatalf("export archive = %d", expRec.Code)
	}
	members := tarMembers(t, expRec.Body.Bytes())
	if !bytes.Equal(members["a.md"], stored) {
		t.Fatalf("archive note mismatch: %v", keys(members))
	}
	if !bytes.Equal(members["resources/"+sha], png) {
		t.Fatalf("archive lacks attachment blob: %v", keys(members))
	}
	if !strings.Contains(string(members["resources.json"]), `"filename":"pic.png"`) {
		t.Fatalf("manifest = %s", members["resources.json"])
	}
}

func TestImportNoteArchive_restoresNoteAndAttachments(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"a.md": "# A"}))
	png := []byte("\x89PNG\r\n\x1a\nfake-image-bytes")
	up := postResource(t, srv, "work", "pic.png", "image/png", png)
	if up.Code != http.StatusCreated {
		t.Fatalf("upload = %d %s", up.Code, up.Body.String())
	}
	var sha string
	decodeJSON(t, up, &struct {
		SHA *string `json:"sha"`
	}{&sha})
	noteBody := "# A\n\n![pic](/n/work/resources/" + sha + ")\n"
	put := httptest.NewRequest(http.MethodPut, "/n/work/note?path=a.md", strings.NewReader(noteBody))
	put.Header.Set("Content-Type", "text/markdown")
	putRec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(putRec, put)
	// Stored bytes include the server-minted frontmatter.
	stored := get(t, srv, "/n/work/raw?path=a.md", nil).Body.Bytes()

	exp := httptest.NewRequest(http.MethodGet, "/n/work/note/export/archive?path=a.md", nil)
	expRec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(expRec, exp)
	if expRec.Code != http.StatusOK {
		t.Fatalf("export = %d", expRec.Code)
	}
	archive := expRec.Body.Bytes()

	other := notebookReq(t, srv, http.MethodPost, "/api/v1/notebooks", `{"slug":"copy","name":"Copy"}`)
	if other.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", other.Code, other.Body.String())
	}

	dryReq := httptest.NewRequest(http.MethodPost, "/n/copy/note/import?dry_run=true", bytes.NewReader(archive))
	dryReq.Header.Set("Content-Type", "application/gzip")
	dryRec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(dryRec, dryReq)
	if dryRec.Code != http.StatusOK {
		t.Fatalf("dry import = %d %s", dryRec.Code, dryRec.Body.String())
	}
	if !strings.Contains(dryRec.Body.String(), `"kind":"resource"`) || !strings.Contains(dryRec.Body.String(), `"status":"created"`) {
		t.Fatalf("dry run = %s", dryRec.Body.String())
	}

	impReq := httptest.NewRequest(http.MethodPost, "/n/copy/note/import", bytes.NewReader(archive))
	impReq.Header.Set("Content-Type", "application/gzip")
	impRec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(impRec, impReq)
	if impRec.Code != http.StatusOK || strings.Contains(impRec.Body.String(), `"status":"error"`) {
		t.Fatalf("import = %d %s", impRec.Code, impRec.Body.String())
	}

	g := get(t, srv, "/n/copy/raw?path=a.md", nil)
	if g.Code != http.StatusOK || !bytes.Equal(g.Body.Bytes(), stored) {
		t.Fatalf("restored note = %d %q", g.Code, g.Body.String())
	}
	ga := get(t, srv, "/n/copy/resources/"+sha, nil)
	if ga.Code != http.StatusOK || !bytes.Equal(ga.Body.Bytes(), png) {
		t.Fatalf("restored attachment = %d", ga.Code)
	}
}

func TestExport_includesHistoryByDefault(t *testing.T) {
	reg := notesFixture(t, map[string]string{"a.md": "# A"})
	srv := newTestServer(t, config.AuthNone, reg)
	nb, _ := srv.reg.Get("work")
	makeCommit(t, nb.Layout.Vault(), "initial note")

	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/n/work/export", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("export = %d", rec.Code)
	}
	members := tarMembers(t, rec.Body.Bytes())
	if _, ok := members["history.bundle"]; !ok {
		t.Fatalf("export lacks history.bundle: %v", keys(members))
	}
	if len(members["history.bundle"]) == 0 {
		t.Fatal("history.bundle is empty")
	}
}

func TestExport_historyCanBeDisabled(t *testing.T) {
	reg := notesFixture(t, map[string]string{"a.md": "# A"})
	srv := newTestServer(t, config.AuthNone, reg)
	nb, _ := srv.reg.Get("work")
	makeCommit(t, nb.Layout.Vault(), "initial note")

	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/n/work/export?include_history=false", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("export = %d", rec.Code)
	}
	members := tarMembers(t, rec.Body.Bytes())
	if _, ok := members["history.bundle"]; ok {
		t.Fatalf("history.bundle present despite include_history=false: %v", keys(members))
	}
}

func TestImport_appliesHistoryBundle(t *testing.T) {
	reg := notesFixture(t, map[string]string{"a.md": "# A"})
	srv := newTestServer(t, config.AuthNone, reg)
	nb, _ := srv.reg.Get("work")
	makeCommit(t, nb.Layout.Vault(), "initial note")

	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/n/work/export", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("export = %d", rec.Code)
	}
	archive := rec.Body.Bytes()

	other := notebookReq(t, srv, http.MethodPost, "/api/v1/notebooks", `{"slug":"copy","name":"Copy"}`)
	if other.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", other.Code, other.Body.String())
	}
	imp := postImport(t, srv, "copy", archive, false)
	if imp.Code != http.StatusOK {
		t.Fatalf("import = %d %s", imp.Code, imp.Body.String())
	}

	copyNB, _ := srv.reg.Get("copy")
	out, err := exec.Command("git", "-C", copyNB.Layout.Vault(), "log", "--oneline").Output()
	if err != nil {
		t.Fatalf("git log in copy: %v", err)
	}
	if !strings.Contains(string(out), "initial note") {
		t.Fatalf("copy history lacks the imported commit: %s", out)
	}
	g := get(t, srv, "/n/copy/raw?path=a.md", nil)
	if g.Code != http.StatusOK {
		t.Fatalf("restored note = %d", g.Code)
	}
}
