# Linking: wikilinks, backlinks, forward links

Design for note identity and `[[wikilinks]]` with backlinks and forward
links, in the spirit of Joplin and SilverBullet. Lands the two items
deferred from stage 3: server-side ULID minting and wikilinks/backlinks.

Status: backend complete (slices 1–5) and UI complete (slice 6).

## 1. Why identity comes first

Links must survive renames and moves. A link by path breaks when the note
moves; a link by title breaks when the title changes. Architecture §3
already committed to ULID+checksum identity for exactly this reason:
"with ULIDs no link rewriting is needed."

So the feature is two layers:

1. **Identity** — every note gets a stable 28-char id the moment it is
   created (or on first scan if it was created outside the app). Durable
   map in `state.db.note_identity`.
2. **Links** — `[[wikilinks]]` resolve against identity first, then path,
   then title/alias. Backlinks and forward links are indexed, disposable,
   rebuildable from the vault.

## 2. Identity format

`docs/schema/frontmatter.json` already pins the format: 28 chars,
Crockford base32 (`0-9A-HJKMNP-TV-Z`), no `I`, `L`, `O`, `U`.

- **Chars 1–26**: a ULID — 48-bit big-endian millisecond timestamp +
  80 bits `crypto/rand`, encoded Crockford base32. Sortable by creation
  time; unique by randomness. No external dependency; ~30 lines.
- **Chars 27–28**: a checksum — CRC32 (IEEE) of the 26-char ULID,
  reduced modulo 1024 (10 bits), encoded as 2 Crockford chars. Detects
  typos and single-character corruption in copied ids. Not a security
  property; ids are not secrets or capabilities.

Validation is already enforced by `idRe` in `frontmatter.Parse`; a
malformed id is discarded and the caller (write path) mints a fresh one.

## 3. Minting

`frontmatter.NewID()` returns a fresh id. `frontmatter.WithID(src, id)`
injects or replaces the top-level `id:` line inside an existing
frontmatter block, or prepends a new block when there is none. Textual
injection (not YAML re-serialization) preserves the author's formatting,
comments, and key order — the id line is the only thing touched.

The write path (`PUT /n/{slug}/note`) mints when:

- the body has no frontmatter block, or
- the block has no `id:`, or
- the `id:` value fails `ValidID` (malformed).

Existing valid ids are never rewritten. On success the handler upserts
`note_identity(path, uid)` in `state.db` — the durable map per ADR 0002.
A backfill pass (on index Update) inserts rows for notes created outside
the app, so externally-edited files converge without a special import.

## 4. Wikilink syntax

```
[[Target]]           link by id, path stem, title, or alias
[[Target|Label]]     same resolution, custom display text
```

- `Target` may not contain `]`, `|`, or newlines.
- `Label` defaults to `Target`.
- Parsed as a goldmark inline node, so `[[links]]` inside code spans and
  code blocks are never linkified.
- Bare `[[` without a closing `]]` is literal text.

## 5. Resolution (notebook-scoped)

Order, first match wins:

1. **Exact id** — 28-char valid id, looked up in `note_identity`.
2. **Path stem** — `[[projects/plan]]` or `[[projects/plan.md]]` matches
   the note at `projects/plan.md`.
3. **Title** — case-sensitive match against frontmatter `title`, or the
   filename stem when title is absent. Only a *unique* title resolves;
   ambiguity renders the link dead rather than guessing.
4. **Alias** — same rule against frontmatter `aliases` entries.

Unresolved targets render as a visible dead link
(`<span class="wikilink dead">Label</span>`) — never silently dropped,
never a broken navigation.

## 6. Index: links table

`index.db` gains a forward-only migration:

```sql
CREATE TABLE links (
    from_path TEXT NOT NULL,
    target    TEXT NOT NULL,           -- raw wikilink target text
    to_path   TEXT NOT NULL DEFAULT '', -- resolved; '' = unresolved
    PRIMARY KEY (from_path, target)
);
```

`docs` gains `id` and `aliases` columns for the resolution map; `links`
gains a `label` column (the wikilink label text, `''` when absent).
Aliases are stored **newline-joined** (`strings.Join(aliases, "\n")`),
not space-joined as first drafted — alias values may contain spaces, and
newline is the only delimiter that cannot appear inside a single alias.
All columns are derived — the index stays fully disposable (ADR 0002
unchanged).

Build/Update: each changed note's body is scanned for `[[...]]`
targets; after all docs are read, a resolution map is built from the
current vault state (uid → path from `note_identity`, restricted to
paths still indexed; then unique docs.id → path, path/stem → path,
unique title → path, unique alias → path; per-doc aliases are deduped,
first match wins, later classes never override earlier ones) and each
raw target is resolved. Rows for changed notes are replaced; rows for
gone notes are deleted; whenever anything changed, `to_path` for *all*
rows is reconciled so renamed/moved/retitled targets re-resolve (stale
links die visibly instead of silently pointing at old paths).

## 7. API

`GET /n/{slug}/note?path=...` gains two fields:

```json
{
  "backlinks": [{"path": "index.md", "title": "Index"}],
  "outgoing":  [{"target": "projects/plan", "path": "projects/plan.md",
                 "label": "the plan"}]
}
```

- `backlinks` — notes whose indexed links include this path (incoming).
- `outgoing` — this note's wikilinks with resolution result (forward).
- The rendered HTML resolves each wikilink against the same links table,
  so display and data never disagree. The note handler refreshes the
  index (mtime-cheap) before serving, same as search.

## 8. UI

- Wikilinks are real `<a href="/n/{slug}/note?path=...">` with
  `data-path` for the SPA click handler; middle-click and no-JS still
  work.
- Dead links render styled but inert.
- Below the note: a "Linked from" panel (backlinks) and a "Links to"
  panel (outgoing, with unresolved targets listed as plain text).
- Panels are plain lists of clickable paths — no graph view in v1.

## 9. Non-goals

- Embeds, transclusion, block references (`[[note#heading]]` is not v1;
  the `#` suffix is treated as part of the target and simply won't
  resolve until headings are addressable).
- Cross-notebook links.
- Auto-complete / link picker in the editor.
- Graph visualization.

## 10. Build slices

1. **Identity**: `NewID`, `ValidID`, `WithID` + tests.
2. **Mint on write**: `state.UpsertIdentity`, write-path injection,
   response `id`, identity backfill on index Update + tests.
3. **Wikilink parse**: goldmark inline extension, dead-link rendering
   without a resolver + tests.
4. **Index links**: migration, extract/resolve/store, `Backlinks` /
   `Outgoing` queries + tests.
5. **HTTP integration**: note response gains `backlinks`/`outgoing`;
   renderer resolves via links table + tests.
6. **UI**: panels, click handling, CSS + manual smoke checklist.

Each slice keeps `make check` green.
