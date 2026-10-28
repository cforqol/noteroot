package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"noteroot/internal/config"
)

// endpointCase describes one (endpoint, auth-mode, header-present) scenario.
type endpointCase struct {
	name       string
	method     string
	path       string
	authMode   config.AuthMode
	sendHeader bool
	wantStatus int
}

var endpointCases = []endpointCase{
	// /healthz — always unauthenticated
	{name: "healthz/none", method: http.MethodGet, path: "/healthz", authMode: config.AuthNone, sendHeader: false, wantStatus: http.StatusOK},
	{name: "healthz/forward/no-header", method: http.MethodGet, path: "/healthz", authMode: config.AuthForward, sendHeader: false, wantStatus: http.StatusOK},
	{name: "healthz/forward/with-header", method: http.MethodGet, path: "/healthz", authMode: config.AuthForward, sendHeader: true, wantStatus: http.StatusOK},

	// /readyz — always unauthenticated
	{name: "readyz/none", method: http.MethodGet, path: "/readyz", authMode: config.AuthNone, sendHeader: false, wantStatus: http.StatusOK},
	{name: "readyz/forward/no-header", method: http.MethodGet, path: "/readyz", authMode: config.AuthForward, sendHeader: false, wantStatus: http.StatusOK},
	{name: "readyz/forward/with-header", method: http.MethodGet, path: "/readyz", authMode: config.AuthForward, sendHeader: true, wantStatus: http.StatusOK},

	// /api/v1/notebooks — intentionally unauthenticated (first-run wizard detection)
	{name: "notebooks/none", method: http.MethodGet, path: "/api/v1/notebooks", authMode: config.AuthNone, sendHeader: false, wantStatus: http.StatusOK},
	{name: "notebooks/forward/no-header", method: http.MethodGet, path: "/api/v1/notebooks", authMode: config.AuthForward, sendHeader: false, wantStatus: http.StatusOK},
	{name: "notebooks/forward/with-header", method: http.MethodGet, path: "/api/v1/notebooks", authMode: config.AuthForward, sendHeader: true, wantStatus: http.StatusOK},

	// /n/{slug}/ — protected, known slug
	{name: "notebookroot/none/found", method: http.MethodGet, path: "/n/work/", authMode: config.AuthNone, sendHeader: false, wantStatus: http.StatusOK},
	{name: "notebookroot/forward/no-header", method: http.MethodGet, path: "/n/work/", authMode: config.AuthForward, sendHeader: false, wantStatus: http.StatusUnauthorized},
	{name: "notebookroot/forward/with-header", method: http.MethodGet, path: "/n/work/", authMode: config.AuthForward, sendHeader: true, wantStatus: http.StatusOK},

	// /n/{slug}/ — not found (auth checked first)
	{name: "notebookroot/none/missing", method: http.MethodGet, path: "/n/missing/", authMode: config.AuthNone, sendHeader: false, wantStatus: http.StatusNotFound},
	{name: "notebookroot/forward/missing/no-header", method: http.MethodGet, path: "/n/missing/", authMode: config.AuthForward, sendHeader: false, wantStatus: http.StatusUnauthorized},
	{name: "notebookroot/forward/missing/with-header", method: http.MethodGet, path: "/n/missing/", authMode: config.AuthForward, sendHeader: true, wantStatus: http.StatusNotFound},
}

func TestEndpoints_authModes(t *testing.T) {
	reg := openTestRegistry(t, "work")

	for _, tc := range endpointCases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newTestServer(t, tc.authMode, reg)
			req := httptest.NewRequest(tc.method, tc.path, nil)
			if tc.sendHeader {
				req.Header.Set("X-Auth-User", "alice")
			}
			rec := httptest.NewRecorder()
			srv.http.Handler.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			// All responses must be valid JSON with correct content-type.
			if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
				t.Errorf("Content-Type = %q, want application/json; charset=utf-8", ct)
			}
			var body any
			if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
				t.Errorf("body is not valid JSON: %v", err)
			}
		})
	}
}

// Verify specific response shapes for the OK cases.

func TestHealthz_body(t *testing.T) {
	srv := newTestServer(t, config.AuthNone, nil)
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("status = %q, want ok", body["status"])
	}
}

func TestReadyz_body(t *testing.T) {
	reg := openTestRegistry(t, "work", "personal")
	srv := newTestServer(t, config.AuthNone, reg)
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["status"] != "ready" {
		t.Errorf("status = %q, want ready", body["status"])
	}
	// notebooks count comes back as float64 from JSON
	if count, ok := body["notebooks"].(float64); !ok || int(count) != 2 {
		t.Errorf("notebooks = %v, want 2", body["notebooks"])
	}
}

func TestNotebooksList_body(t *testing.T) {
	reg := openTestRegistry(t, "alpha", "beta")
	srv := newTestServer(t, config.AuthNone, reg)
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/notebooks", nil))

	var body struct {
		Notebooks []struct {
			Slug    string `json:"slug"`
			Name    string `json:"name"`
			Profile string `json:"profile"`
		} `json:"notebooks"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Notebooks) != 2 {
		t.Fatalf("notebooks count = %d, want 2", len(body.Notebooks))
	}
	// Registry returns notebooks sorted; alpha before beta.
	if body.Notebooks[0].Slug != "alpha" {
		t.Errorf("notebooks[0].slug = %q, want alpha", body.Notebooks[0].Slug)
	}
	if body.Notebooks[1].Slug != "beta" {
		t.Errorf("notebooks[1].slug = %q, want beta", body.Notebooks[1].Slug)
	}
}

func TestNotebookRoot_body(t *testing.T) {
	reg := openTestRegistry(t, "work")
	srv := newTestServer(t, config.AuthNone, reg)
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/n/work/", nil))

	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["slug"] != "work" {
		t.Errorf("slug = %q, want work", body["slug"])
	}
	if body["name"] != "work" {
		t.Errorf("name = %q, want work", body["name"])
	}
}

func TestUnauthorized_body(t *testing.T) {
	// Use a still-protected endpoint to verify the 401 error body shape.
	// (GET /api/v1/notebooks is intentionally unauthenticated for first-run.)
	srv := newTestServer(t, config.AuthForward, notesFixture(t, map[string]string{"a.md": "hi"}))
	rec := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/n/work/tree", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["error"] != "unauthenticated" {
		t.Errorf("error = %q, want unauthenticated", body["error"])
	}
}
