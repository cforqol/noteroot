package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"html"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"noteroot/internal/frontmatter"
	"noteroot/internal/git"
	"noteroot/internal/index"
	"noteroot/internal/markdown"
	"noteroot/internal/state"
	"noteroot/internal/vault"
)

func (s *Server) vaultFor(slug string) (*vault.Vault, bool) {
	nb, ok := s.reg.Get(slug)
	if !ok {
		return nil, false
	}
	return vault.Open(nb.Layout.Vault()), true
}

// mutableCacheControl marks API responses as revalidate-before-reuse.
// Plain "private" plus a Last-Modified header lets browsers apply
// heuristic freshness (10% of the note's age): a fetch right after a
// write could get the pre-write note and its sha from the cache, and the
// next save then failed with a spurious 412.
const mutableCacheControl = "private, no-cache"

// entityHeaders exposes the file identity editors base their writes on:
// ETag is the sha256 of the exact bytes served.
func entityHeaders(w http.ResponseWriter, src []byte, modTime time.Time) {
	w.Header().Set("ETag", `"`+etagOf(src)+`"`)
	if !modTime.IsZero() {
		w.Header().Set("Last-Modified", modTime.UTC().Format(http.TimeFormat))
	}
}

func etagOf(src []byte) string {
	sum := sha256.Sum256(src)
	return hex.EncodeToString(sum[:])
}

// renderNote is a seam so tests can force a render failure. resolve is
// nil when no link data is available; wikilinks then render as visible
// dead spans, never as literal [[brackets]] and never silently dropped.
var renderNote = func(src []byte, resolve func(string) (string, bool)) ([]byte, error) {
	return markdown.New().RenderWithResolver(src, resolve)
}

func (s *Server) tree(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	v, ok := s.vaultFor(slug)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such notebook"})
		return
	}
	t, err := v.Tree()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "walk failed"})
		return
	}
	s.markLockedTree(r, slug, t)
	writeJSON(w, http.StatusOK, t)
}

// markLockedTree flags nodes hidden behind content locks: a node whose
// ancestors include an unopened lock is marked Locked, and a locked
// folder's children are dropped so the tree does not reveal what is
// inside. Note existence (the node itself) stays visible.
func (s *Server) markLockedTree(r *http.Request, slug string, root *vault.Node) {
	ls, ok := s.openLockState(r, slug)
	if !ok {
		return
	}
	defer ls.close()
	// A notebook-wide lock hides the whole tree: keep the root so the
	// client knows the notebook exists and is locked, but reveal nothing.
	if blocked, err := lockBlocked(ls, r, ""); err == nil && blocked {
		root.Locked = true
		root.Children = nil
		return
	}
	var walk func(n *vault.Node)
	walk = func(n *vault.Node) {
		keep := n.Children[:0]
		for _, c := range n.Children {
			childPath := c.Path
			if c.IsDir {
				childPath += "/"
			}
			locked, err := lockBlocked(ls, r, childPath)
			if err != nil {
				keep = append(keep, c)
				continue
			}
			if locked {
				c.Locked = true
				c.Children = nil
				keep = append(keep, c)
				continue
			}
			if c.IsDir {
				walk(c)
			}
			keep = append(keep, c)
		}
		n.Children = keep
	}
	walk(root)
}

// lockBlocked reports whether target is behind an unopened lock.
func lockBlocked(ls *lockState, r *http.Request, target string) (bool, error) {
	allowed, err := ls.allows(r, target)
	if err != nil {
		return false, err
	}
	return !allowed, nil
}

