package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"noteroot/internal/config"
	"noteroot/internal/git"
	"noteroot/internal/index"
	"noteroot/internal/integrations"
	rlog "noteroot/internal/log"
	"noteroot/internal/notebooks"
	"noteroot/internal/platform"
	"noteroot/internal/plugins"
	"noteroot/internal/vault"
	"noteroot/internal/version"
	"noteroot/web"
)

type Server struct {
	cfg        config.Config
	log        *slog.Logger
	reg        *notebooks.Registry
	plat       *platform.Manager
	plug       *plugins.Manager
	integ      *integrations.Manager
	breakers   *notebooks.Breakers
	recentLogs *rlog.Recent
	http       *http.Server

	// transport is shared by every outbound request this server makes to
	// another service (Karakeep, Slink, S3-compatible storage): nil
	// unless NOTEROOT_TLS_CA_FILE/NOTEROOT_TLS_CLIENT_CERT_FILE are set,
	// in which case http.Client{Transport: s.transport} trusts the extra
	// CA and/or presents the client certificate. Built once here — cfg
	// already validated the same call in config.Load(), so an error on
	// this second pass would mean the files changed from under the
	// process; fall back to the default transport rather than panic.
	transport http.RoundTripper

	gitMu sync.Mutex
	git   map[string]*git.Flusher

	// uploadMu serializes the quota-check + journal-begin reservation
	// window across every upload path (server-side, presign). Held only
	// for those two DB operations — never across blob I/O.
	uploadMu sync.Mutex

	stopGC     func()
	stopBackup func()
}

