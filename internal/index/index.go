// Package index is the disposable per-notebook full-text search index.
//
// index.db lives in the notebook app dir and holds no primary data: every
// row is derived from vault files and the whole database can be deleted and
// rebuilt at any time. Corrupt files are quarantined, never repaired.
package index

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"noteroot/internal/frontmatter"
	"noteroot/internal/markdown"
	"noteroot/internal/state"
	"noteroot/internal/vault"
)

// Index is a per-notebook FTS5 search index over vault notes.
type Index struct {
	db *state.DB
	st *state.DB
}

// Open creates or opens the index database, quarantining corrupt files.
// The parent directory is created when missing. stateDBPath is the
// notebook's durable state.db: the index holds it open because link
// resolution reads note_identity (design §5.1) and the backfill pass
// (design §3) writes it during Build/Update.
func Open(ctx context.Context, dbPath, stateDBPath string) (*Index, bool, error) {
	if dir := filepath.Dir(dbPath); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, false, err
		}
	}
	st, stRebuilt, err := state.Open(ctx, stateDBPath)
	if err != nil {
		return nil, false, err
	}
	db, rebuilt, err := state.OpenDB(ctx, dbPath, Migrate)
	if err != nil {
		_ = st.Close()
		return nil, false, err
	}
	return &Index{db: db, st: st}, rebuilt || stRebuilt, nil
}

