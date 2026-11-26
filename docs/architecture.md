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

**Folders** — folders are real directories, so `Tree()` lists empty ones
too. `POST /n/{slug}/folder` is `vault.Mkdir` (parents created, hidden or
`*.md` names refused); `PATCH` is `vault.RenameDir`, one atomic directory
rename that refuses to move a folder into itself or onto an existing path,
followed by `RenameIdentityPrefix` so every note's uid row follows; `DELETE`
sends each note to the trash through the existing `Trash.Put` directory path
(restorable one by one), then `vault.RemoveDir` prunes the now-empty
skeleton. A folder that still holds a file (even an ignored one) is left in
place — nothing is deleted silently. Folder routes are on the session
(forward-auth) surface only: bulk moves/deletes are not offered to path-scoped
tokens, whose qualifier is a single path. Each notebook has a trash‑retention
setting (default 30 days, never = 0) stored in `notebook.toml`; the GC sweep
removes entries past their expiry.

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
every upload entry point (direct, presign, and server-side URL fetch) with
HTTP 507. Stats expose `usedBytes`/`quotaBytes`; usage is dedup-aware
(content-addressed bytes counted once).

**Import attachment fetch** — `POST /n/{slug}/resources/fetch` (#107) lets
plain-markdown note import resolve an attachment that is only an external
URL in the source, not a bundled file: the server fetches it and runs it
through the same `Policy.Validate` → quota reserve → `store.Put` → journal
commit pipeline as a direct upload, rejecting the result on a short read
or a declared/actual size mismatch rather than storing a truncated blob.
This is the only outbound request in the codebase whose target comes from
note content rather than admin config (Karakeep/Slink/S3/webhooks all dial
addresses an operator typed into config), so it runs through a dedicated
SSRF guard (`guardedFetchClient`) instead of the shared `transport`: a
`net.Dialer.Control` hook checks the DNS-resolved IP — not the hostname —
immediately before `connect(2)`, rejecting loopback/private/link-local
(including the `169.254.169.254` cloud metadata address)/unspecified/
multicast ranges, and redirects are never followed. See
[threat-model.md](threat-model.md).

**Global quota** — `NOTEROOT_GLOBAL_QUOTA_BYTES` (0 = unlimited) is a
second ceiling on top of (not instead of) each notebook's own quota: the
same committed + in-flight-reservation bytes `NOTEROOT_QUOTA_BYTES` tracks
per notebook, summed across every notebook's own upload journal. Enforced
at the same two entry points — the atomic reserve-then-begin check (now
serialized across all notebooks via the shared `uploadMu`, since two
different notebooks can race against one global ceiling) and the advisory
presign check — returning the same `ErrQuotaExceeded`/507. A notebook
whose state DB can't be opened when summing is skipped and logged rather
than failing the request, same best-effort posture as the per-notebook
check. Stats expose `globalUsedBytes`/`globalReservedBytes`/
`globalQuotaBytes` when the global quota is enabled. Scope: this is a
resource/blob storage budget, not a filesystem total — it does not count
vault markdown, git history, state/index/platform DB files, or backups.

**GC** — background sweep every `NOTEROOT_GC_INTERVAL` (default 24h) plus a
manual trigger `POST /n/{slug}/resources/gc`; deletes unreferenced blobs
past their grace period and returns removal counts.

**Verify** — `GET /n/{slug}/resources/{sha}/verify` streams the stored blob
through SHA-256 and compares the result to `sha` (the content-addressed
store key), returning `{sha,status,size}` with `status` one of
`ok`/`missing`/`corrupted`. Catches truncation and bit-rot that a bare
existence check (the journal, or a 404 on `GET .../{sha}`) would miss. The
attachment manager calls this once per committed attachment, sequentially,
rather than trusting the journal alone; it does not run automatically.

**Rebuild reconciliation (S3)** — `state.Open`'s rebuild of a corrupted
`state.db` (ADR 0002) wipes the upload journal, the only local record of
which blobs a notebook owns. When the resource store is S3-backed,
`openResources` detects a rebuild just happened and runs
`reconcileAfterRebuild`: scan the notebook's own vault markdown for
`/n/{slug}/resources/{sha}` links (`referencedSHAs`), and for each
referenced sha not already live in the journal, `HEAD` it against the
bucket (`S3Store.Size`) and re-adopt it as a committed `upload_journal` row
(`Journal.AdoptRecovered`) if the bucket still has it — otherwise a blob
confirmed via presigned upload before the rebuild would look orphaned and
be swept by the next GC pass despite still being linked from a note.
Deliberately not a bucket-wide listing: every notebook shares one S3
bucket via server-wide config, so a listing can't attribute an object to
one notebook — the vault's own note links are the ownership signal
instead. Runs at most meaningfully once per `state.db` rebuild.

## 7. Dedup

Media: exact by `blob_sha`, notebook-scoped. Text: SimHash,
detect-don't-merge (planned). Canonicalization is explicit and keeps
originals in git.

## 8. Editor

Two modes. Desktop vim mode runs vim.wasm in-page (xterm.js;
textarea fallback via circuit breaker), source view (stage 4). Phone/shell
mode is a plain textarea with debounced autosave, crash pad, and advisory
locks (shipped in stage 3).

### Frontend structure

A framework-free ES-module SPA embedded in the binary (`web/embed.go`).
`web/app.js` is the DOM glue; everything with logic lives in `web/lib/` and
is held to 100 % Jest coverage:

| Module | Role |
|---|---|
| `lib/stats.js` | word/char/paragraph/reading-time counts, row:col, human sizes, `describeStatus` |
| `lib/tree.js` | counts per folder, tree status text, path validation (`cleanRelPath`, `resolveChild`) |
| `lib/tree-view.js` | collapsible tree renderer: icons, colors, `[n]` badges, row actions, drag-and-drop note → folder |
| `lib/prefs.js` | browser-local theme, tree placement, collapsed state |
| `lib/notes-api.js` | note/folder/trash HTTP client (CSRF-safe JSON types), plus the server-side per-folder emoji/color (`getFolderPrefs`/`setFolderPref`) |
| `lib/dialog.js` | promise-based form/confirm dialogs and context menu (no `window.prompt`) |
| `lib/ui-plugins.js` | built-in UI plugin registry: per-notebook localStorage enable state, `syncUiPlugins` / `destroyAllUiPlugins` lifecycle |
| `lib/plugins/html-renderer.js` | HTML renderer plugin: replaces ` ```html ` fenced blocks with sandboxed iframes on each `noteread` event |
| `lib/plugins/calendar.js` | Calendar widget plugin: floating draggable monthly calendar toggled from the top bar |
| `lib/plugins/pomodoro.js` | Pomodoro timer plugin: floating three-row timer (phase buttons / SVG ring countdown / Start-Reset), configurable sound + widget-flash notifications, background running indicator on the top-bar button; lengths and notification flags read from / written to the plugin preference store |
| `lib/upload.js` | Upload helpers: presigned direct-to-S3 request/confirm flow, video-resource extraction from rendered HTML, and recovery of the real filename for images pasted/dragged from a web page (`resolveAttachmentFilename`, from the paste/drop HTML's `<img src>`) when the browser only hands over a generic placeholder name |
| `lib/import-attachments.js` | Plain-markdown import attachment resolution (#107): finds markdown/`<img>` references in imported note text, classifies each as an already-resolved resource link, an external URL, a local companion-file path, or unresolvable, and rewrites a resolved reference to the new `/n/{slug}/resources/{sha}` link |

Most preferences are per browser (`localStorage`), never server state — the
exception is per-folder emoji/color, which lives in `.notebook/folder-prefs.json`
(notebook-scoped, not `state.db` — see ADR 0002), so it's consistent across
browsers. Layout is
CSS-only: `body[data-tree=left|right|hidden]` selects the grid, and below
720 px the tree becomes an off-canvas drawer (`body.tree-open`). The status
bar reads the textarea live; vim.wasm exposes no buffer, cursor or selection
API, so Vim mode shows the last synced buffer (open and each save) and no
row:col. See [ui-guide.md](ui-guide.md).

## 9. Extensibility

Shipped substrate. Every notebook gets `.notebook/platform.db`
(kv, secrets, audit, api_tokens, ui panels) via the same
quarantine-and-rebuild open as state.db, and a bus that coalesces bursts
(500 ms), suppresses self-delivery by actor/origin, and drops chains past
depth 5. Mutations publish through `emitAs(ctx, actor, origin, depth)`;
plugin and hook writes reuse the real write path (identity, index, git,
audit, bus) with their own attribution.

**Built-in UI plugins** — a lightweight client-side plugin system parallel to the Lua layer.
Each plugin is a plain ES-module object `{id, name, description, init(slug), destroy()}`.
`lib/ui-plugins.js` keeps two kinds of `localStorage` state: enabled state per
notebook (`noteroot_uiplugins_<slug>`) and plugin preferences per user
(`noteroot_uipluginprefs`, one object keyed by plugin id), and `syncUiPlugins` /
`destroyAllUiPlugins` are called from `loadTree` on notebook open/switch. Three
first-party plugins ship: HTML renderer (sandboxed iframe for ` ```html ` blocks),
Calendar widget (floating monthly calendar), and Pomodoro timer (SVG ring timer
whose phase lengths and sound/flash notifications are editable in-widget and
persist across restarts via `getUiPluginPrefs`/`setUiPluginPrefs`). The timer
keeps ticking while its widget is hidden — the top-bar button marks that with a
`running` class, and a phase ending in that state opens a `body`-level
"Time is up" dialog instead of flashing the hidden widget.
Settings → Plugins shows both UI plugins (with per-notebook toggles) and Lua
plugins (fetched from `GET /n/{slug}/plugins`) with a *Reload* button.

