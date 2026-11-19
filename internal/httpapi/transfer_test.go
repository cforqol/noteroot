package httpapi

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"noteroot/internal/config"
)

// --- export jobs ---

func createExportJobReq(t *testing.T, srv *Server, slug string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/n/"+slug+"/export/jobs", nil)
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, req)
	return rec
}

func exportJobStatusReq(t *testing.T, srv *Server, slug, id string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/n/"+slug+"/export/jobs/"+id, nil)
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, req)
	return rec
}

func downloadExportJobReq(t *testing.T, srv *Server, slug, id, rangeHdr string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/n/"+slug+"/export/jobs/"+id+"/download", nil)
	if rangeHdr != "" {
		req.Header.Set("Range", rangeHdr)
	}
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, req)
	return rec
}

// waitExportJobDone polls the job status until it leaves "building".
func waitExportJobDone(t *testing.T, srv *Server, slug, id string) exportJobState {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		rec := exportJobStatusReq(t, srv, slug, id)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body)
		}
		var info exportJobState
		decodeJSON(t, rec, &info)
		if info.Status != "building" {
			return info
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("export job never finished building")
	return exportJobState{}
}

func TestExportJob_buildsAndDownloads(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{
		"notes/hello.md": "# Hello",
		"notes/world.md": "# World",
	}))

	rec := createExportJobReq(t, srv, "work")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("create status = %d: %s", rec.Code, rec.Body)
	}
	var created map[string]any
	decodeJSON(t, rec, &created)
	id, _ := created["id"].(string)
	if id == "" {
		t.Fatal("missing job id")
	}
	if created["status"] != "building" {
		t.Errorf("initial status = %v, want building", created["status"])
	}

	info := waitExportJobDone(t, srv, "work", id)
	if info.Status != "done" {
		t.Fatalf("job status = %+v", info)
	}
	if info.Size <= 0 {
		t.Errorf("size = %d, want > 0", info.Size)
	}

	dl := downloadExportJobReq(t, srv, "work", id, "")
	if dl.Code != http.StatusOK {
		t.Fatalf("download status = %d: %s", dl.Code, dl.Body)
	}
	if ct := dl.Header().Get("Content-Type"); ct != "application/gzip" {
		t.Errorf("Content-Type = %q", ct)
	}
	if dl.Header().Get("Accept-Ranges") != "bytes" {
		t.Error("download must advertise Range support for resumability")
	}

	gz, err := gzip.NewReader(dl.Body)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	found := map[string]bool{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		found[hdr.Name] = true
	}
	if !found["notes/hello.md"] || !found["notes/world.md"] {
		t.Errorf("expected notes in archive, got %v", found)
	}
}

func TestExportJob_rangeRequestResumesDownload(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{
		"a.md": "# A, with enough content to be a non-trivial archive body for range testing.",
	}))
	rec := createExportJobReq(t, srv, "work")
	var created map[string]any
	decodeJSON(t, rec, &created)
	id := created["id"].(string)
	info := waitExportJobDone(t, srv, "work", id)

	full := downloadExportJobReq(t, srv, "work", id, "")
	fullBytes := full.Body.Bytes()
	if int64(len(fullBytes)) != info.Size {
		t.Fatalf("full download = %d bytes, want %d", len(fullBytes), info.Size)
	}

	// Simulate a resumed download picking up from the midpoint, exactly
	// as a dropped connection would be retried.
	mid := int64(len(fullBytes) / 2)
	resumed := downloadExportJobReq(t, srv, "work", id, fmt.Sprintf("bytes=%d-", mid))
	if resumed.Code != http.StatusPartialContent {
		t.Fatalf("resumed status = %d, want 206: %s", resumed.Code, resumed.Body)
	}
	if !bytes.Equal(resumed.Body.Bytes(), fullBytes[mid:]) {
		t.Error("ranged response does not match the tail of the full download")
	}
}

