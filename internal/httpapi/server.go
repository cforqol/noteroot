package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"noteroot/internal/config"
	"noteroot/internal/git"
	"noteroot/internal/integrations"
	"noteroot/internal/notebooks"
	"noteroot/internal/platform"
	"noteroot/internal/plugins"
	"noteroot/internal/vault"
	"noteroot/web"
)

type Server struct {
	cfg   config.Config
	log   *slog.Logger
	reg   *notebooks.Registry
	plat  *platform.Manager
	plug  *plugins.Manager
	integ *integrations.Manager
	http  *http.Server

	gitMu sync.Mutex
	git   map[string]*git.Flusher

	stopGC func()
}

func New(cfg config.Config, log *slog.Logger, reg *notebooks.Registry) *Server {
	s := &Server{cfg: cfg, log: log, reg: reg, plat: platform.NewManager(log)}
	s.plug = plugins.NewManager(log, func(slug string) plugins.NoteStore {
		return &pluginNoteStore{s: s, slug: slug}
	})
	s.integ = integrations.NewManager(log, func(slug string) plugins.NoteStore {
		return &pluginNoteStore{s: s, slug: slug}
	})

	mux := http.NewServeMux()

	// Unauthenticated
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /readyz", s.ready)
	mux.Handle("/", http.FileServerFS(web.FS))

	// Authenticated
	auth := func(h http.HandlerFunc) http.Handler {
		return Chain(h, Auth(cfg.AuthMode))
	}
	// Mutations additionally require a non-form content type and a matching
	// Origin/Referer — see CSRF.
	mutating := func(h http.HandlerFunc) http.Handler {
		return Chain(h, Auth(cfg.AuthMode), CSRF(cfg.PublicOrigin))
	}
	mux.Handle("GET /api/v1/notebooks", auth(s.listNotebooks))
	mux.Handle("GET /n/{slug}/", auth(s.notebookRoot))
	mux.Handle("GET /n/{slug}/tree", auth(s.tree))
	mux.Handle("GET /n/{slug}/note", auth(s.note))
	mux.Handle("GET /n/{slug}/raw", auth(s.raw))
	mux.Handle("GET /n/{slug}/search", auth(s.search))
	mux.Handle("GET /n/{slug}/trash", auth(s.listTrash))
	mux.Handle("PUT /n/{slug}/note", mutating(s.putNote))
	mux.Handle("PATCH /n/{slug}/note", mutating(s.moveNote))
	mux.Handle("DELETE /n/{slug}/note", mutating(s.deleteNote))
	mux.Handle("POST /n/{slug}/trash/restore", mutating(s.restoreTrash))
	mux.Handle("PUT /n/{slug}/lock", mutating(s.putLock))
	mux.Handle("DELETE /n/{slug}/lock", mutating(s.deleteLock))

	// Resources (stage 5) — uploads allow multipart via CSRFAllowMultipart.
	mux.Handle("POST /n/{slug}/resources", Chain(http.HandlerFunc(s.uploadResource), Auth(cfg.AuthMode), CSRFAllowMultipart(cfg.PublicOrigin)))
	mux.Handle("POST /n/{slug}/resources/presign", mutating(s.presignUpload))
	mux.Handle("POST /n/{slug}/resources/confirm", mutating(s.confirmUpload))
	mux.Handle("POST /n/{slug}/resources/gc", mutating(s.gcResources))

	// Stage 8: backup, import/export.
	mux.Handle("POST /n/{slug}/backup", mutating(s.createBackup))
	mux.Handle("GET /n/{slug}/export", auth(s.exportNotebook))
	mux.Handle("POST /n/{slug}/import", Chain(http.HandlerFunc(s.importNotebook), Auth(cfg.AuthMode), CSRFAllowMultipart(cfg.PublicOrigin)))
	mux.Handle("GET /n/{slug}/resources", auth(s.listResources))
	mux.Handle("GET /n/{slug}/resources/stats", auth(s.resourceStats))
	mux.Handle("GET /n/{slug}/resources/{sha}", auth(s.getResource))
	mux.Handle("GET /n/{slug}/resources/{sha}/thumb", auth(s.thumbResource))
	mux.Handle("GET /n/{slug}/resources/{sha}/poster", auth(s.posterResource))
	mux.Handle("GET /n/{slug}/resources/{sha}/presign-read", auth(s.presignRead))
	mux.Handle("DELETE /n/{slug}/resources/{sha}", mutating(s.deleteResource))

	// Stage 9 phase 2: scoped API tokens (human-managed under the
	// forward-auth surface) and the token-authenticated machine API.
	mux.Handle("GET /n/{slug}/tokens", auth(s.listTokens))
	mux.Handle("POST /n/{slug}/tokens", mutating(s.createToken))
	mux.Handle("DELETE /n/{slug}/tokens", mutating(s.revokeToken))

	// Stage 9 phase 3: plugin management and command dispatch.
	mux.Handle("GET /n/{slug}/plugins", auth(s.listPlugins))
	mux.Handle("GET /n/{slug}/plugins/panels", auth(s.pluginPanels))
	mux.Handle("POST /n/{slug}/plugins/reload", mutating(s.reloadPlugins))
	mux.Handle("POST /n/{slug}/plugins/{name}/commands/{command}", mutating(s.runPluginCommand))

	// Stage 9 phase 4: integration and hook management (human) plus
	// the inbound webhook endpoint (HMAC-authenticated machine).
	mux.Handle("GET /n/{slug}/integrations", auth(s.listIntegrations))
	mux.Handle("GET /n/{slug}/hooks", auth(s.listHooks))
	mux.Handle("POST /n/{slug}/integrations/reload", mutating(s.reloadIntegrations))
	mux.Handle("POST /n/{slug}/integrations/{name}/secret", mutating(s.setIntegrationSecret))
	mux.Handle("POST /n/{slug}/hooks/{name}/secret", mutating(s.setHookSecret))
	mux.HandleFunc("POST /hooks/{slug}/{name}", s.handleWebhook)

	// /api/v1: bearer tokens with resource:action[:qualifier] scopes.
	// No forward-auth and no CSRF here — the Authorization header
	// cannot be attached cross-site without a CORS grant the server
	// never issues. Handlers are reused verbatim; the middleware
	// decides who the actor is and whether the scope allows.
	qpath := func(r *http.Request) string { return r.URL.Query().Get("path") }
	qnone := func(*http.Request) string { return "" }
	qname := func(r *http.Request) string { return r.URL.Query().Get("name") }
	qsha := func(r *http.Request) string { return r.PathValue("sha") }
	mux.Handle("GET /api/v1/n/{slug}/note", s.bearer("note", "read", qpath, s.note))
	mux.Handle("PUT /api/v1/n/{slug}/note", s.bearer("note", "write", qpath, s.putNote))
	mux.Handle("PATCH /api/v1/n/{slug}/note", s.bearer("note", "write", qpath, s.moveNote))
	mux.Handle("DELETE /api/v1/n/{slug}/note", s.bearer("note", "write", qpath, s.deleteNote))
	mux.Handle("GET /api/v1/n/{slug}/raw", s.bearer("note", "read", qpath, s.raw))
	mux.Handle("GET /api/v1/n/{slug}/search", s.bearer("search", "read", qnone, s.search))
	mux.Handle("GET /api/v1/n/{slug}/tree", s.bearer("tree", "read", qnone, s.tree))
	mux.Handle("GET /api/v1/n/{slug}/trash", s.bearer("trash", "read", qnone, s.listTrash))
	mux.Handle("POST /api/v1/n/{slug}/trash/restore", s.bearer("trash", "write", qname, s.restoreTrash))
	mux.Handle("GET /api/v1/n/{slug}/resources", s.bearer("resources", "read", qnone, s.listResources))
	mux.Handle("GET /api/v1/n/{slug}/resources/stats", s.bearer("resources", "read", qnone, s.resourceStats))
	mux.Handle("GET /api/v1/n/{slug}/resources/{sha}", s.bearer("resources", "read", qsha, s.getResource))
	mux.Handle("GET /api/v1/n/{slug}/resources/{sha}/thumb", s.bearer("resources", "read", qsha, s.thumbResource))
	mux.Handle("GET /api/v1/n/{slug}/resources/{sha}/poster", s.bearer("resources", "read", qsha, s.posterResource))
	mux.Handle("DELETE /api/v1/n/{slug}/resources/{sha}", s.bearer("resources", "write", qsha, s.deleteResource))
	mux.Handle("GET /api/v1/n/{slug}/export", s.bearer("export", "read", qnone, s.exportNotebook))
	mux.Handle("POST /api/v1/n/{slug}/backup", s.bearer("backup", "write", qnone, s.createBackup))

	h := Chain(mux, RequestID, Recover(log), Log(log), SharedArrayBuffer)
	s.http = &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	s.git = map[string]*git.Flusher{}
	s.purgeTrash()
	s.stopGC = s.startGC()
	// Load every notebook's extensions at boot so hooks and
	// integrations are live before the first mutation reaches them.
	// Failures log and continue: extensions degrade, notebooks do not.
	for _, nb := range s.reg.List() {
		slug := nb.Manifest.Slug
		if err := s.plug.Ensure(slug, nb.Layout, s.plat.Bus(slug)); err != nil {
			s.log.Error("plugin load failed", "notebook", slug, "err", err)
		}
		if err := s.integ.Ensure(slug, nb.Layout, s.plat.Bus(slug)); err != nil {
			s.log.Error("integration load failed", "notebook", slug, "err", err)
		}
	}
	return s
}

