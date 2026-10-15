# Stage 3 — write path

Design for the first mutations: create/update notes, delete to trash, editing
locks, git autocommit, CSRF posture, and the phone editor with crash pad.
Nothing here changes the read path except `ETag`/`Last-Modified` headers.

Status: complete. All 8 slices built, tested, and reviewed.

Review fixes (post-slice audit):
- `git.Flush` no longer discards pending ops on error — failed commits now
  restore ops to pending and re-arm the timer for automatic retry.
- Crash pad restore offered from any tab (not just the writing tab) so
  recovery works after a browser crash wipes sessionStorage.
- Editor post-await guards prevent state mutations after `closeEditor`.
- Force-save reads live textarea value instead of the captured snapshot.
- Lock indicator reset on editor open to avoid stale text.

Deviations from the plan, with reasons:
- Autosave uses `If-Match` like manual save (plan said unconditional):
  silent overwrite of committed work is worse than an occasional 412 banner,
  and the banner + crash pad already handle that case.
- Crash pad uses `localStorage` with quota guard instead of IndexedDB:
  synchronous ~15 lines vs an async wrapper with real bug surface and no JS
  harness to catch it; adequate for a pad, upgrade path open.
- Pad restore is offered from any tab, not just the writing tab: the whole
  point of a crash pad is recovery after the original tab (and its
  sessionStorage id) is gone. The pad still records its tab for provenance.

## 0. What exists today

- `vault.Read` + `vault.Rel` (validation, NFC, traversal guards). No write code.
- `state.db` schema already has a `locks(path, holder, acquired_at, expires_at)`
  table; nothing uses it yet.
- Layout already has `.notebook/drafts/`, `.notebook/trash/`, `vault/` (a git
  repo root — `new-notebook.sh` runs `git init`).
- Auth is header trust (`X-Auth-User` set by Traefik, stripped from clients).
  No app cookie, no session, no CSRF token. `keyUser` is in the request
  context but nothing reads it yet — writes will use it as the actor tag.
- `index.db` is disposable; writes must invalidate it (see §9).

## 1. Goal and non-goals

Goal: one user can create, edit, and delete notes from the browser without
ever losing content to a crash, a race, or a bug — per the threat model
(accidental error, our own bugs).

Non-goals for this stage: vim mode (stage 4), resources/uploads (stage 5),
rename/move bulk ops (journaled transactions come with stage 8 bulk work;
single-note create/update/delete only), conflict auto-merge (last-writer-wins
is rejected; conflicts become conflict copies, never merges).

## 2. Write protocol

One endpoint for both create and update; the preconditions distinguish them.

```
PUT /n/{slug}/note?path=<rel>&force=0|1
If-Match: <sha256-hex> | *
Content-Type: text/markdown; charset=utf-8
Body: full note bytes (frontmatter included)
```

- `If-Match: <sha>` — the sha256 of the file content the edit is based on
  (the client learns it from the `ETag` on `GET note`/`raw`, §10). Mismatch
  → `412 Precondition Failed` with `{"error", "currentSha"}`.
- `If-Match: *` — require existence (update-only, no base check).
- Absent `If-Match` — update-only, no base check (phone autosave uses this;
  the crash pad in §8 is its safety net, not the server).
- Create = `PUT` to a path that does not exist. To avoid resurrecting a
  trashed note by accident, create requires `If-None-Match: *`; writing to
  an existing path with it → `412`.
- `force=1` with a stale base → write wins anyway, response includes
  `"externalChange": true`, and the server logs `note.external_change`
  (actor, path, loser sha). No event bus yet — the field on the response is
  the contract; the stage-9 bus will carry the same payload later.
- Success → `200` (update) or `201` (create) with `{"path", "sha", "mtime"}`.

Why full-content PUT, no patches: notes are small (8 MiB cap, same as reads),
full replacement makes atomicity trivial and `If-Match` exact. Operational
transform is a non-goal forever.

## 3. `vault.Write`

```go
type WriteOpts struct {
    BaseSha string // "" = no precondition; "*" covered by If-Match/If-None-Match mapping
    Actor   string // from request context, recorded for git blame line
}

func (v *Vault) Write(rel string, content []byte, opts WriteOpts) (Result, error)
```

Steps, in order, all inside a per-path mutex (`sync.Map[string]*sync.Mutex`
or a striped lock; one notebook = one process, so in-memory is sufficient):

1. `Rel(rel)` — same validation as reads. Failure → caller maps 400/403.
2. Size check: `len(content) > MaxReadBytes` → `ErrTooLarge` (413).
3. If `BaseSha != ""`: sha256 the current file (missing file = empty-string
   sha... define: missing file hashes as `""`, never matches a real sha) and
   compare. Mismatch → `ErrConflict` (412). `ErrConflict` is a new sentinel
   next to `ErrNotFound`/`ErrEscapes`.
4. MkdirAll the parent (vault allows nested folders; depth cap enforced by
   `Rel`... it currently doesn't cap — add `maxDepth = 5` to `Rel` in this
   stage, architecture §3 wants a 3–5 cap. New unit tests.).