func TestExportJob_downloadNotReadyWhileBuilding(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"a.md": "x"}))
	nb, _ := srv.reg.Get("work")
	dir := exportJobDir(nb, newTransferID())
	id := filepath.Base(dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, exportPartName), []byte("partial"), 0o644); err != nil {
		t.Fatal(err)
	}

	st := exportJobStatusReq(t, srv, "work", id)
	if st.Code != http.StatusOK {
		t.Fatalf("status = %d", st.Code)
	}
	var info exportJobState
	decodeJSON(t, st, &info)
	if info.Status != "building" {
		t.Errorf("status = %q, want building", info.Status)
	}

	dl := downloadExportJobReq(t, srv, "work", id, "")
	if dl.Code != http.StatusConflict {
		t.Errorf("download while building = %d, want 409", dl.Code)
	}
}

func TestExportJob_unknownJobIs404(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"a.md": "x"}))
	id := newTransferID()
	if rec := exportJobStatusReq(t, srv, "work", id); rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	if rec := downloadExportJobReq(t, srv, "work", id, ""); rec.Code != http.StatusNotFound {
		t.Errorf("download = %d, want 404", rec.Code)
	}
}

func TestExportJob_malformedIDIs404(t *testing.T) {
	// Path-traversal-shaped ids ("../../etc/passwd") never reach the
	// handler at all: net/http's mux cleans and redirects them first.
	// validTransferID's own rejection of that shape is covered by
	// TestValidTransferID; here we only need shapes that survive mux
	// routing unchanged but are still not a valid job id.
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"a.md": "x"}))
	for _, bad := range []string{"short", "not-hex-not-hex-not-hex-not-hexxx"} {
		if rec := exportJobStatusReq(t, srv, "work", bad); rec.Code != http.StatusNotFound {
			t.Errorf("id %q: status = %d, want 404", bad, rec.Code)
		}
	}
}

func TestExportJob_unknownNotebook(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, nil)
	if rec := createExportJobReq(t, srv, "nope"); rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestExportJob_cancelDeletesJob(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"a.md": "x"}))
	rec := createExportJobReq(t, srv, "work")
	var created map[string]any
	decodeJSON(t, rec, &created)
	id := created["id"].(string)
	waitExportJobDone(t, srv, "work", id)

	del := httptest.NewRequest(http.MethodDelete, "/n/work/export/jobs/"+id, nil)
	drec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(drec, del)
	if drec.Code != http.StatusOK {
		t.Fatalf("delete status = %d: %s", drec.Code, drec.Body)
	}

	if rec := exportJobStatusReq(t, srv, "work", id); rec.Code != http.StatusNotFound {
		t.Errorf("status after cancel = %d, want 404", rec.Code)
	}
}

func TestExportJob_buildFailureReportsError(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"a.md": "x"}))
	nb, _ := srv.reg.Get("work")
	dir := exportJobDir(nb, newTransferID())
	id := filepath.Base(dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, exportErrorName), []byte("disk full"), 0o644); err != nil {
		t.Fatal(err)
	}

	st := exportJobStatusReq(t, srv, "work", id)
	var info exportJobState
	decodeJSON(t, st, &info)
	if info.Status != "error" || info.Error != "disk full" {
		t.Errorf("info = %+v", info)
	}
	dl := downloadExportJobReq(t, srv, "work", id, "")
	if dl.Code != http.StatusInternalServerError {
		t.Errorf("download of failed job = %d, want 500", dl.Code)
	}
}

// --- import jobs ---

func createImportJobReq(t *testing.T, srv *Server, slug string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/n/"+slug+"/import/jobs", nil)
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, req)
	return rec
}

func appendImportJobReq(t *testing.T, srv *Server, slug, id string, offset int64, chunk []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPatch, "/n/"+slug+"/import/jobs/"+id, bytes.NewReader(chunk))
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("X-Noteroot-Offset", strconv.FormatInt(offset, 10))
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, req)
	return rec
}

func importJobStatusReq(t *testing.T, srv *Server, slug, id string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/n/"+slug+"/import/jobs/"+id, nil)
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, req)
	return rec
}

func completeImportJobReq(t *testing.T, srv *Server, slug, id string, dryRun bool) *httptest.ResponseRecorder {
	t.Helper()
	target := "/n/" + slug + "/import/jobs/" + id + "/complete"
	if dryRun {
		target += "?dry_run=true"
	}
	req := httptest.NewRequest(http.MethodPost, target, nil)
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, req)
	return rec
}