// purgeTrash removes trash entries past retention in every notebook.
// Failures are logged; a stuck purge must never block boot.
func (s *Server) purgeTrash() {
	cutoff := time.Now().Add(-vault.TrashRetention)
	for _, nb := range s.reg.List() {
		tr := vault.OpenTrash(vault.Open(nb.Layout.Vault()), nb.Layout.Trash())
		n, err := tr.Purge(cutoff)
		if err != nil {
			s.log.Warn("trash purge failed", "notebook", nb.Manifest.Slug, "err", err)
		} else if n > 0 {
			s.log.Info("trash purged", "notebook", nb.Manifest.Slug, "entries", n)
		}
	}
}

func (s *Server) ListenAndServe() error { return s.http.ListenAndServe() }
func (s *Server) Shutdown(ctx context.Context) error {
	err := s.http.Shutdown(ctx)
	s.flushGit()
	if s.stopGC != nil {
		s.stopGC()
	}
	// Extensions first: their hooks and deliveries may still be
	// draining into the bus.
	if s.integ != nil {
		s.integ.Close()
	}
	if s.plug != nil {
		s.plug.Close()
	}
	if s.plat != nil {
		s.plat.Close()
	}
	return err
}

// markGitDirty records a mutation for coalesced autocommit. It never fails:
// the write already succeeded, git is recovery.
func (s *Server) markGitDirty(nb *notebooks.Notebook, op git.Op) {
	s.gitMu.Lock()
	defer s.gitMu.Unlock()
	f, ok := s.git[nb.Manifest.Slug]
	if !ok {
		f = git.New(nb.Layout.Vault(), s.log)
		s.git[nb.Manifest.Slug] = f
	}
	f.MarkDirty(op)
}

