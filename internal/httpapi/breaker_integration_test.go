package httpapi

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"noteroot/internal/config"
	"noteroot/internal/notebooks"
)

// twoNotebookRegistry builds a registry with a healthy "work" notebook
// and a "bad" notebook whose vault directory is removed right after
// creation — a real broken vault (per the issue text), not a mock: a
// vault.Tree() on it fails every time with vault.ErrNotFound, exactly
// like the on-disk failure this breaker exists to isolate. Nothing
// quarantines or recreates it, so the failure is persistent across
// requests rather than self-healing like a corrupt sqlite file would
// be (see state.OpenDB).
func twoNotebookRegistry(t *testing.T) *notebooks.Registry {
	t.Helper()
	dataDir := t.TempDir()

	workDir := filepath.Join(dataDir, "notebooks", "work")
	writeTestManifest(t, workDir, "work")
	writeVaultFile(t, filepath.Join(workDir, notebooks.VaultDir), "a.md", "hello world\n")

	badDir := filepath.Join(dataDir, "notebooks", "bad")
	writeTestManifest(t, badDir, "bad")
	writeVaultFile(t, filepath.Join(badDir, notebooks.VaultDir), "a.md", "hello world\n")
	if err := os.RemoveAll(filepath.Join(badDir, notebooks.VaultDir)); err != nil {
		t.Fatal(err)
	}

	reg, problems := notebooks.Open(dataDir)
	if len(problems) != 0 {
		t.Fatalf("notebooks.Open problems: %v", problems)
	}
	return reg
}

func TestNotebookBreaker_integration_isolatesBrokenVaultFromHealthySibling(t *testing.T) {
	reg := twoNotebookRegistry(t)
	cfg := config.Config{
		DataDir:          filepath.Join(t.TempDir(), "data"),
		ListenAddr:       ":0",
		LogLevel:         "info",
		AuthMode:         config.AuthNone,
		BreakerThreshold: 1,
		BreakerCooldown:  "30ms",
	}
	srv := New(cfg, newTestLogger(), reg)

	badSearch := "/n/bad/search?q=" + url.QueryEscape("hello")
	workSearch := "/n/work/search?q=" + url.QueryEscape("hello")

	// The broken vault fails for real, not as a mock: the first
	// request against it genuinely gets a 500, and that trips the
	// breaker at threshold 1.
	rec := get(t, srv, badSearch, nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("first request against broken notebook: status = %d, want 500: %s", rec.Code, rec.Body)
	}

	// Threshold reached: the breaker opens and the next request fails
	// fast with 503, without the handler re-attempting the broken
	// vault scan.
	rec = get(t, srv, badSearch, nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status after threshold = %d, want 503: %s", rec.Code, rec.Body)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("missing Retry-After header on breaker-open response")
	}
	if st := srv.breakers.State("bad"); !st.Open {
		t.Errorf("breaker state = %+v, want Open=true", st)
	}

	// The healthy sibling notebook is completely unaffected: isolation
	// means a broken notebook's failures and its breaker never cross
	// into another notebook's request path.
	rec = get(t, srv, workSearch, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("healthy sibling notebook status = %d, want 200: %s", rec.Code, rec.Body)
	}

	// After the cooldown elapses, exactly one probe is let through.
	// The vault is still missing, so the probe fails and — at
	// threshold 1 — immediately reopens the breaker, rather than
	// latching open forever or silently staying closed.
	time.Sleep(60 * time.Millisecond)
	rec = get(t, srv, badSearch, nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("half-open probe status = %d, want 500 (handler ran and failed again): %s", rec.Code, rec.Body)
	}
	if st := srv.breakers.State("bad"); !st.Open || st.Trips < 2 {
		t.Errorf("breaker state after reopening = %+v, want Open=true and Trips>=2", st)
	}

	// Fixing the notebook (restoring the vault) and letting the
	// cooldown elapse again closes the circuit back up.
	nb, ok := reg.Get("bad")
	if !ok {
		t.Fatal("notebook disappeared")
	}
	writeVaultFile(t, nb.Layout.Vault(), "a.md", "hello world\n")
	time.Sleep(60 * time.Millisecond)
	rec = get(t, srv, badSearch, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status after fix = %d, want 200: %s", rec.Code, rec.Body)
	}
	if st := srv.breakers.State("bad"); st.Open {
		t.Errorf("breaker still open after the notebook was fixed: %+v", st)
	}
}