func New(cfg config.Config, log *slog.Logger, reg *notebooks.Registry) *Server {
	// Tee every Warn-or-above record into a bounded in-memory ring, on
	// top of (never instead of) wherever log already writes: the
	// diagnostics bundle (/n/{slug}/diagnostics) reads it back as
	// "recent error logs" without needing a separate log-shipping
	// setup. Wrapping here, before any manager is constructed, means
	// every logger derived from this one (s.plug, s.integ, s.plat, and
	// every s.log.Warn/Error call below) is captured too.
	recentLogs := rlog.NewRecent(rlog.DefaultRecentCapacity)
	log = slog.New(rlog.Tee(log.Handler(), recentLogs))

	s := &Server{cfg: cfg, log: log, reg: reg, recentLogs: recentLogs, plat: platform.NewManager(log)}
	if tr, err := cfg.BuildTransport(); err != nil {
		log.Error("custom TLS transport failed, falling back to default", "err", err)
	} else {
		s.transport = tr
	}
	s.plug = plugins.NewManager(log, func(slug string) plugins.NoteStore {
		return &pluginNoteStore{s: s, slug: slug}
	})
	s.integ = integrations.NewManager(log, func(slug string) plugins.NoteStore {
		return &pluginNoteStore{s: s, slug: slug}
	})
	breakerCooldown, err := time.ParseDuration(cfg.BreakerCooldown)
	if err != nil {
		log.Warn("invalid breaker cooldown, using default", "value", cfg.BreakerCooldown, "default", notebooks.DefaultBreakerCooldown)
		breakerCooldown = notebooks.DefaultBreakerCooldown
	}
	s.breakers = notebooks.NewBreakers(cfg.BreakerThreshold, breakerCooldown)

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
	// nbAuth and nbMutating are auth/mutating plus the per-notebook
	// circuit breaker: used for every route that actually touches a
	// notebook's vault/db/plugins. Notebook *management* — create,
	// rename, delete — stays on plain auth/mutating below, so a broken
	// notebook whose breaker has tripped can still be fixed or removed.
	nbAuth := func(h http.HandlerFunc) http.Handler {
		return Chain(h, Auth(cfg.AuthMode), NotebookBreaker(s.breakers))
	}
	nbMutating := func(h http.HandlerFunc) http.Handler {
		return Chain(h, Auth(cfg.AuthMode), CSRF(cfg.PublicOrigin), NotebookBreaker(s.breakers))
	}
	// Listing notebooks is unauthenticated: the wizard shown on first visit
	// needs it to detect an empty install, and /readyz already leaks the
	// count unauthed. Actual note content still requires auth.
	mux.HandleFunc("GET /api/v1/notebooks", s.listNotebooks)
	mux.Handle("POST /api/v1/notebooks", mutating(s.createNotebook))
	mux.Handle("PATCH /api/v1/notebooks/{slug}", mutating(s.renameNotebook))
	mux.Handle("DELETE /api/v1/notebooks/{slug}", mutating(s.deleteNotebook))
	mux.Handle("GET /n/{slug}/", nbAuth(s.notebookRoot))
	mux.Handle("GET /n/{slug}/tree", nbAuth(s.tree))
	mux.Handle("GET /n/{slug}/note", nbAuth(s.note))
	mux.Handle("GET /n/{slug}/raw", nbAuth(s.raw))
	mux.Handle("GET /n/{slug}/note/history", nbAuth(s.noteHistory))
	mux.Handle("GET /n/{slug}/note/diff", nbAuth(s.noteDiff))
	mux.Handle("GET /n/{slug}/tags", nbAuth(s.listTags))
	mux.Handle("GET /n/{slug}/search", nbAuth(s.search))
	mux.Handle("GET /n/{slug}/trash", nbAuth(s.listTrash))
	mux.Handle("GET /n/{slug}/trash/note", nbAuth(s.trashedNote))
	mux.Handle("PUT /n/{slug}/note", nbMutating(s.putNote))
	mux.Handle("PATCH /n/{slug}/note", nbMutating(s.moveNote))
	mux.Handle("DELETE /n/{slug}/note", nbMutating(s.deleteNote))
	mux.Handle("POST /n/{slug}/folder", nbMutating(s.createFolder))
	mux.Handle("PATCH /n/{slug}/folder", nbMutating(s.moveFolder))
	mux.Handle("DELETE /n/{slug}/folder", nbMutating(s.deleteFolder))
	mux.Handle("GET /n/{slug}/folder-prefs", nbAuth(s.listFolderPrefs))
	mux.Handle("PUT /n/{slug}/folder-prefs", nbMutating(s.setFolderPref))
	mux.Handle("POST /n/{slug}/trash/restore", nbMutating(s.restoreTrash))
	mux.Handle("POST /n/{slug}/trash/restore-folder", nbMutating(s.restoreTrashFolder))
	mux.Handle("DELETE /n/{slug}/trash", nbMutating(s.deleteTrashEntry))
	mux.Handle("DELETE /n/{slug}/trash/folder", nbMutating(s.deleteTrashFolder))
	mux.Handle("PUT /n/{slug}/trash/settings", nbMutating(s.putTrashSettings))
	mux.Handle("POST /n/{slug}/trash/purge", nbMutating(s.purgeTrashNow))
	mux.Handle("PUT /n/{slug}/lock", nbMutating(s.putLock))
	mux.Handle("DELETE /n/{slug}/lock", nbMutating(s.deleteLock))

	// Content locks (advisory UI password gate; distinct from the edit
	// locks above). Status and unlock are reads/mutations on state only.
	mux.Handle("GET /n/{slug}/contentlock", nbAuth(s.contentLockStatus))
	mux.Handle("PUT /n/{slug}/contentlock", nbMutating(s.setContentLock))
	mux.Handle("DELETE /n/{slug}/contentlock", nbMutating(s.deleteContentLock))
	mux.Handle("POST /n/{slug}/contentlock/unlock", nbMutating(s.unlockContentLock))
	mux.Handle("POST /n/{slug}/contentlock/lock", nbMutating(s.relockContentLock))

	// Resources (stage 5) — uploads allow multipart via CSRFAllowMultipart.
	mux.Handle("POST /n/{slug}/resources", Chain(http.HandlerFunc(s.uploadResource), Auth(cfg.AuthMode), CSRFAllowMultipart(cfg.PublicOrigin), NotebookBreaker(s.breakers)))
	mux.Handle("POST /n/{slug}/resources/presign", nbMutating(s.presignUpload))
	mux.Handle("POST /n/{slug}/resources/confirm", nbMutating(s.confirmUpload))
	mux.Handle("POST /n/{slug}/resources/gc", nbMutating(s.gcResources))

	// Diagnostics bundle (#49): version info, DB stats, vault row
	// count, circuit breaker state, global quota snapshot, recent
	// error logs and a goroutine dump, zipped.
	mux.Handle("POST /n/{slug}/diagnostics", nbMutating(s.diagnostics))

	// Stage 8: backup, import/export.
	mux.Handle("POST /n/{slug}/backup", nbMutating(s.createBackup))
	mux.Handle("GET /n/{slug}/export", nbAuth(s.exportNotebook))
	mux.Handle("GET /n/{slug}/note/export", nbAuth(s.exportNoteHTML))
	mux.Handle("GET /n/{slug}/note/export/archive", nbAuth(s.exportNoteArchive))
	mux.Handle("GET /n/{slug}/export/html", nbAuth(s.exportNotebookHTML))
	mux.Handle("GET /n/{slug}/export/selection", nbAuth(s.exportSelection))
	mux.Handle("POST /n/{slug}/import", Chain(http.HandlerFunc(s.importNotebook), Auth(cfg.AuthMode), CSRFAllowMultipart(cfg.PublicOrigin), NotebookBreaker(s.breakers)))
	mux.Handle("POST /n/{slug}/note/import", Chain(http.HandlerFunc(s.importNoteArchive), Auth(cfg.AuthMode), CSRFAllowMultipart(cfg.PublicOrigin), NotebookBreaker(s.breakers)))

	// Resumable import/export jobs: large notebooks (many GB of
	// resources) transfer without holding one HTTP connection open for
	// the whole duration — export builds to a file and downloads with
	// Range support, import accepts the archive in offset-fenced chunks.
	mux.Handle("POST /n/{slug}/export/jobs", nbMutating(s.createExportJob))
	mux.Handle("GET /n/{slug}/export/jobs/{id}", nbAuth(s.exportJobStatus))
	mux.Handle("GET /n/{slug}/export/jobs/{id}/download", nbAuth(s.downloadExportJob))
	mux.Handle("DELETE /n/{slug}/export/jobs/{id}", nbMutating(s.deleteExportJob))
	mux.Handle("POST /n/{slug}/import/jobs", nbMutating(s.createImportJob))
	mux.Handle("PATCH /n/{slug}/import/jobs/{id}", nbMutating(s.appendImportJob))
	mux.Handle("GET /n/{slug}/import/jobs/{id}", nbAuth(s.importJobStatus))
	mux.Handle("POST /n/{slug}/import/jobs/{id}/complete", nbMutating(s.completeImportJob))
	mux.Handle("DELETE /n/{slug}/import/jobs/{id}", nbMutating(s.deleteImportJob))
	mux.Handle("GET /n/{slug}/resources", nbAuth(s.listResources))
	mux.Handle("GET /n/{slug}/resources/stats", nbAuth(s.resourceStats))
	mux.Handle("GET /n/{slug}/resources/{sha}", nbAuth(s.getResource))
	mux.Handle("GET /n/{slug}/resources/{sha}/thumb", nbAuth(s.thumbResource))
	mux.Handle("GET /n/{slug}/resources/{sha}/poster", nbAuth(s.posterResource))
	mux.Handle("GET /n/{slug}/resources/{sha}/presign-read", nbAuth(s.presignRead))
	mux.Handle("GET /n/{slug}/resources/{sha}/refs", nbAuth(s.listResourceRefs))
	mux.Handle("GET /n/{slug}/resources/{sha}/verify", nbAuth(s.verifyResource))
	mux.Handle("DELETE /n/{slug}/resources/{sha}", nbMutating(s.deleteResource))
	mux.Handle("GET /n/{slug}/integrations/karakeep/bookmarks", nbAuth(s.listKarakeepBookmarks))

	// Stage 9 phase 2: scoped API tokens (human-managed under the
	// forward-auth surface) and the token-authenticated machine API.
	mux.Handle("GET /n/{slug}/tokens", nbAuth(s.listTokens))
	mux.Handle("POST /n/{slug}/tokens", nbMutating(s.createToken))
	mux.Handle("DELETE /n/{slug}/tokens", nbMutating(s.revokeToken))

	// Stage 9 phase 3: plugin management and command dispatch.
	mux.Handle("GET /n/{slug}/plugins", nbAuth(s.listPlugins))
	mux.Handle("GET /n/{slug}/plugins/panels", nbAuth(s.pluginPanels))
	mux.Handle("POST /n/{slug}/plugins/reload", nbMutating(s.reloadPlugins))
	mux.Handle("POST /n/{slug}/plugins/{name}/commands/{command}", nbMutating(s.runPluginCommand))

	// Stage 9 phase 4: integration and hook management (human) plus
	// the inbound webhook endpoint (HMAC-authenticated machine).
	mux.Handle("GET /n/{slug}/integrations", nbAuth(s.listIntegrations))
	mux.Handle("GET /n/{slug}/hooks", nbAuth(s.listHooks))
	mux.Handle("POST /n/{slug}/integrations/reload", nbMutating(s.reloadIntegrations))
	mux.Handle("POST /n/{slug}/integrations/{name}/secret", nbMutating(s.setIntegrationSecret))
	mux.Handle("POST /n/{slug}/hooks/{name}/secret", nbMutating(s.setHookSecret))
	mux.Handle("POST /hooks/{slug}/{name}", NotebookBreaker(s.breakers)(http.HandlerFunc(s.handleWebhook)))

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
	mux.Handle("GET /api/v1/n/{slug}/trash/note", s.bearer("trash", "read", qname, s.trashedNote))
	mux.Handle("POST /api/v1/n/{slug}/trash/restore", s.bearer("trash", "write", qname, s.restoreTrash))
	mux.Handle("GET /api/v1/n/{slug}/resources", s.bearer("resources", "read", qnone, s.listResources))
	mux.Handle("GET /api/v1/n/{slug}/resources/stats", s.bearer("resources", "read", qnone, s.resourceStats))
	mux.Handle("GET /api/v1/n/{slug}/resources/{sha}", s.bearer("resources", "read", qsha, s.getResource))
	mux.Handle("GET /api/v1/n/{slug}/resources/{sha}/thumb", s.bearer("resources", "read", qsha, s.thumbResource))
	mux.Handle("GET /api/v1/n/{slug}/resources/{sha}/poster", s.bearer("resources", "read", qsha, s.posterResource))
	mux.Handle("GET /api/v1/n/{slug}/resources/{sha}/refs", s.bearer("resources", "read", qsha, s.listResourceRefs))
	mux.Handle("GET /api/v1/n/{slug}/resources/{sha}/verify", s.bearer("resources", "read", qsha, s.verifyResource))
	mux.Handle("DELETE /api/v1/n/{slug}/resources/{sha}", s.bearer("resources", "write", qsha, s.deleteResource))
	mux.Handle("GET /api/v1/n/{slug}/export", s.bearer("export", "read", qnone, s.exportNotebook))
	mux.Handle("GET /api/v1/n/{slug}/note/export/archive", s.bearer("export", "read", qpath, s.exportNoteArchive))
	mux.Handle("POST /api/v1/n/{slug}/export/jobs", s.bearer("export", "read", qnone, s.createExportJob))
	mux.Handle("GET /api/v1/n/{slug}/export/jobs/{id}", s.bearer("export", "read", qnone, s.exportJobStatus))
	mux.Handle("GET /api/v1/n/{slug}/export/jobs/{id}/download", s.bearer("export", "read", qnone, s.downloadExportJob))
	mux.Handle("DELETE /api/v1/n/{slug}/export/jobs/{id}", s.bearer("export", "read", qnone, s.deleteExportJob))
	mux.Handle("POST /api/v1/n/{slug}/backup", s.bearer("backup", "write", qnone, s.createBackup))

	// External integration proxies — no per-notebook scope, auth only.
	mux.Handle("GET /api/v1/integrations/status", auth(s.integrationStatus))
	mux.Handle("GET /api/v1/integrations/s3/test", auth(s.testS3))
	mux.Handle("GET /api/v1/integrations/slink/test", auth(s.testSlink))
	mux.Handle("GET /api/v1/integrations/karakeep/test", auth(s.testKarakeep))
	mux.Handle("POST /api/v1/integrations/karakeep/archive", mutating(s.archiveWithKarakeep))
	mux.Handle("POST /api/v1/integrations/slink/upload",
		Chain(http.HandlerFunc(s.uploadToSlink), Auth(cfg.AuthMode), CSRFAllowMultipart(cfg.PublicOrigin)))

	// Admin health page (#49): a per-notebook summary across every
	// notebook at once — breaker state, resource usage, global quota.
	// Not notebook-scoped, so it sits outside the per-notebook breaker.
	mux.Handle("GET /admin/health", auth(s.adminHealth))

	parseDuration := func(s, name string, fallback time.Duration) time.Duration {
		if s == "" {
			return fallback
		}
		d, err := time.ParseDuration(s)
		if err != nil {
			log.Warn("invalid timeout config, using default", "name", name, "value", s, "default", fallback)
			return fallback
		}
		return d
	}
	readTimeout := parseDuration(cfg.ReadTimeout, "ReadTimeout", 10*time.Second)
	writeTimeout := parseDuration(cfg.WriteTimeout, "WriteTimeout", 10*time.Second)
	idleTimeout := parseDuration(cfg.IdleTimeout, "IdleTimeout", 60*time.Second)

	// Build dynamic connect-src: add S3 and Slink endpoints so the browser
	// can send presigned PUTs to S3 and file uploads to Slink without the
	// static 'self' CSP blocking those cross-origin fetches.
	var extraConnect []string
	if o := connectOriginFor(cfg.S3Endpoint); o != "" {
		extraConnect = append(extraConnect, o)
	}
	if o := connectOriginFor(cfg.SlinkURL); o != "" {
		extraConnect = append(extraConnect, o)
	}
	secHdrs := SecurityHeadersFor(strings.Join(extraConnect, " "))
	h := Chain(mux, RequestID, Recover(log), Log(log), SharedArrayBuffer, secHdrs)
	s.http = &http.Server{
		Addr:           cfg.ListenAddr,
		Handler:        h,
		ReadTimeout:    readTimeout,
		WriteTimeout:   writeTimeout,
		IdleTimeout:    idleTimeout,
		MaxHeaderBytes: cfg.MaxHeaderBytes,
	}
	s.git = map[string]*git.Flusher{}
	s.purgeTrash()
	s.stopGC = s.startGC()
	s.stopBackup = s.startBackupScheduler()
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
// Failures are logged; a stuck purge must never block boot or a sweep.
func (s *Server) purgeTrash() {
	for _, nb := range s.reg.List() {
		s.purgeNotebookTrash(nb)
	}
}

// purgeNotebookTrash applies one notebook's retention and reports how
// many entries went. A notebook set to "never" is left alone, and a
// failure is logged rather than surfaced: losing a purge only costs
// disk, while failing the caller would cost the user their action.
func (s *Server) purgeNotebookTrash(nb *notebooks.Notebook) int {
	cutoff, ok := nb.Manifest.TrashCutoff(time.Now())
	if !ok {
		return 0
	}
	tr := vault.OpenTrash(vault.Open(nb.Layout.Vault()), nb.Layout.Trash())
	n, err := tr.Purge(cutoff)
	if err != nil {
		s.log.Warn("trash purge failed", "notebook", nb.Manifest.Slug, "err", err)
	} else if n > 0 {
		s.log.Info("trash purged", "notebook", nb.Manifest.Slug, "entries", n)
	}
	return n
}

func (s *Server) ListenAndServe() error { return s.http.ListenAndServe() }
func (s *Server) Shutdown(ctx context.Context) error {
	err := s.http.Shutdown(ctx)
	s.flushGit()
	if s.stopGC != nil {
		s.stopGC()
	}
	if s.stopBackup != nil {
		s.stopBackup()
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

// resolveWikilink finds the vault-relative path for a wikilink target.
// Returns (path, true) if found, ("", false) otherwise.
func (s *Server) resolveWikilink(nb *notebooks.Notebook, target string) (string, bool) {
	ctx := context.Background()
	idx, _, err := index.Open(ctx, nb.Layout.IndexDB(), nb.Layout.StateDB())
	if err != nil {
		return "", false
	}
	defer idx.Close()
	// Search the identity map for the target
	links, err := idx.Outgoing(ctx, "")
	if err != nil {
		return "", false
	}
	for _, link := range links {
		if strings.EqualFold(link.Target, target) && link.Path != "" {
			return link.Path, true
		}
	}
	return "", false
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": version.V, "timezone": serverTimezone()})
}

// serverTimezone reports the IANA zone name the server itself runs in, so
// the client's timezone preference can offer "server default" as a real,
// named option rather than a silent guess. TZ, when set, is already that
// name; otherwise time.Local resolves it from the container's zoneinfo
// (the image ships tzdata for exactly this). "Local" is what Go reports
// when it cannot name the zone at all (no TZ, no zoneinfo) — fall back to
// UTC rather than surface that internal placeholder to users.
func serverTimezone() string {
	if tz := os.Getenv("TZ"); tz != "" {
		return tz
	}
	if name := time.Local.String(); name != "" && name != "Local" {
		return name
	}
	return "UTC"
}

func (s *Server) ready(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":    "ready",
		"version":   version.V,
		"notebooks": len(s.reg.List()),
	})
}

func (s *Server) listNotebooks(w http.ResponseWriter, r *http.Request) {
	type item struct {
		Slug    string `json:"slug"`
		Name    string `json:"name"`
		Profile string `json:"profile"`
		Locked  bool   `json:"locked"`
	}
	out := make([]item, 0)
	for _, nb := range s.reg.List() {
		out = append(out, item{nb.Manifest.Slug, nb.Manifest.Name, nb.Manifest.Profile, s.notebookLocked(r, nb.Manifest.Slug)})
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
	w.Header().Set("Cache-Control", mutableCacheControl)
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
