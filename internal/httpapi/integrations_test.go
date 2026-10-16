package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"noteroot/internal/config"
	"noteroot/internal/integrations"
)

// writeConfig drops a toml config into the fixture notebook's
// integrations or hooks directory.
func writeConfig(t *testing.T, srv *Server, kind, name, body string) {
	t.Helper()
	nb, ok := srv.reg.Get("work")
	if !ok {
		t.Fatal("fixture notebook missing")
	}
	dir := nb.Layout.Integrations()
	if kind == "hooks" {
		dir = nb.Layout.Hooks()
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func setSecret(t *testing.T, srv *Server, kind, name, secret string) {
	t.Helper()
	rec := doReq(t, srv, http.MethodPost, "/n/work/"+kind+"/"+name+"/secret",
		`{"secret":"`+secret+`"}`, map[string]string{"Content-Type": "application/json"})
	if rec.Code != http.StatusOK {
		t.Fatalf("set secret = %d, body=%s", rec.Code, rec.Body)
	}
}

func TestIntegrations_outboundWebhook(t *testing.T) {
	var (
		mu      sync.Mutex
		gotHdr  http.Header
		gotBody map[string]any
	)
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotHdr = r.Header.Clone()
		_ = json.Unmarshal(b, &gotBody)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer receiver.Close()

	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"a.md": "original\n"}))
	writeConfig(t, srv, "integrations", "sink",
		"url = \""+receiver.URL+"\"\nevents = [\"note.write\"]\n")
	reload := doReq(t, srv, http.MethodPost, "/n/work/integrations/reload", "", nil)
	if reload.Code != http.StatusOK || !strings.Contains(reload.Body.String(), `"sink"`) {
		t.Fatalf("reload = %d, body=%s", reload.Code, reload.Body)
	}
	setSecret(t, srv, "integrations", "sink", "topsecret")
	list := doReq(t, srv, http.MethodGet, "/n/work/integrations", "", nil)
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"has_secret":true`) {
		t.Errorf("list = %d, body=%s", list.Code, list.Body)
	}

	put := doReq(t, srv, http.MethodPut, notePath("a.md"), "updated\n", nil)
	if put.Code != http.StatusOK && put.Code != http.StatusCreated {
		t.Fatalf("PUT = %d, body=%s", put.Code, put.Body)
	}

	deadline := time.Now().Add(4 * time.Second)
	for {
		mu.Lock()
		ok := gotBody != nil
		mu.Unlock()
		if ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("delivery never arrived")
		}
		time.Sleep(50 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if gotHdr.Get("X-Noteroot-Event") != "note.write" ||
		gotHdr.Get("X-Noteroot-Slug") != "work" {
		t.Errorf("headers = %v", gotHdr)
	}
	if !strings.HasPrefix(gotHdr.Get("X-Noteroot-Signature"), "sha256=") {
		t.Errorf("signature = %q", gotHdr.Get("X-Noteroot-Signature"))
	}
	if gotBody["event"] != "note.write" || gotBody["path"] != "a.md" {
		t.Errorf("body = %v", gotBody)
	}
}

func TestIntegrations_inboundWebhook(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"a.md": "x\n"}))
	writeConfig(t, srv, "hooks", "inbox",
		"path = \"inbox/from-ci.md\"\nmode = \"append\"\n")
	reload := doReq(t, srv, http.MethodPost, "/n/work/integrations/reload", "", nil)
	if reload.Code != http.StatusOK || !strings.Contains(reload.Body.String(), `"inbox"`) {
		t.Fatalf("reload = %d, body=%s", reload.Code, reload.Body)
	}
	setSecret(t, srv, "hooks", "inbox", "topsecret")

	body := "build 42 green\n"
	sig := integrations.Sign("topsecret", []byte(body))
	good := doReq(t, srv, http.MethodPost, "/hooks/work/inbox", body, map[string]string{
		"Content-Type":         "text/plain",
		"X-Noteroot-Signature": sig,
		"X-Noteroot-Depth":     "0",
	})
	if good.Code != http.StatusOK {
		t.Fatalf("hook POST = %d, body=%s", good.Code, good.Body)
	}
	if !strings.Contains(good.Body.String(), "inbox/from-ci.md") {
		t.Errorf("hook response = %s", good.Body)
	}
	note := doReq(t, srv, http.MethodGet, "/n/work/note?path=inbox/from-ci.md", "", nil)
	if note.Code != http.StatusOK || !strings.Contains(note.Body.String(), "build 42 green") {
		t.Errorf("note = %d, body=%s", note.Code, note.Body)
	}

	// Second POST appends a new section (append mode).
	if rec := doReq(t, srv, http.MethodPost, "/hooks/work/inbox", "again\n", map[string]string{
		"X-Noteroot-Signature": integrations.Sign("topsecret", []byte("again\n")),
	}); rec.Code != http.StatusOK {
		t.Fatalf("second POST = %d", rec.Code)
	}
	note = doReq(t, srv, http.MethodGet, "/n/work/note?path=inbox/from-ci.md", "", nil)
	if !strings.Contains(note.Body.String(), "build 42 green") ||
		!strings.Contains(note.Body.String(), "again") {
		t.Errorf("append lost content: %s", note.Body)
	}

	// Attributed to the hook in the audit trail.
	nb, _ := srv.reg.Get("work")
	entries, err := auditEntries(t, nb.Layout.PlatformDB())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range entries {
		if e.Action == "note.write" && e.Actor == "hook:inbox" {
			found = true
		}
	}
	if !found {
		t.Errorf("audit missing hook:inbox write: %+v", entries)
	}

	// Failure modes.
	bad := doReq(t, srv, http.MethodPost, "/hooks/work/inbox", body, map[string]string{
		"X-Noteroot-Signature": "sha256=bad",
	})
	if bad.Code != http.StatusUnauthorized {
		t.Errorf("bad sig = %d, want 401", bad.Code)
	}
	unknown := doReq(t, srv, http.MethodPost, "/hooks/work/ghost", body, map[string]string{
		"X-Noteroot-Signature": sig,
	})
	if unknown.Code != http.StatusNotFound {
		t.Errorf("unknown = %d, want 404", unknown.Code)
	}
	deep := doReq(t, srv, http.MethodPost, "/hooks/work/inbox", body, map[string]string{
		"X-Noteroot-Signature": sig,
		"X-Noteroot-Depth":     "5",
	})
	if deep.Code != http.StatusConflict {
		t.Errorf("deep = %d, want 409", deep.Code)
	}
}

// The loop case: an integration whose receiver echoes deliveries back
// as hook posts, each hop +1 depth. The chain must die at the bus
// limit — bounded deliveries, bounded writes, no unbounded growth.
func TestIntegrations_echoLoopTerminates(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, notesFixture(t, map[string]string{"a.md": "x\n"}))

	// The receiver echoes into the API; the base URL is assigned
	// after start and read at call time through the closure.
	apiBase := ""
	var (
		mu   sync.Mutex
		hits int
		last int
	)
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if !integrations.Verify("topsecret", b, r.Header.Get("X-Noteroot-Signature")) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var d struct {
			Depth int `json:"depth"`
		}
		_ = json.Unmarshal(b, &d)
		mu.Lock()
		hits++
		mu.Unlock()
		req, err := http.NewRequest(http.MethodPost, apiBase+"/hooks/work/echo-in",
			strings.NewReader("loop body\n"))
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		req.Header.Set("Content-Type", "text/plain")
		req.Header.Set("X-Noteroot-Signature", integrations.Sign("topsecret", []byte("loop body\n")))
		req.Header.Set("X-Noteroot-Depth", strconv.Itoa(d.Depth+1))
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			mu.Lock()
			last = resp.StatusCode
			mu.Unlock()
			_ = resp.Body.Close()
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer receiver.Close()
	// Give the API a real address so the receiver can echo back into
	// the webhook endpoint — the same round trip a real integration
	// makes.
	api := httptest.NewServer(srv.http.Handler)
	defer api.Close()
	apiBase = api.URL

	writeConfig(t, srv, "integrations", "echo",
		"url = \""+receiver.URL+"/sink\"\nevents = [\"note.write\"]\n")
	writeConfig(t, srv, "hooks", "echo-in",
		"path = \"inbox/echo.md\"\nmode = \"append\"\n")
	if rec := doReq(t, srv, http.MethodPost, "/n/work/integrations/reload", "", nil); rec.Code != http.StatusOK {
		t.Fatalf("reload = %d", rec.Code)
	}
	setSecret(t, srv, "integrations", "echo", "topsecret")
	setSecret(t, srv, "hooks", "echo-in", "topsecret")

	put := doReq(t, srv, http.MethodPut, notePath("a.md"), "start the loop\n", nil)
	if put.Code != http.StatusOK && put.Code != http.StatusCreated {
		t.Fatalf("PUT = %d", put.Code)
	}

	// Wait for the chain to die out: the last receiver hit must have
	// gotten a 409 (depth at the limit) — that's the terminating hop.
	deadline := time.Now().Add(15 * time.Second)
	for {
		mu.Lock()
		lastCode := last
		mu.Unlock()
		if lastCode == http.StatusConflict || time.Now().After(deadline) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	mu.Lock()
	finalHits, lastCode := hits, last
	mu.Unlock()
	if lastCode != http.StatusConflict {
		t.Errorf("loop did not terminate at the depth limit: last status %d after %d hops", lastCode, finalHits)
	}
	if finalHits > 6 {
		t.Errorf("loop not bounded: %d deliveries", finalHits)
	}
	note := doReq(t, srv, http.MethodGet, "/n/work/note?path=inbox/echo.md", "", nil)
	if note.Code != http.StatusOK {
		t.Errorf("echo note = %d", note.Code)
	}
}
