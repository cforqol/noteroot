// Package index is the disposable per-notebook full-text search index.
//
// index.db lives in the notebook app dir and holds no primary data: every
// row is derived from vault files and the whole database can be deleted and
// rebuilt at any time. Corrupt files are quarantined, never repaired.
package index

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"noteroot/internal/frontmatter"
	"noteroot/internal/state"
	"noteroot/internal/vault"
)

// Index is a per-notebook FTS5 search index over vault notes.
type Index struct {
	db *state.DB
}

// Open creates or opens the index database, quarantining corrupt files.
// The parent directory is created when missing.
func Open(ctx context.Context, dbPath string) (*Index, bool, error) {
	if dir := filepath.Dir(dbPath); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, false, err
		}
	}
	db, rebuilt, err := state.OpenDB(ctx, dbPath, Migrate)
	if err != nil {
		return nil, false, err
	}
	return &Index{db: db}, rebuilt, nil
}

func (x *Index) Close() error { return x.db.Close() }

// Stats reports what a Build or Update did. Files is notes scanned;
// Updated, Deleted and Skipped classify the notes that needed action.
type Stats struct {
	Files   int
	Updated int
	Deleted int
	Skipped int // unreadable notes; counted, never fatal
}

// Hit is one ranked search result.
//
// Snippet marks matches with \x01..\x02 control bytes, never HTML: callers
// serving snippets into HTML must escape first, then convert the markers to
// tags. Raw note text can contain anything, including tags that look exactly
// like highlight markup.
type Hit struct {
	Path    string
	Title   string
	Snippet string
	Rank    float64
}