// uploadInChunks drives a full create → append*N → complete cycle,
// splitting archive into pieces of chunkSize bytes — the normal path a
// resumable client takes with nothing going wrong.
func uploadInChunks(t *testing.T, srv *Server, slug string, archive []byte, chunkSize int) (id string) {
	t.Helper()
	rec := createImportJobReq(t, srv, slug)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d: %s", rec.Code, rec.Body)
	}
	var created map[string]any
	decodeJSON(t, rec, &created)
	id = created["id"].(string)

	var offset int64
	for offset < int64(len(archive)) {
		end := offset + int64(chunkSize)
		if end > int64(len(archive)) {
			end = int64(len(archive))
		}
		arec := appendImportJobReq(t, srv, slug, id, offset, archive[offset:end])
		if arec.Code != http.StatusOK {
			t.Fatalf("append at %d status = %d: %s", offset, arec.Code, arec.Body)
		}
		var resp map[string]any
		decodeJSON(t, arec, &resp)
		newOffset := int64(resp["offset"].(float64))
		if newOffset != end {
			t.Fatalf("offset = %d, want %d", newOffset, end)
		}
		offset = end
	}
	return id
}

func TestImportJob_chunkedUploadAndComplete(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"existing.md": "old"}))
	archive := buildTarGz(t, map[string]string{
		"new.md": "# New Note",
		"a/b.md": "nested",
	})

	id := uploadInChunks(t, srv, "work", archive, 7) // small chunks to exercise many round trips

	st := importJobStatusReq(t, srv, "work", id)
	var stInfo map[string]any
	decodeJSON(t, st, &stInfo)
	if int64(stInfo["offset"].(float64)) != int64(len(archive)) {
		t.Fatalf("status offset = %v, want %d", stInfo["offset"], len(archive))
	}

	rec := completeImportJobReq(t, srv, "work", id, false)
	if rec.Code != http.StatusOK {
		t.Fatalf("complete status = %d: %s", rec.Code, rec.Body)
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

	// Applying consumes the job: it must be gone afterward.
	if rec := importJobStatusReq(t, srv, "work", id); rec.Code != http.StatusNotFound {
		t.Errorf("status after apply = %d, want 404", rec.Code)
	}
}

func TestImportJob_offsetMismatchReportsCurrentOffsetAndCanResume(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"existing.md": "old"}))
	archive := buildTarGz(t, map[string]string{"new.md": "# New"})

	rec := createImportJobReq(t, srv, "work")
	var created map[string]any
	decodeJSON(t, rec, &created)
	id := created["id"].(string)

	// First chunk lands fine.
	first := archive[:len(archive)/2]
	if arec := appendImportJobReq(t, srv, "work", id, 0, first); arec.Code != http.StatusOK {
		t.Fatalf("first append = %d: %s", arec.Code, arec.Body)
	}

	// Client believes it's still at offset 0 (e.g. it never saw the
	// response to the first append because the connection dropped) and
	// retries from scratch — the server must refuse and report where it
	// actually is instead of silently duplicating bytes.
	mismatch := appendImportJobReq(t, srv, "work", id, 0, first)
	if mismatch.Code != http.StatusConflict {
		t.Fatalf("mismatched append = %d, want 409: %s", mismatch.Code, mismatch.Body)
	}
	var conflict map[string]any
	decodeJSON(t, mismatch, &conflict)
	gotOffset := int64(conflict["offset"].(float64))
	if gotOffset != int64(len(first)) {
		t.Fatalf("conflict offset = %d, want %d", gotOffset, len(first))
	}

	// Resuming from the server's reported offset succeeds and the
	// upload completes correctly.
	rest := archive[len(first):]
	if arec := appendImportJobReq(t, srv, "work", id, gotOffset, rest); arec.Code != http.StatusOK {
		t.Fatalf("resumed append = %d: %s", arec.Code, arec.Body)
	}
	crec := completeImportJobReq(t, srv, "work", id, false)
	if crec.Code != http.StatusOK {
		t.Fatalf("complete = %d: %s", crec.Code, crec.Body)
	}
	var resp map[string]any
	decodeJSON(t, crec, &resp)
	entries, _ := resp["entries"].([]any)
	if len(entries) != 1 || entries[0].(map[string]any)["status"] != "created" {
		t.Errorf("entries = %v", entries)
	}
}

