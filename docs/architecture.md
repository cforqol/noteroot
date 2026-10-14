# Architecture

Reference document. Decisions live in `docs/adr/`; this describes the system.

## 0. Principles

See README. The load-bearing ones: files are truth, no partial-valid writes,
classify failures first, extensions over core, isolation by boundary, no
database holds primary data.

## 1. Topology

Single Go binary in Docker behind Traefik (TLS, forward-auth). Vault on a bind
mount. Object storage is stage 7. Backup sidecar. Everything below HTTP is
instantiated per notebook.

## 2. Auth

Forward-auth sets `X-Auth-User`; Traefik strips client-supplied values.
`/healthz` and `/readyz` are unauthenticated. Session cookie and CSRF land at
stage 3 with writes. Scoped API tokens on `/api/v1`.

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

**Buffer/lock** — desktop uses a buffer with draft autosave and a swap file.
Phone autosaves directly plus the crash pad. Locks are TTL'd; `If-Match` is the
correctness backstop.

**Crash pad** — IndexedDB, tab-scoped, stores base sha, cleared on save.

**Bulk ops** — rename/move is a journaled transaction; with ULIDs no link
rewriting is needed. Imports use bulk mode with batched commits.

## 5. Read path

Scanner with mtime as truth and fsnotify as a hint, walking `vault/` only.
One scanner feeds FTS, refs, and identity. Caches: LRU, per-notebook disk,
content-hash keys, singleflight, ETags.

## 6. Resources

Pure `Policy` → `Pipeline` → `ResourceStore` / `Transformer`. `LocalStore` in
v1. Upload journal with resume, verification, dedup, GC with grace. `vips` for
stills, `ffmpeg` for video posters.

## 7. Dedup

Media: exact by `blob_sha`, notebook-scoped. Text: SimHash, detect-don't-merge.
Canonicalization is explicit and keeps originals in git.

## 8. Editor

Two modes. Desktop vim mode in a sandboxed iframe (wasi-vim; nvim fallback),
source view, circuit breaker with auto-fallback. Phone normal mode in the
shell. Textarea last resort. Buffer mirror in the shell.

## 9. Extensibility

Shared substrate: manifest, permissions, per-notebook event bus (coalesced,
actor-suppressed, depth-limited), registry, scheduler, secrets, kv, audit.

## 10. Error handling

Failure matrix with classification. Atomic writes, reconcile on boot,
per-notebook circuit breakers, quarantine, trash, disk budget, `state.db`
rebuild via the rebuildability guard.

## 11. Backups

Per notebook: git bundle, restic, `VACUUM INTO`. GFS retention, verification
each run, weekly restore drill. Keys stored separately. v1 backs up locally.

## 12–14. Import/export, security, operations

Streaming, staged, journaled, idempotent, per-item isolation, dry-run. Strict
sanitizer, CSP, CSRF, sandbox, allowlist, audit. Structured logs, diagnostics,
health page, alerts, forward-only migrations.
