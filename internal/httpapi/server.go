package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"noteroot/internal/config"
	"noteroot/internal/notebooks"
)

type Server struct {
	cfg  config.Config
	log  *slog.Logger
	reg  *notebooks.Registry
	http *http.Server
}

func New(cfg config.Config, log *slog.Logger, reg *notebooks.Registry) *Server {
	s := &Server{cfg: cfg, log: log, reg: reg}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /readyz", s.ready)
	mux.Handle("GET /api/v1/notebooks", Chain(http.HandlerFunc(s.listNotebooks), Auth(cfg.AuthMode)))
	mux.Handle("GET /n/{slug}/", Chain(http.HandlerFunc(s.notebookRoot), Auth(cfg.AuthMode)))

	h := Chain(mux, RequestID, Recover(log), Log(log))
	s.http = &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	return s
}

func (s *Server) ListenAndServe() error              { return s.http.ListenAndServe() }
func (s *Server) Shutdown(ctx context.Context) error { return s.http.Shutdown(ctx) }

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
		"note": "read path lands in stage 1",
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "private")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
