package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"noteroot/internal/config"
)

func getAdminHealth(t *testing.T, srv *Server, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/admin/health", nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, req)
	return rec
}

func TestAdminHealth_requiresAuthWhenForward(t *testing.T) {
	srv := newTestServer(t, config.AuthForward, resourceFixture(t))
	if rec := getAdminHealth(t, srv, nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("status without X-Auth-User = %d, want 401", rec.Code)
	}
	if rec := getAdminHealth(t, srv, map[string]string{"X-Auth-User": "alice"}); rec.Code != http.StatusOK {
		t.Errorf("status with X-Auth-User = %d, want 200", rec.Code)
	}
}

func TestAdminHealth_listsEveryNotebook(t *testing.T) {
	reg := twoNotebookRegistry(t)
	srv := New(config.Config{
		DataDir: t.TempDir(), ListenAddr: ":0", LogLevel: "info", AuthMode: config.AuthNone,
	}, newTestLogger(), reg)

	rec := getAdminHealth(t, srv, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
	body := rec.Body.String()
	for _, want := range []string{"work", "bad"} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing notebook %q: %s", want, body)
		}
	}
}

func TestAdminHealth_showsBreakerStatus(t *testing.T) {
	reg := twoNotebookRegistry(t)
	srv := New(config.Config{
		DataDir: t.TempDir(), ListenAddr: ":0", LogLevel: "info", AuthMode: config.AuthNone,
		BreakerThreshold: 1, BreakerCooldown: "1h",
	}, newTestLogger(), reg)

	if rec := get(t, srv, "/n/bad/search?q=x", nil); rec.Code != http.StatusInternalServerError {
		t.Fatalf("priming failure status = %d, want 500", rec.Code)
	}

	body := getAdminHealth(t, srv, nil).Body.String()
	badRow := rowFor(body, "bad")
	if !strings.Contains(badRow, "open") {
		t.Errorf("bad notebook's row doesn't show an open breaker: %s", badRow)
	}
	workRow := rowFor(body, "work")
	if !strings.Contains(workRow, "closed") {
		t.Errorf("healthy work notebook's row doesn't show a closed breaker: %s", workRow)
	}
}

// rowFor returns the <tr>...</tr> fragment containing slug, for
// assertions scoped to one notebook's row rather than the whole page
// (both "open" and "closed" appear somewhere on any multi-notebook
// page, so a substring check against the whole body can't tell them
// apart).
func rowFor(body, slug string) string {
	idx := strings.Index(body, "<td>"+slug+"</td>")
	if idx < 0 {
		return ""
	}
	end := strings.Index(body[idx:], "</tr>")
	if end < 0 {
		return body[idx:]
	}
	return body[idx : idx+end]
}

func TestAdminHealth_showsGlobalQuotaLineWhenEnabled(t *testing.T) {
	srv := newGlobalQuotaServer(t, 0, 5000)
	body := getAdminHealth(t, srv, nil).Body.String()
	if !strings.Contains(body, "Global quota") {
		t.Errorf("page missing global quota line: %s", body)
	}
	if !strings.Contains(body, "5000") {
		t.Errorf("page missing the configured global quota bytes: %s", body)
	}
}

func TestAdminHealth_omitsGlobalQuotaLineWhenDisabled(t *testing.T) {
	srv := newGlobalQuotaServer(t, 0, 0)
	body := getAdminHealth(t, srv, nil).Body.String()
	if strings.Contains(body, "Global quota") {
		t.Errorf("page shows a global quota line when the feature is disabled: %s", body)
	}
}