// TestNotebookBreaker_integration_adminRoutesBypassBreaker proves the
// deliberate exemption: notebook management (rename/delete) must keep
// working even while a notebook's breaker is tripped open, or there
// would be no way to recover a broken notebook short of a restart.
func TestNotebookBreaker_integration_adminRoutesBypassBreaker(t *testing.T) {
	reg := twoNotebookRegistry(t)
	cfg := config.Config{
		DataDir:          filepath.Join(t.TempDir(), "data"),
		ListenAddr:       ":0",
		LogLevel:         "info",
		AuthMode:         config.AuthNone,
		BreakerThreshold: 1,
		BreakerCooldown:  time.Hour.String(),
	}
	srv := New(cfg, newTestLogger(), reg)

	if rec := get(t, srv, "/n/bad/search?q=x", nil); rec.Code != http.StatusInternalServerError {
		t.Fatalf("priming failure: status = %d, want 500", rec.Code)
	}
	if !srv.breakers.State("bad").Open {
		t.Fatal("breaker should be open after one strike at threshold 1")
	}

	rec := notebookReq(t, srv, http.MethodDelete, "/api/v1/notebooks/bad", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("delete notebook status = %d, want 200 even with an open breaker: %s", rec.Code, rec.Body)
	}
}

// TestNotebookBreaker_integration_bearerRouteTripsAndBlocks proves the
// breaker also guards the bearer-token /api/v1/n/{slug}/... surface
// (wired inside bearer() rather than via the nbAuth/nbMutating
// middleware chain), and that an invalid token never trips or resets
// it.
func TestNotebookBreaker_integration_bearerRouteTripsAndBlocks(t *testing.T) {
	reg := twoNotebookRegistry(t)
	cfg := config.Config{
		DataDir:          filepath.Join(t.TempDir(), "data"),
		ListenAddr:       ":0",
		LogLevel:         "info",
		AuthMode:         config.AuthNone,
		BreakerThreshold: 1,
		BreakerCooldown:  time.Hour.String(),
	}
	srv := New(cfg, newTestLogger(), reg)

	tok := issueTokenFor(t, srv, "bad", `{"name":"ci","scopes":"search:read"}`)
	bearer := map[string]string{"Authorization": "Bearer " + tok.Token}

	// An invalid token must not touch the breaker at all: this is a
	// 401, strictly neutral, before the handler (and the vault) are
	// ever reached.
	bad := doReq(t, srv, http.MethodGet, "/api/v1/n/bad/search?q=x", "", map[string]string{"Authorization": "Bearer wrong"})
	if bad.Code != http.StatusUnauthorized {
		t.Fatalf("invalid token status = %d, want 401", bad.Code)
	}
	if st := srv.breakers.State("bad"); st.Open || st.Strikes != 0 {
		t.Errorf("breaker touched by an invalid token: %+v", st)
	}

	// A valid, correctly scoped token hits the real broken vault and
	// trips the breaker at threshold 1.
	rec := doReq(t, srv, http.MethodGet, "/api/v1/n/bad/search?q=x", "", bearer)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("first bearer request against broken notebook: status = %d, want 500: %s", rec.Code, rec.Body)
	}
	if !srv.breakers.State("bad").Open {
		t.Fatal("breaker should be open after one strike at threshold 1")
	}

	// Further valid requests fail fast without reaching the handler.
	rec = doReq(t, srv, http.MethodGet, "/api/v1/n/bad/search?q=x", "", bearer)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status after threshold = %d, want 503: %s", rec.Code, rec.Body)
	}
}
