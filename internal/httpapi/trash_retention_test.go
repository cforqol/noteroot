package httpapi

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"noteroot/internal/config"
	"noteroot/internal/notebooks"
)

type trashListing struct {
	RetentionDays int `json:"retentionDays"`
	Entries       []struct {
		Name      string `json:"name"`
		OrigPath  string `json:"origPath"`
		DeletedAt int64  `json:"deletedAt"`
		ExpiresAt *int64 `json:"expiresAt"`
	} `json:"entries"`
}

// trashOne deletes a note and returns the trash listing.
func trashOne(t *testing.T, srv *Server, rel string) trashListing {
	t.Helper()
	etag := get(t, srv, notePath(rel), nil).Header().Get("ETag")
	if rec := deleteNote(t, srv, putNotePath(rel), map[string]string{"If-Match": etag}); rec.Code != http.StatusOK {
		t.Fatalf("delete %s: status %d: %s", rel, rec.Code, rec.Body)
	}
	return readTrash(t, srv)
}

func readTrash(t *testing.T, srv *Server) trashListing {
	t.Helper()
	var list trashListing
	decodeJSON(t, get(t, srv, "/n/work/trash", nil), &list)
	return list
}

func putJSON(t *testing.T, srv *Server, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, req)
	return rec
}

// trashDir is where the fixture notebook keeps its trash.
func trashDir(t *testing.T, srv *Server) string {
	t.Helper()
	nb, ok := srv.reg.Get("work")
	if !ok {
		t.Fatal("fixture notebook missing")
	}
	return nb.Layout.Trash()
}

func TestTrashList_reportsRetentionAndExpiry(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"n.md": "v1\n"}))
	list := trashOne(t, srv, "n.md")
	if list.RetentionDays != notebooks.DefaultTrashRetentionDays {
		t.Errorf("retentionDays = %d, want %d", list.RetentionDays, notebooks.DefaultTrashRetentionDays)
	}
	if len(list.Entries) != 1 {
		t.Fatalf("entries = %+v", list.Entries)
	}
	e := list.Entries[0]
	if e.ExpiresAt == nil {
		t.Fatal("expiresAt missing while retention is on")
	}
	// 30 days later, give or take the DST-free arithmetic of AddDate.
	if gap := *e.ExpiresAt - e.DeletedAt; gap < 29*24*3600 || gap > 31*24*3600 {
		t.Errorf("expiresAt - deletedAt = %ds, want about 30 days", gap)
	}
}

func TestTrashSettings_neverDropsTheExpiryAndStopsThePurge(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"n.md": "v1\n"}))
	list := trashOne(t, srv, "n.md")
	name := list.Entries[0].Name
	// Backdate it well past any plausible retention.
	ageSidecar(t, trashDir(t, srv), name, -365*24)

	rec := putJSON(t, srv, "/n/work/trash/settings", `{"retentionDays":0}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var got struct {
		RetentionDays int `json:"retentionDays"`
		Purged        int `json:"purged"`
	}
	decodeJSON(t, rec, &got)
	if got.RetentionDays != 0 {
		t.Errorf("retentionDays = %d, want 0", got.RetentionDays)
	}
	if got.Purged != 0 {
		t.Errorf("purged %d entries with retention off", got.Purged)
	}

	after := readTrash(t, srv)
	if after.RetentionDays != 0 {
		t.Errorf("listing retentionDays = %d, want 0", after.RetentionDays)
	}
	if len(after.Entries) != 1 {
		t.Fatalf("entries = %+v, want the note kept", after.Entries)
	}
	if after.Entries[0].ExpiresAt != nil {
		t.Errorf("expiresAt = %v with retention off", *after.Entries[0].ExpiresAt)
	}

	// The boot/periodic sweep leaves a "never" notebook alone too.
	srv.purgeTrash()
	if len(readTrash(t, srv).Entries) != 1 {
		t.Error("sweep purged a notebook set to never")
	}
}

func TestTrashSettings_shorteningRetentionSweepsImmediately(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{
		"old.md": "o\n", "fresh.md": "f\n",
	}))
	trashOne(t, srv, "old.md")
	list := trashOne(t, srv, "fresh.md")
	if len(list.Entries) != 2 {
		t.Fatalf("entries = %+v, want 2", list.Entries)
	}
	var oldName string
	for _, e := range list.Entries {
		if e.OrigPath == "old.md" {
			oldName = e.Name
		}
	}
	ageSidecar(t, trashDir(t, srv), oldName, -10*24) // 10 days ago

	rec := putJSON(t, srv, "/n/work/trash/settings", `{"retentionDays":7}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var got struct {
		RetentionDays int `json:"retentionDays"`
		Purged        int `json:"purged"`
	}
	decodeJSON(t, rec, &got)
	if got.RetentionDays != 7 || got.Purged != 1 {
		t.Errorf("settings response = %+v, want 7 days / 1 purged", got)
	}
	after := readTrash(t, srv)
	if len(after.Entries) != 1 || after.Entries[0].OrigPath != "fresh.md" {
		t.Errorf("entries = %+v, want only fresh.md", after.Entries)
	}
}

