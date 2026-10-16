package httpapi

import (
	"encoding/json"
	"net/http"

	"noteroot/internal/notebooks"
	"noteroot/internal/platform"
)

// emit records a successful mutation in the notebook's audit trail and
// publishes it on the notebook's event bus. It never fails and never
// runs before the response logic is free to proceed: the mutation has
// already succeeded, extensions are recovery-grade observers. Audit
// detail is the JSON form of data (empty object when nil).
func (s *Server) emit(r *http.Request, nb *notebooks.Notebook, typ, path string, data map[string]any) {
	slug := nb.Manifest.Slug
	actor := actorFrom(r)

	detail := ""
	if len(data) > 0 {
		if b, err := json.Marshal(data); err == nil {
			detail = string(b)
		}
	}
	if db, _, err := platform.Open(r.Context(), nb.Layout.PlatformDB()); err != nil {
		s.log.Warn("audit open failed", "notebook", slug, "err", err)
	} else {
		if err := db.AuditAppend(r.Context(), actor, typ, slug+"/"+path, detail); err != nil {
			s.log.Warn("audit append failed", "notebook", slug, "type", typ, "err", err)
		}
		_ = db.Close()
	}

	s.plat.Bus(slug).Publish(platform.Event{
		Type:   typ,
		Slug:   slug,
		Path:   path,
		Actor:  actor,
		Origin: "http",
		Data:   data,
	})
}
