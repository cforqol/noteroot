# Architecture

Reference document. Decisions live in `docs/adr/`; this describes the system.

## 0. Principles

See README. The load-bearing ones: files are truth, no partial-valid writes,
classify failures first, extensions over core, isolation by boundary, no
database holds primary data.

## 1. Topology

Single Go binary in Docker behind Traefik (TLS, forward-auth). Vault on a bind
mount. Optional S3-compatible object storage with presigned URLs (stage 7).
In-process backups (git bundle + state snapshot; remote targets planned).
Everything below HTTP is instantiated per notebook.

## 2. Auth

Forward-auth sets `X-Auth-User`; Traefik strips client-supplied values.
`/healthz` and `/readyz` are unauthenticated. CSRF is enforced on mutating
routes: form content types rejected, Origin/Referer allowlist checked, no
CORS granted, no application session cookie. Scoped API tokens on
`/api/v1` (planned, stage 9).

## 3. Data model

**Hierarchy** — user → notebook → folders → notes. Notebook is the isolation
unit. Folders are path segments. Depth cap 3–5. Collections are virtual, via
frontmatter, notebook-scoped.

**Identity** — ULID+checksum in frontmatter `id`, reserved. Assigned at
creation, and on first scan for externally-created files. Durable map in
`state.db`. Repair order: map → git → re-mint.

**Frontmatter** — see `docs/schema/frontmatter.json`. Parse defensively; a
malformed note must never fail to open.

**Resources** — content-addressed. `blob_sha` is the hash of stored bytes.
Immutable after promotion. Filename is the checksum.

**Databases** — `index.db` disposable; `state.db` rebuildable (ADR 0002);
`platform.db` rebuildable except secrets.

## 4. Write path

All writes through `vault.Write(path, content, opts{BaseSha, Actor})`.
Serialized per path. `BaseSha` mismatch → 412. Forced writes emit
`note.external_change`; the editor reloads if clean, banners if dirty.

**Git autocommit** — per-notebook flusher coalesces
write/delete/restore/rename/import marks into at most one commit per 60 s
window. Commit failures restore the pending ops and retry on the next
window; they never fail the HTTP write.

**Buffer/lock** — desktop buffer with draft autosave and swap file ships
with vim mode (stage 4). Phone autosaves directly (`If-Match` on every
save) plus the crash pad. Locks are advisory and TTL'd; `If-Match` is the
correctness backstop.

**Crash pad** — `localStorage` (quota-guarded), keyed per notebook+path,
stores base sha; cleared only on HTTP 200/201. Restorable from any tab.

**Bulk ops** — rename/move (`PATCH /n/{slug}/note`) is an atomic per-path
`vault.Rename` with a matching identity-map update; with ULIDs no link
rewriting is needed. Imports use staged validation with per-item isolation
and batched writes.

## 5. Read path

Scanner with mtime as truth, walking `vault/` only (fsnotify as a change
hint is planned). One scanner feeds FTS, refs, and identity. Caches:
per-notebook disk with content-hash keys (transform outputs), ETags on note
reads; in-process LRU and singleflight are planned.

## 6. Resources

Pure `Policy` → `Pipeline` → `ResourceStore` / `Transformer`. `LocalStore` in
v1. Upload journal with resume, verification, dedup, GC with grace.
`ffmpeg`/`ffprobe` produce stills and video posters; the narrow `Transformer`
interface allows a vips-backed swap later.

**Quota** — `NOTEROOT_QUOTA_BYTES` per notebook (0 = unlimited), enforced at
every upload entry point (direct and presign) with HTTP 507. Stats expose
`usedBytes`/`quotaBytes`; usage is dedup-aware (content-addressed bytes
counted once).

**GC** — background sweep every `NOTEROOT_GC_INTERVAL` (default 24h) plus a
manual trigger `POST /n/{slug}/resources/gc`; deletes unreferenced blobs
past their grace period and returns removal counts.

## 7. Dedup

Media: exact by `blob_sha`, notebook-scoped. Text: SimHash,
detect-don't-merge (planned). Canonicalization is explicit and keeps
originals in git.

## 8. Editor

Two modes. Desktop vim mode in a sandboxed iframe (vim.wasm, xterm.js;
textarea fallback via circuit breaker), source view (stage 4). Phone/shell
mode is a plain textarea with debounced autosave, crash pad, and advisory
locks (shipped in stage 3).

## 9. Extensibility

Planned substrate (stage 9): manifest, permissions, per-notebook event bus
(coalesced, actor-suppressed, depth-limited), registry, scheduler, secrets,
kv, audit.

## 10. Error handling

Failure matrix with classification. Atomic writes, reconcile on boot,
quarantine, trash, `state.db` rebuild via the rebuildability guard; the
editor's circuit breaker falls back to a plain textarea. Server-side
per-notebook circuit breakers and a global disk budget are planned.

## 11. Backups

Per notebook: git bundle + `VACUUM INTO` (v1, local). `POST /n/{slug}/backup`
writes timestamped `<slug>-YYYYMMDDTHHMMSSZ.bundle` and `.db` alongside a
`manifest.json` in `.notebook/backups/`. Partial failures (non-git vault,
state.db absent) are recorded in `manifest.Errors`; the endpoint still returns
200 with the partial manifest. Restic / remote targets deferred to v2.

## 12. Import/export

`GET /n/{slug}/export` streams a tar.gz of all vault `.md` files and
`notebook.toml`. `POST /n/{slug}/import` accepts raw gzip or multipart
`file` field. Staged: all entries validated before any writes. Per-item
isolation: one bad entry (bad path, non-.md, escaping vault) is recorded as
`"error"` without aborting others. Idempotent by SHA-256: identical content
returns `"skipped"`. `?dry_run=true` classifies without writing.

## 13–15. Security, operations, future

Strict sanitizer, CSRF enforcement, path/Origin allowlists, isolation by
iframe sandbox (ship). CSP and a platform audit log are planned. Structured
logs and forward-only migrations ship. Also planned: diagnostics bundle,
health page, alerts. Stage 9: plugin substrate, scoped API tokens, remote
backup targets.