func (x *Index) Close() error {
	err := x.db.Close()
	if stErr := x.st.Close(); err == nil {
		err = stErr
	}
	return err
}

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
	// Backfill note_identity before resolving: the durable map feeds id
	// links (design §5.1) and a full rebuild is the one moment where a
	// full vault pass is cheap enough to be free (design §3).
	if _, err := x.st.SyncIdentityFromVault(ctx, v); err != nil {
		return Stats{}, err
	}
	if err := x.metaSet(ctx, metaIdentitySynced, "1"); err != nil {
		return Stats{}, err
	}
	idMap, err := x.st.IdentityMap(ctx)
	if err != nil {
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
	resolver := buildResolver(docs, idMap)
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
	// Keep note_identity in step with what this Update already knows
	// changed or vanished (design §3 backfill): nothing else can differ
	// from the vault without tripping the mtime/size comparison above.
	if err := x.syncIdentity(ctx, v, changed, gone, len(notes) > 0); err != nil {
		return Stats{}, err
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
	// Rebuild links for changed docs from a resolver over ALL current
	// docs (changed + unchanged) plus the durable identity map, then
	// reconcile every stored row: an edit that never touched the linking
	// note can still re-point or dead-end its targets (title change,
	// rename, deletion).
	allDocs, err := x.loadAllDocs(ctx, tx)
	if err != nil {
		_ = tx.Rollback()
		return Stats{}, err
	}
	idMap, err := x.st.IdentityMap(ctx)
	if err != nil {
		_ = tx.Rollback()
		return Stats{}, err
	}
	resolver := buildResolver(allDocs, idMap)
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
	if err := reconcileLinks(ctx, tx, resolver); err != nil {
		_ = tx.Rollback()
		return Stats{}, err
	}
	if err := tx.Commit(); err != nil {
		return Stats{}, err
	}
	stats.Updated = len(changed)
	stats.Deleted = len(gone)
	return stats, nil
}

// metaIdentitySynced marks the one-time full backfill of note_identity
// as done; after that only changed/gone paths are synced.
const metaIdentitySynced = "identity_synced"

func (x *Index) metaSet(ctx context.Context, key, value string) error {
	_, err := x.db.ExecContext(ctx,
		`INSERT INTO meta(key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// syncIdentity applies the identity backfill rules to one Update round.
// A full vault pass runs when the marker is missing (first run after an
// upgrade or index rebuild) or when the map is empty while notes exist
// (state.db recreated under a healthy index.db); otherwise only the
// changed and gone paths are touched.
func (x *Index) syncIdentity(ctx context.Context, v *vault.Vault, changed []doc, gone []string, hasNotes bool) error {
	marker, err := x.metaGet(ctx, metaIdentitySynced)
	if err != nil {
		return err
	}
	full := marker != "1"
	if !full {
		var n int
		if err := x.st.QueryRowContext(ctx, `SELECT COUNT(*) FROM note_identity`).Scan(&n); err != nil {
			return err
		}
		full = n == 0 && hasNotes
	}
	if full {
		if _, err := x.st.SyncIdentityFromVault(ctx, v); err != nil {
			return err
		}
		return x.metaSet(ctx, metaIdentitySynced, "1")
	}
	updates := make(map[string]string, len(changed))
	for _, d := range changed {
		updates[d.path] = d.id
	}
	_, err = x.st.SyncIdentityPaths(ctx, updates, gone)
	return err
}

func (x *Index) metaGet(ctx context.Context, key string) (string, error) {
	var v string
	err := x.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", nil
	case err != nil:
		return "", err
	}
	return v, nil
}

// reconcileLinks re-resolves every stored link row against resolver and
// rewrites the rows whose to_path drifted. Stale to_path is a correctness
// bug, not a freshness nicety: a deleted target must become a dead link,
// a renamed title must re-point, and the rendered HTML — which reads
// these rows — must agree with the panels.
func reconcileLinks(ctx context.Context, tx *sql.Tx, resolver map[string]string) error {
	rows, err := tx.QueryContext(ctx, `SELECT from_path, target, to_path FROM links`)
	if err != nil {
		return err
	}
	type drift struct{ from, target, want string }
	var fixes []drift
	for rows.Next() {
		var from, target, cur string
		if err := rows.Scan(&from, &target, &cur); err != nil {
			_ = rows.Close()
			return err
		}
		if want := resolver[target]; want != cur {
			fixes = append(fixes, drift{from, target, want})
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	_ = rows.Close()
	for _, f := range fixes {
		if _, err := tx.ExecContext(ctx,
			`UPDATE links SET to_path = ? WHERE from_path = ? AND target = ?`,
			f.want, f.from, f.target); err != nil {
			return err
		}
	}
	return nil
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
	defer func() { _ = rows.Close() }()
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
	links   []markdown.Wikilink // parsed by the same extension that renders them
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
		aliases: strings.Join(aliases, "\n"),
		tags:    strings.Join(frontmatter.Strings(parsed.Meta, "tags"), " "),
		body:    string(parsed.Body),
		links:   markdown.ExtractWikilinks(parsed.Body),
		mtime:   n.ModTime,
		size:    n.Size,
	}, true
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
	defer func() { _ = rows.Close() }()
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
	defer func() { _ = rows.Close() }()
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

// buildResolver creates the resolution map in design §5 order — exact
// id, path stem, unique title, unique alias — first match wins, so a
// later class can never shadow an earlier one.
//
// idMap is the durable note_identity map (uid→path) behind §5.1: ids it
// owns resolve even when the frontmatter id was lost, as long as the
// path is still indexed. Ids parsed from frontmatter count only when
// they are well-formed and unique across the vault — two notes sharing
// an id is ambiguous and renders dead rather than guessing.
//
// Aliases are newline-joined in doc.aliases (one entry per line) so a
// multi-word alias stays one alias; each doc counts an alias at most
// once, and only aliases unique across docs resolve.
func buildResolver(docs []doc, idMap map[string]string) map[string]string {
	res := make(map[string]string)

	have := make(map[string]bool, len(docs))
	for _, d := range docs {
		have[d.path] = true
	}

	// 1. Exact id: durable map first, then unique frontmatter ids that
	// the map does not already own.
	for uid, p := range idMap {
		if !frontmatter.ValidID(uid) || !have[p] {
			continue
		}
		if _, exists := res[uid]; !exists {
			res[uid] = p
		}
	}
	idCount := make(map[string]int, len(docs))
	for _, d := range docs {
		if d.id != "" && frontmatter.ValidID(d.id) {
			idCount[d.id]++
		}
	}
	for _, d := range docs {
		if d.id == "" || idCount[d.id] != 1 {
			continue
		}
		if _, exists := res[d.id]; !exists {
			res[d.id] = d.path
		}
	}

	// 2. Path stem and full path.
	for _, d := range docs {
		stem := strings.TrimSuffix(d.path, ".md")
		if _, exists := res[stem]; !exists {
			res[stem] = d.path
		}
		if _, exists := res[d.path]; !exists {
			res[d.path] = d.path
		}
	}

	// 3. Unique titles (case-sensitive, per design §5), as a full class
	// before aliases so a title never loses to another note's alias.
	titleCount := make(map[string]int, len(docs))
	for _, d := range docs {
		titleCount[d.title]++
	}
	for _, d := range docs {
		if titleCount[d.title] != 1 {
			continue
		}
		if _, exists := res[d.title]; !exists {
			res[d.title] = d.path
		}
	}

	// 4. Unique aliases, counted per doc so one note repeating an alias
	// cannot make its own alias ambiguous; they only fill keys the
	// earlier classes left open.
	aliasCount := make(map[string]int)
	aliasSets := make([]map[string]bool, len(docs))
	for i, d := range docs {
		set := make(map[string]bool)
		for _, a := range strings.Split(d.aliases, "\n") {
			a = strings.TrimSpace(a)
			if a == "" || set[a] {
				continue
			}
			set[a] = true
			aliasCount[a]++
		}
		aliasSets[i] = set
	}
	for i, d := range docs {
		for a := range aliasSets[i] {
			if aliasCount[a] != 1 {
				continue
			}
			if _, exists := res[a]; !exists {
				res[a] = d.path
			}
		}
	}
	return res
}

// storeLinks resolves each wikilink in doc and inserts a links row,
// keeping the display label so the outgoing panel can show it.
func storeLinks(ctx context.Context, tx *sql.Tx, d doc, resolver map[string]string) error {
	for _, wl := range d.links {
		toPath := resolver[wl.Target]
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO links(from_path, target, to_path, label) VALUES (?, ?, ?, ?)`,
			d.path, wl.Target, toPath, wl.Label); err != nil {
			return err
		}
	}
	return nil
}

// Link describes one wikilink with its resolution status. JSON tags pin
// the API shape of design §7 (lowercase keys; label only when the
// source wikilink set one).
type Link struct {
	Target string `json:"target,omitempty"` // raw wikilink target text
	Path   string `json:"path"`             // resolved path, "" if unresolved
	Title  string `json:"title"`            // title of resolved note, "" if unresolved
	Label  string `json:"label,omitempty"`  // display text when the source used [[target|label]]
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
	defer func() { _ = rows.Close() }()
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
		SELECT l.target, l.to_path, COALESCE(d.title, '') AS title, l.label
		FROM links l
		LEFT JOIN docs d ON d.path = l.to_path
		WHERE l.from_path = ?
		ORDER BY l.target`, fromPath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var links []Link
	for rows.Next() {
		var link Link
		if err := rows.Scan(&link.Target, &link.Path, &link.Title, &link.Label); err != nil {
			return nil, err
		}
		links = append(links, link)
	}
	return links, rows.Err()
}
