package httpapi

import (
	"context"
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
	s.emitAs(r.Context(), actorFrom(r), "http", 0, nb, typ, path, data)
}

// emitAs is emit with explicit attribution. HTTP mutations attribute
// as actor=<user>, origin="http", depth=0; plugin-originated writes
// carry the plugin's actor/origin and the hop depth of the event
// chain that caused them, which is what bounds runaway extension
// chains at the bus's depth limit.
func (s *Server) emitAs(ctx context.Context, actor, origin string, depth int,
	nb *notebooks.Notebook, typ, path string, data map[string]any) {
	slug := nb.Manifest.Slug

	detail := ""
	if len(data) > 0 {
		if b, err := json.Marshal(data); err == nil {
			detail = string(b)
		}
	}
	if db, _, err := platform.Open(ctx, nb.Layout.PlatformDB()); err != nil {
		s.log.Warn("audit open failed", "notebook", slug, "err", err)
	} else {
		if err := db.AuditAppend(ctx, actor, typ, slug+"/"+path, detail); err != nil {
			s.log.Warn("audit append failed", "notebook", slug, "type", typ, "err", err)
		}
		_ = db.Close()
	}

	s.plat.Bus(slug).Publish(platform.Event{
		Type:   typ,
		Slug:   slug,
		Path:   path,
		Actor:  actor,
		Origin: origin,
		Depth:  depth,
		Data:   data,
	})
}