func (s *Server) note(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	v, ok := s.vaultFor(slug)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such notebook"})
		return
	}
	rel := r.URL.Query().Get("path")
	if !s.lockGuard(w, r, slug, rel) {
		return
	}

	src, err := v.Read(rel)
	if err != nil {
		writeJSON(w, statusFor(err), map[string]string{"error": publicError(err)})
		return
	}
	_, modTime, _ := v.Stat(rel)
	entityHeaders(w, src, modTime)

	doc := frontmatter.Parse(src)
	resp := map[string]any{
		"path":  rel,
		"id":    doc.ID,
		"title": doc.Title,
		"tags":  frontmatter.Strings(doc.Meta, "tags"),
		"sha":   etagOf(src),
	}
	if doc.Err != nil {
		resp["frontmatterError"] = doc.Err.Error()
	}

	// Refresh the index (mtime-cheap, same as search) and read the link
	// rows behind the panels. The renderer resolves wikilinks against
	// those same rows, so the HTML and the outgoing panel can never
	// disagree (design §7). Index trouble degrades to no panels and
	// dead-span wikilinks — it never blocks opening the note.
	var resolve func(string) (string, bool)
	if nb, ok := s.reg.Get(slug); ok {
		idx, _, oerr := index.Open(r.Context(), nb.Layout.IndexDB(), nb.Layout.StateDB())
		if oerr != nil {
			s.log.Warn("index open failed", "notebook", slug, "err", oerr)
		} else {
			if _, uerr := idx.Update(r.Context(), v); uerr != nil {
				s.log.Warn("index update failed", "notebook", slug, "err", uerr)
			}
			if back, berr := idx.Backlinks(r.Context(), rel); berr == nil {
				resp["backlinks"] = back
			}
			if out, oerr := idx.Outgoing(r.Context(), rel); oerr == nil {
				resp["outgoing"] = out
				resolve = outgoingResolver(slug, out)
			}
			_ = idx.Close()
		}
	}

	// A render failure must never prevent the note from opening.
	if out, rerr := renderNote(doc.Body, resolve); rerr == nil {
		out = s.remapResourceLinks(out, slug)
		// After sanitization: turn links to uploaded video/audio into
		// players (no-op for notes without media links).
		if mimes := s.mediaMimeMap(r.Context(), slug, out); len(mimes) > 0 {
			out = embedMedia(out, slug, mimes)
		}
		resp["html"] = string(out)
	} else {
		resp["html"] = "<pre>" + html.EscapeString(string(doc.Body)) + "</pre>"
		resp["renderError"] = rerr.Error()
	}
	writeJSON(w, http.StatusOK, resp)
}

// outgoingResolver maps wikilink targets to note hrefs using the same
// links rows that back the outgoing panel. Unresolved targets report
// ok=false so the renderer marks them dead instead of guessing.
func outgoingResolver(slug string, links []index.Link) func(string) (string, bool) {
	byTarget := make(map[string]string, len(links))
	for _, l := range links {
		if l.Path != "" {
			byTarget[l.Target] = l.Path
		}
	}
	return func(target string) (string, bool) {
		p, ok := byTarget[target]
		if !ok {
			return "", false
		}
		return "/n/" + slug + "/note?path=" + url.QueryEscape(p), true
	}
}

