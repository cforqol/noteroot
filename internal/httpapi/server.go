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
	"noteroot/internal/notebooks"
	"noteroot/internal/vault"
	"noteroot/web"
)

type Server struct {
	cfg  config.Config
	log  *slog.Logger
	reg  *notebooks.Registry
	http *http.Server

	gitMu sync.Mutex
	git   map[string]*git.Flusher

	stopGC func()
}

func New(cfg config.Config, log *slog.Logger, reg *notebooks.Registry) *Server {
	s := &Server{cfg: cfg, log: log, reg: reg}

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
	mux.Handle("GET /n/{slug}/resources", auth(s.listResources))
	mux.Handle("GET /n/{slug}/resources/stats", auth(s.resourceStats))
	mux.Handle("GET /n/{slug}/resources/{sha}", auth(s.getResource))
	mux.Handle("GET /n/{slug}/resources/{sha}/thumb", auth(s.thumbResource))
	mux.Handle("GET /n/{slug}/resources/{sha}/poster", auth(s.posterResource))
	mux.Handle("GET /n/{slug}/resources/{sha}/presign-read", auth(s.presignRead))
	mux.Handle("DELETE /n/{slug}/resources/{sha}", mutating(s.deleteResource))

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
		if err := f.Flush(); err != nil {
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