func (s *Server) flushGit() {
	s.gitMu.Lock()
	defer s.gitMu.Unlock()
	for slug, f := range s.git {
		// Close, not Flush: it sets closed so a failing commit cannot
		// re-arm the window timer and leak a goroutine (or write) past
		// shutdown. Shutdown is the only caller.
		if err := f.Close(); err != nil {
			s.log.Warn("git flush failed", "notebook", slug, "err", err)
		}
	}
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) ready(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":    "ready",
		"notebooks": len(s.reg.List()),
	})
}

func (s *Server) listNotebooks(w http.ResponseWriter, _ *http.Request) {
	type item struct {
		Slug    string `json:"slug"`
		Name    string `json:"name"`
		Profile string `json:"profile"`
	}
	out := make([]item, 0)
	for _, nb := range s.reg.List() {
		out = append(out, item{nb.Manifest.Slug, nb.Manifest.Name, nb.Manifest.Profile})
	}
	writeJSON(w, http.StatusOK, map[string]any{"notebooks": out})
}

func (s *Server) notebookRoot(w http.ResponseWriter, r *http.Request) {
	nb, ok := s.reg.Get(r.PathValue("slug"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such notebook"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"slug": nb.Manifest.Slug,
		"name": nb.Manifest.Name,
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "private")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