func (s *Server) raw(w http.ResponseWriter, r *http.Request) {
	v, ok := s.vaultFor(r.PathValue("slug"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such notebook"})
		return
	}
	slug := r.PathValue("slug")
	rel := r.URL.Query().Get("path")
	if !s.lockGuard(w, r, slug, rel) {
		return
	}
	src, err := v.Read(rel)
	if err != nil {
		writeJSON(w, statusFor(err), map[string]string{"error": publicError(err)})
		return
	}
	_, modTime, _ := v.Stat(rel)
	entityHeaders(w, src, modTime)
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Cache-Control", mutableCacheControl)
	_, _ = w.Write(src)
}

func statusFor(err error) int {
	switch {
	case errors.Is(err, vault.ErrNotFound), errors.Is(err, vault.ErrNotFile):
		return http.StatusNotFound
	case errors.Is(err, vault.ErrEscapes):
		return http.StatusForbidden
	case errors.Is(err, vault.ErrTooLarge):
		return http.StatusRequestEntityTooLarge
	default:
		return http.StatusBadRequest
	}
}

// publicError renders err for the client. Sentinel errors carry stable,
// path-free messages; everything else (OS errors with absolute paths,
// wrapped Rel validations echoing segments) is replaced with a generic
// string so filesystem layout never leaks. Detail goes to the log.
func publicError(err error) string {
	for _, sentinel := range []error{
		vault.ErrNotFound, vault.ErrNotFile, vault.ErrEscapes,
		vault.ErrTooLarge, vault.ErrConflict, vault.ErrOccupied,
		state.ErrLocked, state.ErrNoLock,
	} {
		if errors.Is(err, sentinel) {
			return sentinel.Error()
		}
	}
	switch statusFor(err) {
	case http.StatusNotFound:
		return "not found"
	case http.StatusForbidden:
		return "forbidden"
	case http.StatusRequestEntityTooLarge:
		return "too large"
	default:
		return "request failed"
	}
}

// --- Note History & Diff ---

// historyCommit represents a single commit in the note's history.
type historyCommit struct {
	Hash      string `json:"hash"`
	ShortHash string `json:"shortHash"`
	Date      int64  `json:"date"` // Unix timestamp
	Author    string `json:"author"`
	Message   string `json:"message"`
	IsMerge   bool   `json:"isMerge"`
}

// noteHistory handles GET /n/{slug}/note/history?path=...&limit=50&before=<hash>
func (s *Server) noteHistory(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	nb, ok := s.reg.Get(slug)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such notebook"})
		return
	}
	path := r.URL.Query().Get("path")
	if path == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing path"})
		return
	}
	if !s.lockGuard(w, r, slug, path) {
		return
	}

	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 && parsed <= 200 {
			limit = parsed
		}
	}
	before := r.URL.Query().Get("before")
	if before != "" && !isGitHash(before) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid before hash"})
		return
	}

	commits, hasMore, err := git.LogFile(nb.Layout.Vault(), path, limit, before)
	if err != nil {
		s.log.Warn("note history failed", "notebook", slug, "path", path, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "history unavailable"})
		return
	}

	out := make([]historyCommit, len(commits))
	for i, c := range commits {
		out[i] = historyCommit{
			Hash:      c.Hash,
			ShortHash: c.ShortHash,
			Date:      c.Date.Unix(),
			Author:    c.Author,
			Message:   c.Message,
			IsMerge:   c.IsMerge,
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"commits": out, "hasMore": hasMore})
}

// noteDiff handles GET /n/{slug}/note/diff?path=...&from=<hash>&to=<hash>
func (s *Server) noteDiff(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	nb, ok := s.reg.Get(slug)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such notebook"})
		return
	}
	path := r.URL.Query().Get("path")
	if path == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing path"})
		return
	}
	from := r.URL.Query().Get("from")
	to := r.URL.Query().Get("to")
	if from == "" || to == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing from or to hash"})
		return
	}
	if !isGitHash(from) || !isGitHash(to) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid hash"})
		return
	}
	if !s.lockGuard(w, r, slug, path) {
		return
	}

	diff, err := git.DiffFile(nb.Layout.Vault(), path, from, to)
	if err != nil {
		s.log.Warn("note diff failed", "notebook", slug, "path", path, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "diff unavailable"})
		return
	}

	fromInfo := map[string]any{"hash": from}
	toInfo := map[string]any{"hash": to}
	if c, cerr := git.GetCommit(nb.Layout.Vault(), from); cerr == nil && c != nil {
		fromInfo["date"] = c.Date.Unix()
		fromInfo["message"] = c.Message
	}
	if c, cerr := git.GetCommit(nb.Layout.Vault(), to); cerr == nil && c != nil {
		toInfo["date"] = c.Date.Unix()
		toInfo["message"] = c.Message
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"diff": diff,
		"from": fromInfo,
		"to":   toInfo,
	})
}

// isGitHash reports whether s is a valid full-length git commit hash
// (40 lowercase hex chars for SHA-1, 64 for SHA-256). Accepting only
// full hashes prevents flag injection when the value is passed directly
// as a git argument before the end-of-options separator.
func isGitHash(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}