func TestTrashSettings_badInput(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"n.md": "v\n"}))
	cases := []struct {
		name, target, body string
		want               int
	}{
		{"not JSON", "/n/work/trash/settings", "{", http.StatusBadRequest},
		{"missing field", "/n/work/trash/settings", `{}`, http.StatusBadRequest},
		{"negative", "/n/work/trash/settings", `{"retentionDays":-1}`, http.StatusBadRequest},
		{"absurd", "/n/work/trash/settings", `{"retentionDays":100000}`, http.StatusBadRequest},
		{"unknown notebook", "/n/nope/trash/settings", `{"retentionDays":7}`, http.StatusNotFound},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if rec := putJSON(t, srv, c.target, c.body); rec.Code != c.want {
				t.Errorf("status = %d, want %d: %s", rec.Code, c.want, rec.Body)
			}
		})
	}
	if readTrash(t, srv).RetentionDays != notebooks.DefaultTrashRetentionDays {
		t.Error("a rejected request changed the setting")
	}
}

func TestTrashSettings_survivesAReload(t *testing.T) {
	reg := notesFixture(t, map[string]string{"n.md": "v\n"})
	srv := newTestServer(t, config.AuthNone, reg)
	if rec := putJSON(t, srv, "/n/work/trash/settings", `{"retentionDays":90}`); rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	nb, _ := reg.Get("work")
	dataDir := filepath.Dir(filepath.Dir(nb.Layout.Root()))
	reloaded, problems := notebooks.Open(dataDir)
	if len(problems) != 0 {
		t.Fatalf("reload problems: %v", problems)
	}
	back, _ := reloaded.Get("work")
	if got := back.Manifest.RetentionDays(); got != 90 {
		t.Errorf("reloaded retention = %d, want 90", got)
	}
}

func TestTrashPurge_nowAndEmptyAll(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{
		"a.md": "a\n", "b.md": "b\n",
	}))
	trashOne(t, srv, "a.md")
	list := trashOne(t, srv, "b.md")
	var aName string
	for _, e := range list.Entries {
		if e.OrigPath == "a.md" {
			aName = e.Name
		}
	}
	ageSidecar(t, trashDir(t, srv), aName, -40*24) // past the 30-day default

	rec := postEmpty(t, srv, "/n/work/trash/purge", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("purge status = %d: %s", rec.Code, rec.Body)
	}
	var got struct {
		Purged int `json:"purged"`
	}
	decodeJSON(t, rec, &got)
	if got.Purged != 1 {
		t.Errorf("purged = %d, want 1", got.Purged)
	}
	if n := len(readTrash(t, srv).Entries); n != 1 {
		t.Fatalf("entries left = %d, want 1", n)
	}

	// ?all=1 ignores retention and clears the lot.
	rec = postEmpty(t, srv, "/n/work/trash/purge?all=1", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("empty status = %d: %s", rec.Code, rec.Body)
	}
	decodeJSON(t, rec, &got)
	if got.Purged != 1 {
		t.Errorf("emptied = %d, want 1", got.Purged)
	}
	if n := len(readTrash(t, srv).Entries); n != 0 {
		t.Errorf("entries left = %d, want none", n)
	}
	if rec := postEmpty(t, srv, "/n/nope/trash/purge", nil); rec.Code != http.StatusNotFound {
		t.Errorf("unknown notebook: %d, want 404", rec.Code)
	}
}

func TestTrashDelete_removesOneEntryForGood(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{
		"a.md": "a\n", "b.md": "b\n",
	}))
	trashOne(t, srv, "a.md")
	list := trashOne(t, srv, "b.md")
	var aName string
	for _, e := range list.Entries {
		if e.OrigPath == "a.md" {
			aName = e.Name
		}
	}

	rec := deleteNote(t, srv, "/n/work/trash?name="+aName, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	after := readTrash(t, srv)
	if len(after.Entries) != 1 || after.Entries[0].OrigPath != "b.md" {
		t.Errorf("entries = %+v, want only b.md", after.Entries)
	}
	// It is gone for good: restoring it now fails.
	if rec := postEmpty(t, srv, "/n/work/trash/restore?name="+aName, nil); rec.Code != http.StatusNotFound {
		t.Errorf("restore after delete: %d, want 404", rec.Code)
	}

	for _, c := range []struct {
		name, target string
		want         int
	}{
		{"missing name", "/n/work/trash", http.StatusBadRequest},
		{"unknown entry", "/n/work/trash?name=nope.md", http.StatusNotFound},
		{"path escape", "/n/work/trash?name=" + url.QueryEscape("../a.md"), http.StatusBadRequest},
		{"unknown notebook", "/n/nope/trash?name=x.md", http.StatusNotFound},
	} {
		t.Run(c.name, func(t *testing.T) {
			if rec := deleteNote(t, srv, c.target, nil); rec.Code != c.want {
				t.Errorf("status = %d, want %d: %s", rec.Code, c.want, rec.Body)
			}
		})
	}
}

func TestTrashSettings_requiresCSRFLikeOtherMutations(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"n.md": "v\n"}))
	// A form content type is exactly what CSRF rejects.
	req := httptest.NewRequest(http.MethodPut, "/n/work/trash/settings",
		strings.NewReader(`{"retentionDays":7}`))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}
