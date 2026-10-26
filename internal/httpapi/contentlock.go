package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"noteroot/internal/lock"
	"noteroot/internal/notebooks"
	"noteroot/internal/state"
	"noteroot/internal/vault"
)

// Content locks gate reading through the app. They are advisory: the
// note stays plaintext on disk, in git, in the search index and in
// backups, so a lock hides content from a user of the UI — it does not
// keep anyone with filesystem access out. This is deliberately separate
// from the edit locks in lock.go, which only coordinate concurrent
// editors.
//
// A lock is stored against a path: "" locks the whole notebook, "dir/"
// locks a folder, "a/b.md" locks one note. A lock covers its path and
// every descendant. To read an item, the request must hold a session for
// every lock on an ancestor of that item; sessions ride a per-notebook
// cookie.

// lockState bundles the two state-DB reads a request needs so a handler
// never opens the database twice.
type lockState struct {
	db       *state.DB
	sessions []string
}

func (s *Server) openLockState(r *http.Request, slug string) (*lockState, bool) {
	nb, ok := s.reg.Get(slug)
	if !ok {
		return nil, false
	}
	db, _, err := state.Open(r.Context(), nb.Layout.StateDB())
	if err != nil {
		return nil, false
	}
	tokens := lock.ParseTokens(cookieValue(r, lock.CookieName(slug)))
	paths, err := db.SessionPaths(r.Context(), tokens)
	if err != nil {
		_ = db.Close()
		return nil, false
	}
	return &lockState{db: db, sessions: paths}, true
}

func (ls *lockState) close() {
	if ls != nil && ls.db != nil {
		_ = ls.db.Close()
	}
}

// allows reports whether the current sessions unlock target, considering
// every lock on an ancestor path.
func (ls *lockState) allows(r *http.Request, target string) (bool, error) {
	locks, err := ls.db.LocksFor(r.Context(), target)
	if err != nil {
		return false, err
	}
	for _, c := range locks {
		if !lock.Covers(ls.sessions, c.Path) {
			return false, nil
		}
	}
	return true, nil
}

// lockGuard is the one enforcement point for request handlers. It opens
// and closes the state database itself and reports whether the request
// may proceed; when it may not, the response has already been written
// (404, 423 or 500). It is a no-op when the notebook holds no locks.
func (s *Server) lockGuard(w http.ResponseWriter, r *http.Request, slug, target string) bool {
	if _, ok := s.reg.Get(slug); !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such notebook"})
		return false
	}
	ls, ok := s.openLockState(r, slug)
	if !ok {
		// Locks are advisory and stored in the same durable database the
		// rest of the app treats as recoverable. If it cannot be read we
		// cannot know whether a lock exists, and wedging every note behind
		// a broken database is worse than briefly failing open — the lock
		// is a UI gate, not secrecy.
		s.log.Warn("lock check skipped: state unavailable", "notebook", slug)
		return true
	}
	defer ls.close()
	allowed, err := ls.allows(r, target)
	if err != nil {
		s.log.Warn("lock check skipped: read failed", "notebook", slug, "err", err)
		return true
	}
	if !allowed {
		writeLocked(w, target)
		return false
	}
	return true
}

// writeLocked answers 423 Locked so the client can prompt for the path
// that must be unlocked.
func writeLocked(w http.ResponseWriter, target string) {
	writeJSON(w, http.StatusLocked, map[string]any{
		"error": "locked",
		"path":  target,
	})
}