**Lua plugins** — `.notebook/plugins/<dir>/{plugin.toml,plugin.lua}`.
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
per-notebook circuit breakers are shipped: `NotebookBreaker` middleware
wraps every `/n/{slug}/...`, bearer `/api/v1/n/{slug}/...`, and inbound
`/hooks/{slug}/{name}` route, tripping open after `NOTEROOT_BREAKER_THRESHOLD`
consecutive 5xx (4xx is neutral; 2xx/3xx closes) and fast-failing with
503 + `Retry-After` until a half-open probe after `NOTEROOT_BREAKER_COOLDOWN`
succeeds — the same strikes/cooldown/half-open shape as the per-plugin and
per-integration breakers (§9), kept as a separate implementation. Notebook
create/rename/delete stay exempt so a tripped notebook is still reachable
for repair.

## 11. Backups

Per notebook: git bundle + `VACUUM INTO` (v1, local). `POST /n/{slug}/backup`
writes timestamped `<stamp>-vault.bundle` and `<stamp>-state.db` (with a
`-N` suffix if two backups land in the same second) in `.notebook/backups/`.
There is no manifest file on disk — the run manifest (paths, SHAs, partial
failures in `Errors`) is the HTTP response body. Partial failures (non-git
vault, state.db absent) still return 200 with the partial manifest. Restic /
remote targets deferred to v2.

