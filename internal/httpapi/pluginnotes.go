package httpapi

import (
	"context"
	"errors"

	"noteroot/internal/frontmatter"
	"noteroot/internal/git"
	"noteroot/internal/plugins"
	"noteroot/internal/vault"
)

// pluginNoteStore is the note capability exposed to plugins. Writes
// run the same bookkeeping as putNote — identity minting, search
// index, git autocommit, audit+bus — with the plugin's own
// attribution, so a plugin write is indistinguishable from an HTTP
// write except for who the audit says did it. No preconditions: a
// plugin write is last-wins among extensions, never between humans
// (humans hold the If-Match path).
type pluginNoteStore struct {
	s    *Server
	slug string
}

func (a *pluginNoteStore) Read(ctx context.Context, rel string) (string, error) {
	v, ok := a.s.vaultFor(a.slug)
	if !ok {
		return "", plugins.ErrNotFound
	}
	data, err := v.Read(rel)
	if err != nil {
		if errors.Is(err, vault.ErrNotFound) {
			return "", plugins.ErrNotFound
		}
		return "", err
	}
	return string(data), nil
}

func (a *pluginNoteStore) Write(ctx context.Context, wr plugins.WriteRequest) error {
	nb, ok := a.s.reg.Get(a.slug)
	if !ok {
		return errors.New("unknown notebook")
	}
	v := vault.Open(nb.Layout.Vault())
	body := []byte(wr.Content)

	fm := frontmatter.Parse(body)
	id := fm.ID
	if !frontmatter.ValidID(id) {
		id = frontmatter.NewID()
		body = frontmatter.WithID(body, id)
	}

	res, err := v.Write(wr.Path, body, vault.WriteOpts{Actor: wr.Actor})
	if err != nil {
		return err
	}
	a.s.upsertIdentity(ctx, nb, wr.Path, id)
	a.s.reindex(ctx, nb, v)
	a.s.markGitDirty(nb, git.Op{Action: "write", Path: wr.Path, Actor: wr.Actor})
	a.s.emitAs(ctx, wr.Actor, wr.Origin, wr.Depth, nb, "note.write", wr.Path,
		map[string]any{"id": id, "sha": res.Sha, "created": res.Created})
	return nil
}