// setContentLock handles PUT /n/{slug}/contentlock.
//
// Body: {"path": "", "kind": "notebook|folder|note", "password": "..."}
//
// Setting a lock while the target is already locked requires the current
// password, so a locked item cannot be re-keyed by someone who is not
// allowed to see it. Changing a password is just setting a new one.
func (s *Server) setContentLock(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	nb, ok := s.reg.Get(slug)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such notebook"})
		return
	}
	var req struct {
		Path     string `json:"path"`
		Kind     string `json:"kind"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad JSON: " + err.Error()})
		return
	}
	kind, ok := normalizeKind(req.Kind, req.Path)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad kind"})
		return
	}
	rel, err := lockPath(kind, req.Path)
	if err != nil {
		writeJSON(w, statusFor(err), map[string]string{"error": publicError(err)})
		return
	}
	if len(req.Password) < lock.MinPasswordLen {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "password too short"})
		return
	}
	// The target must exist so a typo cannot silently lock nothing.
	if !lockTargetExists(nb, kind, rel) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such path"})
		return
	}

	ls, ok := s.openLockState(r, slug)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "state unavailable"})
		return
	}
	defer ls.close()
	if allowed, err := ls.allows(r, rel); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "lock check failed"})
		return
	} else if !allowed {
		writeJSON(w, http.StatusLocked, map[string]any{"error": "locked", "path": rel})
		return
	}

	hash, salt, err := lock.Hash(req.Password)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "password too short"})
		return
	}
	if err := ls.db.SetLockConfig(r.Context(), rel, state.PathKind(kind), hash, salt); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "lock failed"})
		return
	}
	// Setting or changing a password revokes the setter's own sessions on
	// that exact path, so a lock takes effect immediately and an old
	// password cannot keep a browser unlocked.
	tokens := lock.ParseTokens(cookieValue(r, lock.CookieName(slug)))
	remaining := make([]string, 0, len(tokens))
	for _, tok := range tokens {
		paths, err := ls.db.SessionPaths(r.Context(), []string{tok})
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "lock failed"})
			return
		}
		if len(paths) > 0 && paths[0] == rel {
			_ = ls.db.DeleteSession(r.Context(), tok)
			continue
		}
		remaining = append(remaining, tok)
	}
	s.setLockCookie(w, slug, lock.JoinTokens(remaining), time.Now().Add(-time.Hour))
	writeJSON(w, http.StatusOK, map[string]any{"path": rel, "kind": kind, "locked": true})
}

// deleteContentLock handles DELETE /n/{slug}/contentlock?path= — removing
// a lock requires that it is currently unlocked.
func (s *Server) deleteContentLock(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	rel := r.URL.Query().Get("path")
	kind := r.URL.Query().Get("kind")
	if _, ok := normalizeKind(kind, rel); !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad kind"})
		return
	}
	k, _ := normalizeKind(kind, rel)
	norm, err := lockPath(k, rel)
	if err != nil {
		writeJSON(w, statusFor(err), map[string]string{"error": publicError(err)})
		return
	}
	if !s.lockGuard(w, r, slug, norm) {
		return
	}
	ls, ok := s.openLockState(r, slug)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "state unavailable"})
		return
	}
	defer ls.close()
	if err := ls.db.DeleteLockConfig(r.Context(), norm); err != nil {
		if errors.Is(err, state.ErrNoLockConfig) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "no lock on that path"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "unlock failed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": norm, "locked": false})
}

// unlockContentLock handles POST /n/{slug}/contentlock/unlock.
//
// Body: {"path": "", "password": "..."}
//
// On success it appends a session cookie covering that path and returns
// the locks now open.
func (s *Server) unlockContentLock(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if _, ok := s.reg.Get(slug); !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such notebook"})
		return
	}
	var req struct {
		Path     string `json:"path"`
		Kind     string `json:"kind"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad JSON: " + err.Error()})
		return
	}
	kind, _ := normalizeKind(req.Kind, req.Path)
	norm, err := lockPath(kind, req.Path)
	if err != nil {
		writeJSON(w, statusFor(err), map[string]string{"error": publicError(err)})
		return
	}

	ls, ok := s.openLockState(r, slug)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "state unavailable"})
		return
	}
	defer ls.close()

	// Verify against the locks that cover the requested path — any of
	// them may be the one the user meant. The most specific lock is the
	// natural target, but matching any lock on the path keeps the flow
	// forgiving when a folder and its parent both have passwords.
	locks, err := ls.db.LocksFor(r.Context(), norm)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "lock check failed"})
		return
	}
	if len(locks) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"path": norm, "unlocked": true})
		return
	}
	sort.SliceStable(locks, func(i, j int) bool { return len(locks[i].Path) > len(locks[j].Path) })
	var matched *state.LockConfig
	for i := range locks {
		if lock.Verify(req.Password, locks[i].Hash, locks[i].Salt) {
			matched = &locks[i]
			break
		}
	}
	if matched == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "wrong password"})
		return
	}
	if lock.Covers(ls.sessions, matched.Path) {
		writeJSON(w, http.StatusOK, map[string]any{"path": matched.Path, "unlocked": true})
		return
	}
	token, err := lock.NewToken()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "unlock failed"})
		return
	}
	expires, err := ls.db.CreateSession(r.Context(), matched.Path, token)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "unlock failed"})
		return
	}
	tokens := lock.AddToken(lock.ParseTokens(cookieValue(r, lock.CookieName(slug))), token)
	s.setLockCookie(w, slug, lock.JoinTokens(tokens), expires)
	writeJSON(w, http.StatusOK, map[string]any{"path": matched.Path, "unlocked": true})
}

