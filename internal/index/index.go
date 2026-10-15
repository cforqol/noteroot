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
	if _, err := tx.ExecContext(ctx, `DELETE FROM links`); err != nil {
		_ = tx.Rollback()
		return Stats{}, err
	}
	for _, d := range docs {
		if err := insertDoc(ctx, tx, d); err != nil {
			_ = tx.Rollback()
			return Stats{}, err
		}
	}
	// Build resolution map and store links
	resolver := buildResolver(docs)
	for _, d := range docs {
		if err := storeLinks(ctx, tx, d, resolver); err != nil {
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
		if _, err := tx.ExecContext(ctx, `DELETE FROM links WHERE from_path = ?`, p); err != nil {
			_ = tx.Rollback()
			return Stats{}, err
		}
	}
	// Rebuild links for changed docs
	if len(changed) > 0 {
		// Build resolver from ALL current docs (changed + unchanged) for accurate resolution
		allDocs, err := x.loadAllDocs(ctx, tx)
		if err != nil {
			_ = tx.Rollback()
			return Stats{}, err
		}
		resolver := buildResolver(allDocs)
		for _, d := range changed {
			// Delete old links first
			if _, err := tx.ExecContext(ctx, `DELETE FROM links WHERE from_path = ?`, d.path); err != nil {
				_ = tx.Rollback()
				return Stats{}, err
			}
			if err := storeLinks(ctx, tx, d, resolver); err != nil {
				_ = tx.Rollback()
				return Stats{}, err
			}
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
	path    string
	title   string
	id      string
	aliases string
	tags    string
	body    string
	links   []string // raw wikilink targets extracted from body
	mtime   int64
	size    int64
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
	aliases := frontmatter.Strings(parsed.Meta, "aliases")
	return doc{
		path:    rel,
		title:   title,
		id:      parsed.ID,
		aliases: strings.Join(aliases, " "),
		tags:    strings.Join(frontmatter.Strings(parsed.Meta, "tags"), " "),
		body:    string(parsed.Body),
		links:   extractWikilinks(parsed.Body),
		mtime:   n.ModTime,
		size:    n.Size,
	}, true
}

// extractWikilinks scans markdown for [[target]] and [[target|label]]
// patterns and returns unique targets. Does not resolve; stores raw text.
func extractWikilinks(body []byte) []string {
	var targets []string
	seen := make(map[string]bool)
	s := string(body)
	for i := 0; i < len(s); {
		if i+4 < len(s) && s[i:i+2] == "[[" {
			end := strings.Index(s[i+2:], "]]")
			if end < 0 {
				i++
				continue
			}
			content := s[i+2 : i+2+end]
			// Extract target (before pipe if present)
			target := content
			if pipe := strings.IndexByte(content, '|'); pipe >= 0 {
				target = content[:pipe]
			}
			target = strings.TrimSpace(target)
			if target != "" && !seen[target] {
				targets = append(targets, target)
				seen[target] = true
			}
			i += 2 + end + 2
		} else {
			i++
		}
	}
	return targets
}

func insertDoc(ctx context.Context, tx *sql.Tx, d doc) error {
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO docs(path, title, id, aliases, mtime, size) VALUES (?, ?, ?, ?, ?, ?)`,
		d.path, d.title, d.id, d.aliases, d.mtime, d.size); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx,
		`INSERT INTO docs_fts(path, title, tags, body) VALUES (?, ?, ?, ?)`,
		d.path, d.title, d.tags, d.body)
	return err
}

func upsertDoc(ctx context.Context, tx *sql.Tx, d doc) error {
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO docs(path, title, id, aliases, mtime, size) VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(path) DO UPDATE SET title=excluded.title, id=excluded.id, aliases=excluded.aliases, mtime=excluded.mtime, size=excluded.size`,
		d.path, d.title, d.id, d.aliases, d.mtime, d.size); err != nil {
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

// loadAllDocs reads all indexed docs (for building a complete resolver).
// Uses the provided transaction so it sees docs upserted in the same tx.
func (x *Index) loadAllDocs(ctx context.Context, tx *sql.Tx) ([]doc, error) {
	rows, err := tx.QueryContext(ctx, `SELECT path, title, id, aliases FROM docs`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var docs []doc
	for rows.Next() {
		var d doc
		if err := rows.Scan(&d.path, &d.title, &d.id, &d.aliases); err != nil {
			return nil, err
		}
		docs = append(docs, d)
	}
	return docs, rows.Err()
}

// buildResolver creates a resolution map from docs: id→path, path→path,
// title→path, alias→path. Only unique matches are added; ambiguous titles
// or aliases are omitted.
func buildResolver(docs []doc) map[string]string {
	res := make(map[string]string)
	titleCount := make(map[string]int)
	aliasCount := make(map[string]int)
	
	// Count occurrences first
	for _, d := range docs {
		titleCount[d.title]++
		for _, a := range strings.Fields(d.aliases) {
			aliasCount[a]++
		}
	}
	
	// Build map with uniqueness check
	for _, d := range docs {
		// ID → path (always unique by database constraint)
		if d.id != "" {
			res[d.id] = d.path
		}
		// path stem → path (with and without .md)
		stem := strings.TrimSuffix(d.path, ".md")
		res[stem] = d.path
		res[d.path] = d.path
		// title → path (only if unique)
		if titleCount[d.title] == 1 {
			res[d.title] = d.path
		}
		// alias → path (only if unique)
		for _, a := range strings.Fields(d.aliases) {
			if aliasCount[a] == 1 {
				res[a] = d.path
			}
		}
	}
	return res
}

// storeLinks resolves each wikilink target in doc and inserts a links row.
func storeLinks(ctx context.Context, tx *sql.Tx, d doc, resolver map[string]string) error {
	for _, target := range d.links {
		toPath := resolver[target]
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO links(from_path, target, to_path) VALUES (?, ?, ?)`,
			d.path, target, toPath); err != nil {
			return err
		}
	}
	return nil
}

// Link describes one wikilink with its resolution status.
type Link struct {
	Target string // raw wikilink text
	Path   string // resolved path, empty if unresolved
	Title  string // title of resolved note, empty if unresolved
}

// Backlinks returns notes that link to the given path (incoming).
func (x *Index) Backlinks(ctx context.Context, targetPath string) ([]Link, error) {
	rows, err := x.db.QueryContext(ctx, `
		SELECT DISTINCT l.from_path, d.title
		FROM links l
		JOIN docs d ON d.path = l.from_path
		WHERE l.to_path = ?
		ORDER BY d.title`, targetPath)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var links []Link
	for rows.Next() {
		var link Link
		if err := rows.Scan(&link.Path, &link.Title); err != nil {
			return nil, err
		}
		links = append(links, link)
	}
	return links, rows.Err()
}

// Outgoing returns wikilinks from the given path with resolution (forward).
func (x *Index) Outgoing(ctx context.Context, fromPath string) ([]Link, error) {
	rows, err := x.db.QueryContext(ctx, `
		SELECT l.target, l.to_path, COALESCE(d.title, '') AS title
		FROM links l
		LEFT JOIN docs d ON d.path = l.to_path
		WHERE l.from_path = ?
		ORDER BY l.target`, fromPath)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var links []Link
	for rows.Next() {
		var link Link
		if err := rows.Scan(&link.Target, &link.Path, &link.Title); err != nil {
			return nil, err
		}
		links = append(links, link)
	}
	return links, rows.Err()
}

// Resolver builds and returns a function that resolves wikilink targets to
// paths using the current index state.
func (x *Index) Resolver(ctx context.Context) (func(target string) (string, bool), error) {
	rows, err := x.db.QueryContext(ctx, `SELECT path, id, title, aliases FROM docs`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	
	var docs []doc
	for rows.Next() {
		var d doc
		if err := rows.Scan(&d.path, &d.id, &d.title, &d.aliases); err != nil {
			return nil, err
		}
		docs = append(docs, d)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	
	resMap := buildResolver(docs)
	return func(target string) (string, bool) {
		path, ok := resMap[target]
		return path, ok
	}, nil
}