func TestImportJob_dryRunKeepsJobForRealCompleteAfter(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"existing.md": "old"}))
	archive := buildTarGz(t, map[string]string{"new.md": "# New"})
	id := uploadInChunks(t, srv, "work", archive, 1<<20)

	dry := completeImportJobReq(t, srv, "work", id, true)
	if dry.Code != http.StatusOK {
		t.Fatalf("dry run status = %d: %s", dry.Code, dry.Body)
	}
	var dryResp map[string]any
	decodeJSON(t, dry, &dryResp)
	if dryResp["dryRun"] != true {
		t.Error("dryRun flag not echoed back")
	}

	// The job must still exist: a dry run consumes nothing.
	if rec := importJobStatusReq(t, srv, "work", id); rec.Code != http.StatusOK {
		t.Fatalf("job missing after dry run: %d", rec.Code)
	}

	real := completeImportJobReq(t, srv, "work", id, false)
	if real.Code != http.StatusOK {
		t.Fatalf("real complete status = %d: %s", real.Code, real.Body)
	}
	var resp map[string]any
	decodeJSON(t, real, &resp)
	entries, _ := resp["entries"].([]any)
	if len(entries) != 1 || entries[0].(map[string]any)["status"] != "created" {
		t.Errorf("entries = %v", entries)
	}
	if rec := importJobStatusReq(t, srv, "work", id); rec.Code != http.StatusNotFound {
		t.Errorf("job should be gone after real apply, status = %d", rec.Code)
	}
}

func TestImportJob_completeWithNoDataUploaded(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"a.md": "x"}))
	rec := createImportJobReq(t, srv, "work")
	var created map[string]any
	decodeJSON(t, rec, &created)
	id := created["id"].(string)

	crec := completeImportJobReq(t, srv, "work", id, false)
	if crec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", crec.Code)
	}
}

func TestImportJob_completeUnknownJobIs404(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"a.md": "x"}))
	if rec := completeImportJobReq(t, srv, "work", newTransferID(), false); rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestImportJob_appendUnknownJobIs404(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"a.md": "x"}))
	if rec := appendImportJobReq(t, srv, "work", newTransferID(), 0, []byte("x")); rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestImportJob_malformedIDIs404(t *testing.T) {
	// See TestExportJob_malformedIDIs404: traversal shapes are handled
	// by net/http's mux before reaching here, and by validTransferID
	// directly in TestValidTransferID.
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"a.md": "x"}))
	for _, bad := range []string{"short", "not-hex-not-hex-not-hex-not-hexxx"} {
		if rec := importJobStatusReq(t, srv, "work", bad); rec.Code != http.StatusNotFound {
			t.Errorf("id %q: status = %d, want 404", bad, rec.Code)
		}
	}
}

func TestImportJob_missingOffsetHeaderRejected(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"a.md": "x"}))
	rec := createImportJobReq(t, srv, "work")
	var created map[string]any
	decodeJSON(t, rec, &created)
	id := created["id"].(string)

	req := httptest.NewRequest(http.MethodPatch, "/n/work/import/jobs/"+id, bytes.NewReader([]byte("x")))
	arec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(arec, req)
	if arec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", arec.Code)
	}
}

func TestImportJob_chunkTooLargeRejectedWithoutCorruptingOffset(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"a.md": "x"}))
	rec := createImportJobReq(t, srv, "work")
	var created map[string]any
	decodeJSON(t, rec, &created)
	id := created["id"].(string)

	oversized := make([]byte, maxImportChunkSize+1)
	arec := appendImportJobReq(t, srv, "work", id, 0, oversized)
	if arec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413: %s", arec.Code, arec.Body)
	}

	// The rejected chunk must not have been partially written: offset
	// stays at 0 and a correctly sized retry at 0 still works.
	st := importJobStatusReq(t, srv, "work", id)
	var stInfo map[string]any
	decodeJSON(t, st, &stInfo)
	if stInfo["offset"].(float64) != 0 {
		t.Fatalf("offset after rejected chunk = %v, want 0", stInfo["offset"])
	}
	okRec := appendImportJobReq(t, srv, "work", id, 0, []byte("ok"))
	if okRec.Code != http.StatusOK {
		t.Fatalf("retry after rejection = %d: %s", okRec.Code, okRec.Body)
	}
}