// relockContentLock handles POST /n/{slug}/contentlock/lock. Body:
// {"path": "..."} — revokes sessions that cover the path, dropping the
// user back to a locked view.
func (s *Server) relockContentLock(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if _, ok := s.reg.Get(slug); !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such notebook"})
		return
	}
	var req struct {
		Path string `json:"path"`
		Kind string `json:"kind"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad JSON: " + err.Error()})
		return
	}
	kind, _ := normalizeKind(req.Kind, req.Path)
	norm, err := lockPath(kind, req.Path)
	if err != nil {
		writeJSON(w, statusFor(err), map[string]string{"error": publicError(err)})
		return
	}
	ls, ok := s.openLockState(r, slug)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "state unavailable"})
		return
	}
	defer ls.close()

	tokens := lock.ParseTokens(cookieValue(r, lock.CookieName(slug)))
	remaining := make([]string, 0, len(tokens))
	for _, tok := range tokens {
		paths, err := ls.db.SessionPaths(r.Context(), []string{tok})
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "lock check failed"})
			return
		}
		drop := false
		for _, p := range paths {
			// A session on the target or above it is dropped: relocking a
			// folder relocks everything under it.
			if p == norm || lock.CoverPath(p, norm) {
				drop = true
				break
			}
		}
		if drop {
			_ = ls.db.DeleteSession(r.Context(), tok)
			continue
		}
		remaining = append(remaining, tok)
	}
	s.setLockCookie(w, slug, lock.JoinTokens(remaining), time.Now().Add(-time.Hour))
	writeJSON(w, http.StatusOK, map[string]any{"path": norm, "locked": true})
}

// contentLockStatus handles GET /n/{slug}/contentlock?path=&kind= — the
// client asks what is locked and whether the current sessions cover it.
func (s *Server) contentLockStatus(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if _, ok := s.reg.Get(slug); !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such notebook"})
		return
	}
	kind, _ := normalizeKind(r.URL.Query().Get("kind"), r.URL.Query().Get("path"))
	rel, err := lockPath(kind, r.URL.Query().Get("path"))
	if err != nil {
		writeJSON(w, statusFor(err), map[string]string{"error": publicError(err)})
		return
	}
	ls, ok := s.openLockState(r, slug)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "state unavailable"})
		return
	}
	defer ls.close()

	locks, err := ls.db.LocksFor(r.Context(), rel)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "lock check failed"})
		return
	}
	locked := make([]string, 0, len(locks))
	allOpen := true
	for _, c := range locks {
		locked = append(locked, c.Path)
		if !lock.Covers(ls.sessions, c.Path) {
			allOpen = false
		}
	}
	// Exact lock on this path, for the UI to show "locked/locked by parent".
	exact, hasExact, _ := ls.db.GetLockConfig(r.Context(), rel)
	resp := map[string]any{
		"path":     rel,
		"locked":   len(locks) > 0,
		"unlocked": allOpen,
		"locks":    locked,
		"params":   lock.FormatParams(),
	}
	if hasExact {
		resp["exact"] = true
		resp["kind"] = string(exact.Kind)
	}
	writeJSON(w, http.StatusOK, resp)
}

// normalizeKind resolves a kind string to one of notebook/folder/note,
// inferring "note" vs "folder" from the path when kind is empty.
func normalizeKind(kind, rel string) (string, bool) {
	switch kind {
	case "notebook", "folder", "note":
		return kind, true
	case "":
		return inferKind(rel), true
	default:
		return "", false
	}
}

func inferKind(rel string) string {
	rel = strings.TrimSpace(rel)
	if rel == "" {
		return "notebook"
	}
	if strings.HasSuffix(rel, "/") || !strings.Contains(pathBase(rel), ".") {
		return "folder"
	}
	return "note"
}

func pathBase(p string) string {
	p = strings.TrimSuffix(p, "/")
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

// lockPath normalizes a lock path the way it is stored.
func lockPath(kind, rel string) (string, error) {
	if kind == "notebook" {
		return "", nil
	}
	clean, err := vault.Rel(strings.TrimSuffix(rel, "/"))
	if err != nil {
		return "", err
	}
	return lock.NormalizeRel(kind, clean), nil
}

func lockTargetExists(nb *notebooks.Notebook, kind, rel string) bool {
	v := vault.Open(nb.Layout.Vault())
	switch kind {
	case "notebook":
		return true
	case "folder":
		isDir, err := v.IsDir(strings.TrimSuffix(rel, "/"))
		return err == nil && isDir
	default:
		_, err := v.Read(rel)
		return err == nil
	}
}

// cookieValue reads a cookie, tolerating its absence.
func cookieValue(r *http.Request, name string) string {
	c, err := r.Cookie(name)
	if err != nil {
		return ""
	}
	return c.Value
}

// setLockCookie writes the per-notebook lock cookie. It is HttpOnly (the
// UI never needs to read it), SameSite=Strict (lock state is not a
// cross-site concern) and Secure when the public origin is https. An
// empty value or a past expiry clears it.
func (s *Server) setLockCookie(w http.ResponseWriter, slug, value string, expires time.Time) {
	secure := strings.HasPrefix(s.cfg.PublicOrigin, "https://")
	http.SetCookie(w, &http.Cookie{
		Name:     lock.CookieName(slug),
		Value:    value,
		Path:     "/",
		Expires:  expires,
		MaxAge:   int(time.Until(expires).Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   secure,
	})
}


