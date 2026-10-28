package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"noteroot/internal/platform"
)

// Machine API: /api/v1 routes authenticate with a bearer token issued
// for this notebook, never with forward-auth. No CSRF middleware is
// applied by design — the Authorization header cannot be attached by
// another origin without a CORS grant this server never issues.

type tokenCreated struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	Token  string   `json:"token"`
	Scopes []string `json:"scopes"`
}

type tokenInfo struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Scopes     []string   `json:"scopes"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

// createToken issues a scoped API token. The plaintext secret appears
// once, in this response; only its SHA-256 hash is stored.
func (s *Server) createToken(w http.ResponseWriter, r *http.Request) {
	nb, ok := s.reg.Get(r.PathValue("slug"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown notebook"})
		return
	}
	var req struct {
		Name   string `json:"name"`
		Scopes string `json:"scopes"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || len(name) > 64 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name required (max 64 chars)"})
		return
	}
	grants, err := platform.ParseGrants(req.Scopes)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if len(grants) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "scopes required"})
		return
	}
	scopes := strings.Fields(platform.GrantsSpec(grants))

	var rnd [32]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
		return
	}
	secret := "nrk_" + base64.RawURLEncoding.EncodeToString(rnd[:])
	sum := sha256.Sum256([]byte(secret))

	db, _, err := platform.Open(r.Context(), nb.Layout.PlatformDB())
	if err != nil {
		s.log.Error("platform open failed", "notebook", nb.Manifest.Slug, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
		return
	}
	defer func() { _ = db.Close() }()

	// Live names are unique — the bearer actor is "token:"+name. Check
	// first for a clean 409; the partial unique index is the backstop
	// for a concurrent create.
	taken, err := db.TokenNameTaken(r.Context(), name)
	if err != nil {
		s.log.Error("token name check failed", "notebook", nb.Manifest.Slug, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
		return
	}
	if taken {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "token name already in use"})
		return
	}

	t, err := db.TokenCreate(r.Context(), name, hex.EncodeToString(sum[:]), scopes)
	if err != nil {
		if platform.IsNameConflict(err) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "token name already in use"})
			return
		}
		s.log.Error("token create failed", "notebook", nb.Manifest.Slug, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
		return
	}
	if err := db.AuditAppend(r.Context(), actorFrom(r), "token.create",
		nb.Manifest.Slug+"/tokens/"+t.ID, platform.GrantsSpec(grants)); err != nil {
		s.log.Warn("audit append failed", "notebook", nb.Manifest.Slug, "err", err)
	}
	writeJSON(w, http.StatusCreated, tokenCreated{
		ID: t.ID, Name: t.Name, Token: secret, Scopes: t.Scopes,
	})
}

// listTokens shows token metadata. Hashes never leave the server and
// the plaintext never existed server-side beyond create.
func (s *Server) listTokens(w http.ResponseWriter, r *http.Request) {
	nb, ok := s.reg.Get(r.PathValue("slug"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown notebook"})
		return
	}
	db, _, err := platform.Open(r.Context(), nb.Layout.PlatformDB())
	if err != nil {
		s.log.Error("platform open failed", "notebook", nb.Manifest.Slug, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
		return
	}
	defer func() { _ = db.Close() }()

	tokens, err := db.TokenList(r.Context())
	if err != nil {
		s.log.Error("token list failed", "notebook", nb.Manifest.Slug, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
		return
	}
	out := make([]tokenInfo, 0, len(tokens))
	for i := range tokens {
		info := tokenInfo{
			ID: tokens[i].ID, Name: tokens[i].Name, Scopes: tokens[i].Scopes,
			CreatedAt: tokens[i].CreatedAt,
		}
		if !tokens[i].LastUsedAt.IsZero() {
			t := tokens[i].LastUsedAt
			info.LastUsedAt = &t
		}
		if tokens[i].Revoked() {
			t := tokens[i].RevokedAt
			info.RevokedAt = &t
		}
		out = append(out, info)
	}
	writeJSON(w, http.StatusOK, out)
}

// revokeToken kills a token by id. Revoking an already-revoked token
// is a no-op 200; an unknown id is 404.
func (s *Server) revokeToken(w http.ResponseWriter, r *http.Request) {
	nb, ok := s.reg.Get(r.PathValue("slug"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown notebook"})
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id required"})
		return
	}
	db, _, err := platform.Open(r.Context(), nb.Layout.PlatformDB())
	if err != nil {
		s.log.Error("platform open failed", "notebook", nb.Manifest.Slug, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
		return
	}
	defer func() { _ = db.Close() }()

	fresh, err := db.TokenRevoke(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown token"})
		return
	}
	if fresh {
		if err := db.AuditAppend(r.Context(), actorFrom(r), "token.revoke",
			nb.Manifest.Slug+"/tokens/"+id, ""); err != nil {
			s.log.Warn("audit append failed", "notebook", nb.Manifest.Slug, "err", err)
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"revoked": true, "fresh": fresh})
}

// bearer authenticates /api/v1 routes: Authorization: Bearer nrk_...
// is looked up by secret hash in the notebook's platform.db, checked
// against the token's grants for this resource:action[:qualifier],
// and the request continues as actor "token:<name>" so audit and bus
// events attribute writes to the credential that made them.
func (s *Server) bearer(resource, action string, qf func(*http.Request) string, h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := bearerToken(r)
		if raw == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid token"})
			return
		}
		nb, ok := s.reg.Get(r.PathValue("slug"))
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown notebook"})
			return
		}
		db, _, err := platform.Open(r.Context(), nb.Layout.PlatformDB())
		if err != nil {
			s.log.Error("platform open failed", "notebook", nb.Manifest.Slug, "err", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
			return
		}
		defer func() { _ = db.Close() }()

		sum := sha256.Sum256([]byte(raw))
		t, err := db.TokenByHash(r.Context(), hex.EncodeToString(sum[:]))
		if err != nil {
			s.log.Error("token lookup failed", "notebook", nb.Manifest.Slug, "err", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
			return
		}
		if t == nil || t.Revoked() {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid token"})
			return
		}
		grants, err := platform.ParseGrants(t.Spec())
		if err != nil {
			// Stored scopes failed validation — fail closed and
			// shout: a token must never degrade into no access
			// silently, and never into unrestricted access.
			s.log.Error("stored token scopes invalid",
				"notebook", nb.Manifest.Slug, "token", t.ID, "err", err)
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
			return
		}
		qualifier := ""
		if qf != nil {
			qualifier = qf(r)
		}
		if !platform.GrantsAllow(grants, resource, action, qualifier) {
			writeJSON(w, http.StatusForbidden, map[string]string{
				"error": "forbidden", "missing": resource + ":" + action,
			})
			return
		}
		if err := db.TokenTouch(r.Context(), t.ID); err != nil {
			s.log.Warn("token touch failed", "notebook", nb.Manifest.Slug, "err", err)
		}
		ctx := context.WithValue(r.Context(), keyUser, "token:"+t.Name)
		h(w, r.WithContext(ctx))
	})
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	const prefix = "bearer "
	if len(h) > len(prefix) && strings.EqualFold(h[:len(prefix)], prefix) {
		return strings.TrimSpace(h[len(prefix):])
	}
	return ""
}