5. Write temp file in the **same directory** (`.<base>.tmp.<rand>`), `Write`,
   `Sync` (fsync file), `Close`, `Rename` over target, `Sync` the directory
   fd. Any failure → remove temp, return error. No partial file may ever look
   valid (principle 2).
6. Return `{Sha, Size, ModTime}`.

Errors: `ErrConflict`, `ErrTooLarge`, plus the existing `ErrNotFound`
(missing vault root), `ErrEscapes`, and validation errors. `os` errors from
fsync/rename propagate raw → 500. Temp files never live in `vault/` under a
`.md` name, so the tree walk and index never see them; a crashed temp is
garbage-collected on boot by deleting `.*.tmp.*` in `vault/` (stage the
cleanup into server start, one function, tested).

Frontmatter `id` on create: if the body has no `id`, the server mints a
ULID+checksum and prepends it (architecture §3: assigned at creation). If a
malformed `id` is present, keep `Parse` semantics — never refuse the write
for frontmatter reasons; record `frontmatterError` in the response like the
read path does.

## 4. Locks: advisory, TTL'd, never load-bearing

`If-Match` is the correctness backstop; locks are UX (show "locked by this
browser tab" and reduce 412 surprises). Table already exists — use it as is.

```
PUT    /n/{slug}/lock?path=<rel>&ttl=300   → 200 {"holder","expiresAt"} | 409 {"holder","expiresAt"}
DELETE /n/{slug}/lock?path=<rel>&holder=<id> → 200 | 404 (no lock / not yours)
```

- `holder` = client-generated tab id (`crypto.randomUUID`, stored in
  `sessionStorage`). Server never invents holders.
- Default TTL 5 min, max 30 min. Refresh = re-PUT (upsert when holder
  matches, 409 otherwise). Expired rows are lazily treated as absent and
  opportunistically deleted.
- Taking a lock on a missing note → 404. Locks do not create notes.
- A lock never blocks a write: `PUT note` ignores locks entirely. This is
  deliberate — a crashed tab must not wedge the vault; expiry plus `If-Match`
  covers the realistic cases.
- Cleanup: expired rows deleted on each lock write (one `DELETE WHERE
  expires_at < now` in the same transaction).

## 5. Trash, not unlink

```
DELETE /n/{slug}/note?path=<rel>          → 200 {"trashedAs"} (requires If-Match, same 412 rules)
GET    /n/{slug}/trash                    → list, newest first
POST   /n/{slug}/trash/restore?name=<id>  → 200 | 404 | 409 (target reoccupied)
```

- Delete = `rename` into `.notebook/trash/<unixnano>-<basename>.md` plus a
  sidecar `<same>.json` with `{origPath, deletedAt, actor, sha}`. Rename is
  atomic on one filesystem; trash lives on the same mount by construction.
- Restore moves back; if the original path is reoccupied → `409`, client
  picks a new path (no auto-rename, no merge).
- Retention: 30 days, purged on boot (`deletedAt` older → unlink). Purge
  failures are logged, never fatal.
- Trash is per notebook (isolation units stay isolated). `vault.Tree` and the
  index never walk `.notebook/`, so trashed notes vanish from UI and search
  immediately.
- Directories: deleting a path prefix moves each contained note individually
  (one sidecar per note). No recursive `rename` of a dir — keeps restore
  one-note-at-a-time and the journal trivially auditable.

## 6. Git autocommit, coalesced

`vault/` is already a repo. After every successful write/delete/restore, the
server marks the notebook dirty; a per-notebook flusher commits at most once
per 60 s window (first write starts the timer), message:

```
noteroot: <actor> <op> <path> [+N more]
```

- `exec git` binary with a 30 s timeout, serialized per notebook (same stripe
  as writes, plus a commit mutex). No new dependency; `git` is already
  required by the setup script.
- Identity travels on the command line (`-c user.name/email=noteroot`),
  not from repo-local config: autocommits work in repos the operator never
  configured, and an operator's manual commits keep their own identity
  instead of being misattributed. `new-notebook.sh` intentionally sets no
  git identity.
- Commit failure (no user.email configured, lock file, disk full) is logged
  and the pending ops are restored and retried on the next window — **it
  never fails the HTTP write**. The write is truth; git is recovery.
- UI surfaces "uncommitted changes" count? Deferred — keep the API surface
  minimal; logs suffice for one user.

## 7. CSRF and session posture

No app session cookie is introduced in this stage, and none is needed:

- Ambient authority analysis: browsers send no noteroot credential today.
  Forward-auth sessions live between browser↔Traefik/IdP, and Traefik strips
  client-supplied `X-Auth-User`. A cross-site attacker therefore cannot mint
  the trusted header — but *can* ride the victim's Traefik session with a
  top-level form POST, which Traefik would duly forward with a valid header.
- Defenses, all server-side, all tested:
  1. Mutations require `PUT`/`POST`/`DELETE` — never `GET`.
  2. Mutating handlers require either `Content-Type: application/json`
     (or `text/markdown`) **and** reject `multipart/form-data` /
     `application/x-www-form-urlencoded` — a cross-origin form cannot send
     those without a CORS preflight, which we never grant (no
     `Access-Control-Allow-Origin` anywhere, ever).
  3. `Origin`/`Referer` allowlist check on mutations when the header is
     present: must match the configured public origin (new env
     `NOTEROOT_PUBLIC_ORIGIN`, empty = same-host check against `Host`).
     Missing header + non-form content type = fetch/API client → allow.
  4. No `Access-Control-Allow-*` headers, no JSONP, no cross-origin embeds.
- If a login page ever replaces forward-auth, that change *must* introduce
  `SameSite=Lax` (or Strict) session cookies plus per-session CSRF tokens on
  mutations. Recorded here so the requirement isn't rediscovered later.
- `/healthz`, `/readyz` stay unauthenticated and mutation-free.

## 8. Phone editor and crash pad

Desktop vim arrives in stage 4; stage 3 ships the plain editor used by
phones (and as the no-WASM fallback later):

- `<textarea>` bound to `GET raw`, debounced autosave (2 s idle) via
  `PUT note` **with** `If-Match` (deviation: silent overwrite of committed
  work is worse than an occasional 412 banner). `If-None-Match` only for
  creates.
- Crash pad (`localStorage`, quota-guarded — deviation from the original
  IndexedDB plan; synchronous ~15 lines, no async bug surface):
  `{tab, baseSha, content, savedAt}`, keyed per notebook+path, written on
  every keystroke batch (throttled ~500 ms), **cleared only on HTTP
  200/201**. Restorable from any tab — the sessionStorage tab id is
  recorded for provenance but does not gate restore, because the whole
  point is recovery after the original tab is gone.
- On load: if a pad entry exists and pad content != server content →
  banner with Restore / Discard. Only 200/201 clears the pad, so anything
  in the pad is by definition unsaved-or-unknown.
- Conflict path: 412 → editor keeps the typed content, shows
  "Reload (lose mine) / Force save (keep mine, marks externalChange)".
  Force-save reads the live textarea at click time, not the snapshot from
  when the conflict was detected. Never auto-merge, never silently
  overwrite.

## 9. Index invalidation

Writes call `idx.Update` inline after a successful write (same handler, after
rename, before git mark-dirty). Rationale: single user, millisecond cost,
search never stale. If `Update` fails, the write still succeeds (200/201)
and the failure is logged — the next search request re-runs `Update` anyway
(handlers already update-on-read), so staleness self-heals. No new API.

## 10. Read-path retrofit (prerequisite slice)

`GET note` and `GET raw` gain `ETag: "<sha256-hex>"` and
`Last-Modified` headers (sha over exact response bytes; cheap: one hash of
the already-read buffer). Editors need the base sha from somewhere — this is
it. `HEAD` support falls out of `net/http` for free. Small, tested, lands
first.

## 11. Failure matrix

| Failure | Class | Response |
|---|---|---|
| Bad path, bad limit, missing query/body | client | 400 |
| Unknown notebook / note | client | 404 |
| Traversal, reserved names | client | 400/403 (existing `statusFor`) |
| `If-Match` mismatch, create-on-existing | client | 412 + current sha |
| Oversize body (>8 MiB) | client | 413 (existing `ErrTooLarge`) |
| Lock held by other tab | client | 409 + holder/expiry |
| Restore target reoccupied | client | 409 |
| fsync/rename/IO error, index.db unusable | transport/data | 500, temp removed, nothing half-written |
| Git commit failure | data (aux) | logged + retried; HTTP already succeeded |
| Trash purge failure | data (aux) | logged; next boot retries |

## 12. Build slices (in order)

1. **Read retrofit**: `ETag`/`Last-Modified` on `GET note`/`raw` + tests.
2. **`vault.Write` + `ErrConflict` + `Rel` depth cap** (+ `vault` tests:
   atomicity, preconditions, traversal, oversize, id minting).
3. **`PUT /n/{slug}/note`** (+ handler tests: 200/201/400/404/412/413,
   force+externalChange; index reflects the write).
4. **Trash**: delete/restore/list/purge (+ tests; boot purge wired into
   server start).
5. **Locks** endpoints using the existing table (+ TTL/expiry tests).
6. **Git autocommit** flusher (+ tests with a temp repo; failure-path test
   asserts the write still succeeds).
7. **CSRF middleware** (content-type gate + origin check + tests) applied to
   mutating routes only.
8. **Phone editor + crash pad** (textarea UI, autosave, crash pad,
   412 banner/force flow). Manual browser verification + a JS smoke checklist
   in the doc footer, since there is no JS harness.

Each slice keeps `make check` green; no slice changes read-path behavior
except slice 1's additive headers.

## 13. Open questions (decisions needed during build, not now)

- Lock TTL default: 5 min chosen; revisit if phone autosave lock-churn shows
  up in logs.
- Trash retention 30 days vs size-based cap — revisit when quota (stage 8)
  forces the issue.
- Whether `PUT` should also accept `application/json` `{content}` envelope
  for non-text clients — defer until an integration needs it.