func TestImportJob_cancelDeletesJob(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"a.md": "x"}))
	rec := createImportJobReq(t, srv, "work")
	var created map[string]any
	decodeJSON(t, rec, &created)
	id := created["id"].(string)
	appendImportJobReq(t, srv, "work", id, 0, []byte("partial"))

	del := httptest.NewRequest(http.MethodDelete, "/n/work/import/jobs/"+id, nil)
	drec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(drec, del)
	if drec.Code != http.StatusOK {
		t.Fatalf("delete status = %d: %s", drec.Code, drec.Body)
	}
	if rec := importJobStatusReq(t, srv, "work", id); rec.Code != http.StatusNotFound {
		t.Errorf("status after cancel = %d, want 404", rec.Code)
	}
}

func TestImportJob_unknownNotebook(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, nil)
	if rec := createImportJobReq(t, srv, "nope"); rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

// --- id / offset helpers ---

func TestValidTransferID(t *testing.T) {
	cases := map[string]bool{
		newTransferID():                    true,
		"":                                 false,
		"short":                            false,
		"../../etc/passwd":                 false,
		"zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz": false, // right length, not hex
		"00000000000000000000000000000000": true,
	}
	for id, want := range cases {
		if got := validTransferID(id); got != want {
			t.Errorf("validTransferID(%q) = %v, want %v", id, got, want)
		}
	}
}

func TestParseOffsetHeader(t *testing.T) {
	cases := []struct {
		in      string
		want    int64
		wantErr bool
	}{
		{"0", 0, false},
		{"42", 42, false},
		{"", 0, true},
		{"-1", 0, true},
		{"abc", 0, true},
		{"12abc", 0, true},
	}
	for _, c := range cases {
		got, err := parseOffsetHeader(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseOffsetHeader(%q) = %d, want error", c.in, got)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("parseOffsetHeader(%q) = (%d, %v), want (%d, nil)", c.in, got, err, c.want)
		}
	}
}

// --- GC sweep ---

func TestSweepTransfers_removesStaleJobsOnly(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"a.md": "x"}))
	nb, _ := srv.reg.Get("work")

	staleExport := exportJobDir(nb, newTransferID())
	freshExport := exportJobDir(nb, newTransferID())
	staleImport := importJobDir(nb, newTransferID())
	freshImport := importJobDir(nb, newTransferID())
	for _, dir := range []string{staleExport, freshExport, staleImport, freshImport} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeAt := func(dir, name string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeAt(staleExport, exportPartName)
	writeAt(freshExport, exportPartName)
	writeAt(staleImport, importDataName)
	writeAt(freshImport, importDataName)

	old := time.Now().Add(-transferTTL - time.Hour)
	if err := os.Chtimes(filepath.Join(staleExport, exportPartName), old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(staleImport, importDataName), old, old); err != nil {
		t.Fatal(err)
	}

	n := srv.sweepTransfers(nb)
	if n != 2 {
		t.Errorf("swept %d jobs, want 2", n)
	}
	if _, err := os.Stat(staleExport); !os.IsNotExist(err) {
		t.Error("stale export job should be removed")
	}
	if _, err := os.Stat(staleImport); !os.IsNotExist(err) {
		t.Error("stale import job should be removed")
	}
	if _, err := os.Stat(freshExport); err != nil {
		t.Error("fresh export job should survive the sweep")
	}
	if _, err := os.Stat(freshImport); err != nil {
		t.Error("fresh import job should survive the sweep")
	}
}

func TestSweepTransfers_emptyJobDirUsesOwnMTime(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"a.md": "x"}))
	nb, _ := srv.reg.Get("work")
	fresh := importJobDir(nb, newTransferID())
	if err := os.MkdirAll(fresh, 0o755); err != nil {
		t.Fatal(err)
	}
	if n := srv.sweepTransfers(nb); n != 0 {
		t.Errorf("swept %d jobs, want 0 (freshly created, no data yet)", n)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Error("freshly created empty job dir should survive")
	}
}