## 12. Import/export

`GET /n/{slug}/export` streams a tar.gz of all vault `.md` files and
`notebook.toml`. `POST /n/{slug}/import` accepts raw gzip or multipart
`file` field. Staged: all entries validated before any writes. Per-item
isolation: one bad entry (bad path, non-.md, escaping vault) is recorded as
`"error"` without aborting others. Idempotent by SHA-256: identical content
returns `"skipped"`. `?dry_run=true` classifies without writing.

## 13–15. Security, operations, future

Strict sanitizer, CSRF enforcement, path/Origin allowlists, and
COOP/COEP headers for SharedArrayBuffer isolation (ship). Every mutation is recorded in platform.db's audit
trail (actor, action, resource, detail) — HTTP writes say who, plugin and
hook writes say which extension. CSP is planned. Structured
logs and forward-only migrations ship. Stage 9 shipped: plugin substrate,
scoped API tokens, integrations. Diagnostics and health visibility shipped
(#49): `POST /n/{slug}/diagnostics` returns a zip (version/runtime info,
per-notebook DB file sizes, vault row count, quota usage, circuit breaker
state — §6, §10 — a full goroutine dump, and recent warn/error log lines),
gated by the same per-notebook circuit breaker as other mutating routes; a
notebook whose state DB can't currently be opened still gets a 200 with a
degraded report rather than failing. `GET /admin/health` is a
session-authenticated, not notebook-scoped, HTML page listing every
notebook's breaker status (open/closed plus strike/trip counts — half-open
is a transient one-probe window entered only by an actual request, not a
readable third state) and quota usage, plus a global quota summary line
(§6) when enabled. Also planned: alerts, remote backup targets.
