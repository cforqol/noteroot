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

**Container** — `Dockerfile` (two-stage: `golang:1.27-alpine` build, runtime
`alpine:3.22` + `git`, non-root uid 1000) and `deploy/compose.yaml` (single
service, `127.0.0.1:8080`, `data/` → `/data`). `git` ships in the image
because vault autocommit and backup bundles shell out to it — that is why
the image is Alpine rather than distroless. The compose file publishes
localhost only and bundles no reverse proxy; a proxy terminates TLS and
sets `X-Auth-User` outside it (or attach to the compose network).

## 2. Auth

Forward-auth sets `X-Auth-User`; Traefik strips client-supplied values.
`/healthz` and `/readyz` are unauthenticated. CSRF is enforced on mutating
routes: form content types rejected, Origin/Referer allowlist checked, no
CORS granted, no application session cookie. Scoped API tokens on
`/api/v1`: SHA-256-hashed at rest, plaintext shown once, `resource:action
[:qualifier]` grants, revocable; bearer middleware is the only auth there
(no forward-auth, no CSRF — Authorization cannot ride a cross-site
request), and `X-Auth-User` is not a credential on those routes.

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

Shipped substrate. Every notebook gets `.notebook/platform.db`
(kv, secrets, audit, api_tokens, ui panels) via the same
quarantine-and-rebuild open as state.db, and a bus that coalesces bursts
(500 ms), suppresses self-delivery by actor/origin, and drops chains past
depth 5. Mutations publish through `emitAs(ctx, actor, origin, depth)`;
plugin and hook writes reuse the real write path (identity, index, git,
audit, bus) with their own attribution.

**Plugins** — `.notebook/plugins/<dir>/{plugin.toml,plugin.lua}`.
Manifest validation is strict (name must match the directory, entry
cannot escape, permissions via the shared grant grammar); a bad or
disabled plugin is listed with its problem and never fails boot. One
gopher-lua VM per plugin behind one mutex; the bus only enqueues
(bounded, drop-on-full). Every invocation (entry, hook, command) takes
the same path: context deadline (load 10 s, command/hook 5 s), panic
recovery, teardown-and-rebuild on timeout, consecutive-failure breaker.
Capabilities (`note`, `kv`, `ui.set_panel`, `fm`, `register_command`,
`on`) check grants at call time; qualifiers are path/key/panel globs.
Panels land in `ui.panels` kv and render in the frontend status strip.

**Tokens** — `api_tokens` table, `nrk_` prefix, hashed. Bearer middleware
maps a scope miss to 403 and an unknown/revoked hash to the same 401;
corrupt stored scopes fail closed.

**Integrations** — outbound configs in `.notebook/integrations/*.toml`
(url, events, enabled), secrets in platform.db set over the API and
rotated in place. Deliveries are signed POSTs with the event's depth in
`X-Noteroot-Depth`; redirects are failures. Inbound hooks in
`.notebook/hooks/*.toml` (path, mode) receive `POST /hooks/{slug}/{name}`
authenticated by HMAC over the raw body (GitHub-compatible header
accepted), write with `hook:` attribution at depth+1, and reject a
delivery already at the bus limit with 409 — an echoing service
terminates in a handful of hops.

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
iframe sandbox (ship). Every mutation is recorded in platform.db's audit
trail (actor, action, resource, detail) — HTTP writes say who, plugin and
hook writes say which extension. CSP is planned. Structured
logs and forward-only migrations ship. Stage 9 shipped: plugin substrate,
scoped API tokens, integrations. Also planned: diagnostics bundle,
health page, alerts, remote backup targets.