// Build reindexes the whole vault from scratch in one transaction.
func (x *Index) Build(ctx context.Context, v *vault.Vault) (Stats, error) {
	notes, err := listNotes(v)
	if err != nil {
		return Stats{}, err
	}
	docs, stats := readAll(ctx, v, notes)
	if err := ctx.Err(); err != nil {
		return Stats{}, err
	}
	tx, err := x.db.BeginTx(ctx, nil)
	if err != nil {
		return Stats{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM docs`); err != nil {
		_ = tx.Rollback()
		return Stats{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM docs_fts`); err != nil {
		_ = tx.Rollback()
		return Stats{}, err
	}
	for _, d := range docs {
		if err := insertDoc(ctx, tx, d); err != nil {
			_ = tx.Rollback()
			return Stats{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Stats{}, err
	}
	return stats, nil
}

// Update reindexes changed notes and drops indexed notes that are gone,
// using stored mtime and size as the change signal.
func (x *Index) Update(ctx context.Context, v *vault.Vault) (Stats, error) {
	notes, err := listNotes(v)
	if err != nil {
		return Stats{}, err
	}
	known, err := x.known(ctx)
	if err != nil {
		return Stats{}, err
	}
	var stats Stats
	var changed []doc
	seen := make(map[string]bool, len(notes))
	for _, n := range notes {
		if err := ctx.Err(); err != nil {
			return Stats{}, err
		}
		stats.Files++
		seen[n.Path] = true
		prev, ok := known[n.Path]
		if ok && prev.mtime == n.ModTime && prev.size == n.Size {
			continue
		}
		d, ok := readOne(v, n)
		if !ok {
			stats.Skipped++
			continue
		}
		changed = append(changed, d)
	}
	var gone []string
	for p := range known {
		if !seen[p] {
			gone = append(gone, p)
		}
	}
	if len(changed) == 0 && len(gone) == 0 {
		return stats, nil
	}
	tx, err := x.db.BeginTx(ctx, nil)
	if err != nil {
		return Stats{}, err
	}
	for _, d := range changed {
		if err := upsertDoc(ctx, tx, d); err != nil {
			_ = tx.Rollback()
			return Stats{}, err
		}
	}
	for _, p := range gone {
		if _, err := tx.ExecContext(ctx, `DELETE FROM docs WHERE path = ?`, p); err != nil {
			_ = tx.Rollback()
			return Stats{}, err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM docs_fts WHERE path = ?`, p); err != nil {
			_ = tx.Rollback()
			return Stats{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Stats{}, err
	}
	stats.Updated = len(changed)
	stats.Deleted = len(gone)
	return stats, nil
}

const defaultLimit = 50

// Search returns ranked hits with snippets. Malformed FTS5 queries,
// including unbalanced quotes, are reported as errors.
func (x *Index) Search(ctx context.Context, query string, limit int) ([]Hit, error) {
	if limit <= 0 {
		limit = defaultLimit
	}
	rows, err := x.db.QueryContext(ctx, `
		SELECT path, title, rank,
			snippet(docs_fts, 3, char(1), char(2), '…', 12) AS snippet
		FROM docs_fts WHERE docs_fts MATCH ? ORDER BY rank LIMIT ?`,
		query, limit)
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	defer rows.Close()
	var hits []Hit
	for rows.Next() {
		var h Hit
		if err := rows.Scan(&h.Path, &h.Title, &h.Rank, &h.Snippet); err != nil {
			return nil, err
		}
		hits = append(hits, h)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	return hits, nil
}

type doc struct {
	path  string
	title string
	tags  string
	body  string
	mtime int64
	size  int64
}

func listNotes(v *vault.Vault) ([]*vault.Node, error) {
	t, err := v.Tree()
	if err != nil {
		return nil, err
	}
	var out []*vault.Node
	var walk func(n *vault.Node)
	walk = func(n *vault.Node) {
		for _, c := range n.Children {
			if c.IsDir {
				walk(c)
			} else {
				out = append(out, c)
			}
		}
	}
	walk(t)
	return out, nil
}

func readAll(ctx context.Context, v *vault.Vault, notes []*vault.Node) ([]doc, Stats) {
	var docs []doc
	var stats Stats
	for _, n := range notes {
		if err := ctx.Err(); err != nil {
			return nil, Stats{}
		}
		stats.Files++
		d, ok := readOne(v, n)
		if !ok {
			stats.Skipped++
			continue
		}
		docs = append(docs, d)
	}
	return docs, stats
}

func readOne(v *vault.Vault, n *vault.Node) (doc, bool) {
	rel := n.Path
	src, err := v.Read(rel)
	if err != nil {
		return doc{}, false
	}
	parsed := frontmatter.Parse(src)
	title := parsed.Title
	if title == "" {
		title = strings.TrimSuffix(path.Base(rel), ".md")
	}
	return doc{
		path:  rel,
		title: title,
		tags:  strings.Join(frontmatter.Strings(parsed.Meta, "tags"), " "),
		body:  string(parsed.Body),
		mtime: n.ModTime,
		size:  n.Size,
	}, true
}

func insertDoc(ctx context.Context, tx *sql.Tx, d doc) error {
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO docs(path, title, mtime, size) VALUES (?, ?, ?, ?)`,
		d.path, d.title, d.mtime, d.size); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx,
		`INSERT INTO docs_fts(path, title, tags, body) VALUES (?, ?, ?, ?)`,
		d.path, d.title, d.tags, d.body)
	return err
}

func upsertDoc(ctx context.Context, tx *sql.Tx, d doc) error {
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO docs(path, title, mtime, size) VALUES (?, ?, ?, ?)
		 ON CONFLICT(path) DO UPDATE SET title=excluded.title, mtime=excluded.mtime, size=excluded.size`,
		d.path, d.title, d.mtime, d.size); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM docs_fts WHERE path = ?`, d.path); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx,
		`INSERT INTO docs_fts(path, title, tags, body) VALUES (?, ?, ?, ?)`,
		d.path, d.title, d.tags, d.body)
	return err
}

type stored struct {
	mtime int64
	size  int64
}

func (x *Index) known(ctx context.Context) (map[string]stored, error) {
	rows, err := x.db.QueryContext(ctx, `SELECT path, mtime, size FROM docs`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]stored{}
	for rows.Next() {
		var p string
		var s stored
		if err := rows.Scan(&p, &s.mtime, &s.size); err != nil {
			return nil, err
		}
		out[p] = s
	}
	return out, rows.Err()
}
