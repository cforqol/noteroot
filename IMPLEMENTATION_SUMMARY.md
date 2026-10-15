# Linking Feature Implementation Summary

**Date**: 2026-10-15  
**Status**: ✅ Complete (slices 1–6)  
**Gate**: `make check` green (fmt, vet, staticcheck + golangci-lint v2,
`go test -race ./...`, govulncheck — 0 affecting vulnerabilities)

## Overview

Implements the backend of `docs/linking.md`: note identity
(ULID+checksum), mint-on-write, wikilink parse/render, disposable link
index with full re-resolution, and HTTP integration (backlinks/outgoing
plus render-from-links-table per design §7).

An earlier pass claimed slices 2 and 5 were done; they were not wired
end-to-end. This session closed every remaining design gap:

| Gap | Fix |
|---|---|
| Index did not own `state.db` | 3-arg `index.Open(ctx, dbPath, stateDBPath)`; `Index{db, st}`; `Close` closes both |
| `SyncIdentityFromVault` never called | full sync on index Build; Update gated on `identity_synced` marker (full resync when missing or when identity empty), incremental `SyncIdentityPaths` on changed/gone paths |
| No prune of moved/deleted notes | sync deletes rows whose path left the vault; lost frontmatter id keeps the row (path still valid), gone path prunes it |
| Write path never minted | PUT mints via `frontmatter.NewID` + `WithID` when id missing/malformed, upserts `note_identity` (non-blocking, logged), returns `id` in response |
| No link re-resolution | `reconcileLinks` rewrites every drifted `to_path` when anything changed (rename/retitle/delete ⇒ stale links die visibly) |
| Resolution ignored identity map | `buildResolver(docs, idMap)`: uid→path (indexed paths only) wins; then unique docs.id, path/stem, unique title, unique alias; first match wins, per-doc alias dedupe |
| No `label` on links | migration 5 adds `links.label`; extractor passes wikilink label through; `Link` json tags `target,omitempty,path,title,label,omitempty` |
| Render ≠ data | note handler renders from links-table rows via `markdown.RenderWithResolver` (dead spans for `to_path=''`); `backlinks`/`outgoing` fields added |

## 📊 Slices

### Slice 1: Identity (ULID+Checksum) — done
- `internal/frontmatter`: 28-char id (Crockford base32 ULID + CRC32 checksum), `NewID`, `ValidID`, textual `WithID` (preserves formatting/comments/key order; fixed space-in-key-line bug).

### Slice 2: Mint on Write + identity map — done
- `internal/state/identity.go`: `note_identity(path, uid, first_seen, last_seen)` UNIQUE(uid); `UpsertIdentity`, `SyncIdentityFromVault` (prune gone paths, skip duplicate uids — copy-pasted frontmatter never aborts), `SyncIdentityPaths(changed, gone)`, `IdentityMap` (uid→path), `DeleteIdentity`.
- `internal/httpapi/write.go`: PUT mints, upserts non-blockingly, responds with `id`.

### Slice 3: Wikilink Parsing — done
- `internal/markdown`: goldmark inline parser for `[[Target]]` / `[[Target|Label]]`; code spans/blocks ignored; `RenderWithResolver` emits `<a class="wikilink" data-path="...">` or `<span class="wikilink dead">`.

### Slice 4: Index Links — done
- `internal/index`: `links(from_path, target, to_path, label)`, `docs(id, aliases)`; aliases stored **newline-joined** (deviation from draft "space-joined": alias values may contain spaces); Build/Update extract via AST, build resolution map (identity map first), resolve, reconcile all rows; `meta identity_synced` marker; fully disposable, forward-only migrations.

### Slice 5: HTTP Integration — done
- `internal/httpapi/notes.go`: note handler refreshes index (degrades gracefully, never blocks), serves `backlinks`/`outgoing`, renders HTML from the same links table.
- E2E test `TestNote_linksIntegration` asserts backlinks/outgoing/label/dead-span/data-path.

### Slice 6: UI (frontend) — done
- `web/app.js`: "Outgoing links" / "Backlinks" panels appended below note
  content (design §8); resolved items are real `<a class="wikilink"
  data-path>` anchors, unresolved outgoing targets render as
  `<span class="wikilink dead">` with the raw target; panels omitted
  entirely when the index is unavailable.
- Delegated SPA click handler on `#note`: plain left-click calls
  `openNote(data-path)`; middle-click / cmd / ctrl / shift keep browser
  defaults (new tab works; no-JS works via real hrefs).
- `web/style.css`: `.wikilink` (accent, underline), `.wikilink.dead`
  (muted, dotted underline), `.links-panel` section styling.
- Verified live: PUT mints ids, `[[Bee]]` alias and `[[alpha]]` title
  resolve, dead span for `[[missing]]`, assets serve embedded.

## 🧪 Tests & quality

### Backend Tests
- New/updated this session: `state/identity_test.go` (prune, dup-uid skip, IdentityMap, DeleteIdentity), `index_test.go` (identity-map wins, title-beats-alias, re-resolve on retitle/delete, label storage, code-span exclusion, backfill), `write_test.go` (preserve valid id, re-mint malformed, record identity), `notes_test.go` (§7 E2E, index-unavailable render fallback).
- `render_test.go` SA4000 fixed (singleton assertion).
- errcheck debt (19 sites, incl. pre-existing `defer Close`/`Rollback`/`Setenv`) fixed; `make setup` now installs golangci-lint **v2** (v1 cannot typecheck Go 1.27).
- `golang.org/x/net` upgraded v0.26.0 → v0.38.0 (GO-2025-3595, bluemonday→html.Tokenizer).

### UI Integration Tests
- **JavaScript unit tests** (`web/app.test.js`): 12 tests covering panel building (`linksPanels`, `panel`, `noteLink`), wikilink structure (`<a class="wikilink" data-path>` vs `<span class="wikilink dead">`), and SPA click handler (plain left-click navigates, middle/cmd/ctrl preserve browser defaults).
- **Go E2E tests** (`internal/httpapi/notes_test.go`): 5 new functions verifying complete HTML rendering: `TestNote_wikilinkHTMLStructure`, `TestNote_backlinksOutgoingJSONShape`, `TestNote_wikilinkSPAAttributes`, `TestNote_panelDataConsistency` — validate JSON field structure, href encoding, data-path attributes, and display/data agreement per design §7.
- **Test infrastructure**: `web/package.json` (jsdom ^25.0.0), `web/README.test.md` (documentation), `Makefile` targets (`test-web`, `test-all`).
- All tests passing: 12 JS tests ✓, Go httpapi suite ✓.

## 📚 References

- Design: `docs/linking.md` (status: complete)
- ADR 0002: disposable derived data
- Frontmatter schema: `docs/schema/frontmatter.json`
