# noteroot

Plain markdown notes on your own server. Real vim. Nothing you can't `grep`.

`noteroot` is a self-hosted notebook for one person who wants their notes to be *files* — readable, portable, versioned, and never locked inside a database you can't open.

> **Status:** stage 9 complete — plugins and integrations on a Lua sandbox (scoped tokens, signed webhooks, depth-limited bus, three proof plugins). Stages 0–8 before it: vault, search, write path, vim mode, resources, transforms, S3, dedup/quota/backups/import-export.

---

## Table of contents

- [What it is](#what-it-is)
- [What it is not](#what-it-is-not)
- [Design principles](#design-principles)
- [Status](#status)
- [Features](#features)
- [Architecture](#architecture)
- [Data layout](#data-layout)
- [Tech stack](#tech-stack)
- [Quick start](#quick-start)
- [Development](#development)
- [Configuration](#configuration)
- [Extensibility](#extensibility)
- [Roadmap](#roadmap)
- [Non-goals](#non-goals)
- [Decisions](#decisions)
- [License](#license)

---

## What it is

A single-user, self-hosted notebook built around a few uncompromising ideas:

- **Your notes are plain `.md` files.** Not rows, not blocks, not a proprietary format. You can `cat`, `grep`, `rsync`, and `git` them without the app.
- **Real vim, not a vim emulator.** Editing happens in an actual Vim binary running in your browser, so motions, text objects, registers, macros, and `.` repeat behave the way they should.
- **Notebooks are isolation units.** A corrupt index in one notebook cannot damage another. Separate vaults, separate repos, separate databases, separate failure domains.
- **Nothing important is unrecoverable.** Atomic writes, git history, trash, and a supported rebuild path for every derived database.
- **It is a platform, not just an app.** A small frozen core with plugins and integrations on top.

## What it is not

- Not multi-user. No sharing, no ACLs, no permissions between people.
- Not a Notion clone. No block editor, no databases-in-notes.
- Not real-time collaborative. No CRDT, no live cursors.
- Not a mobile app. The web UI is responsive; there is no native client.
- Not a hosted service. You run it.

## Design principles

1. Plain markdown files are the source of truth. Everything else is derived or cached.
2. No failure may produce a partial file that looks valid.
3. Classify failures before responding: client → transport → data.
4. If a feature can live in an extension, it should. The core stays small and frozen.
5. Isolation is enforced by boundaries, not convention.
6. No database holds primary data. Every row is reconstructible from disk, git, or object storage.

## Status

| Stage | Scope | State |
|-------|-------|-------|
| 0 | Skeleton, layout, registry, health, auth | ✓ done |
| 1 | Vault read path, markdown rendering, read-only UI | ✓ done |
| 2 | Full-text index, search | ✓ done |
| 3 | Write path, buffer/lock, crash pad, phone editor | ✓ done |
| 4 | Vim mode (vim.wasm running in-page) | ✓ done |
| 5 | Resources: policy, local store, upload journal | ✓ done |
| 6 | Transforms: image resize, video posters | ✓ done |
| 7 | Remote object storage (S3-compatible, presigned URLs) | ✓ done |
| 8 | Dedup, bulk ops, quota, backups, import/export | ✓ done |
| 9 | Plugins and integrations | ✓ done |
| UI | Workspace UI: note/folder CRUD, collapsible sidebar, status bar, themes, responsive layout | ✓ done |

## Features

### Working today

- Notebook registry discovered from disk; **one broken notebook never takes down the app**
- `/healthz` (unauthenticated) and `/readyz` (unauthenticated, returns notebook count)
- Forward-auth integration via Traefik
- Structured JSON logging, request IDs, panic recovery
- Safe path validation: traversal guards, NFC normalization, reserved-name and control-character checks, cross-platform filename rules
- Vault read path: tree walk, note read with frontmatter parse, raw markdown, strict sanitized HTML rendering
- Web UI: notebook picker, sidebar, rendered note view
- Workspace UI ([guide](docs/ui-guide.md)): create, rename/move and delete notes **and folders** from the sidebar (right-click or the ⋯ button), a trash panel with restore, collapsible folders with `[note count]` badges and a notes/folders total at the foot of the sidebar, per-folder emoji and name color (kept in the browser), sidebar on the left, right or hidden, dark/light/auto theme, a status bar (mode, line:column, total and selected words, characters, paragraphs, reading time, file size), and a phone layout with a slide-in sidebar drawer
- Folders: `POST` / `PATCH` / `DELETE /n/{slug}/folder` create an empty folder, rename or move a folder with everything in it (identity map follows), or move its notes to the trash and remove it. Session surface only — not exposed on the token API
- Per-notebook FTS5 search index with incremental updates, ranked snippets, and a search box in the UI
- Write path: atomic `PUT` with `If-Match`/`If-None-Match` (412 + force flow), soft-delete trash with restore, advisory TTL locks, coalesced git autocommit, CSRF-gated mutations, phone editor with autosave and crash pad
- Embedded, forward-only, idempotent migrations, run automatically at open
- Corrupt databases quarantined rather than repaired
- Graceful shutdown with a drain timeout
- Vim mode: real Vim (WASM) running in-page, with circuit-breaker fallback to a plain textarea; buffer model with draft autosave and swap files (desktop)
- Resources: drag-and-drop upload into content-addressed storage, upload journal with verification and crash recovery, dedup by SHA-256, strict size policy
- Transforms: automatic image resize and video poster frames
- S3-compatible object storage: presigned direct-to-S3 upload URLs (AWS SigV4, stdlib-only — no SDK dependency) with journal confirm, plus authenticated presigned read URLs
- Per-notebook quota (`NOTEROOT_QUOTA_BYTES`, HTTP 507 on exceed) and background GC (`NOTEROOT_GC_INTERVAL`) with a manual `POST /n/{slug}/resources/gc` trigger
- Note rename/move: `PATCH /n/{slug}/note` moves the file atomically; the ULID identity travels so links never break
- Wikilinks with backlinks: `[[target|label]]` resolves through the identity map (aliases, titles, renames); the note view shows outgoing and backlink panels
- Backups: `POST /n/{slug}/backup` writes a git bundle (`<stamp>-vault.bundle`) and a `VACUUM INTO` state snapshot (`<stamp>-state.db`) into `.notebook/backups/`; the run manifest (paths, SHAs, partial-failure notes) is the HTTP response body
- Import/export: streaming tar.gz export; staged import with dry-run, per-item isolation, and SHA-256 idempotency
- Scoped API tokens: hashed at rest, `resource:action[:qualifier]` grants, minted over the forward-auth surface, used against the bearer-authenticated `/api/v1` machine API (no CSRF — Authorization can't ride a cross-site request)
- Sandboxed Lua plugins (gopher-lua): per-notebook `.notebook/plugins/<dir>/{plugin.toml,plugin.lua}`; commands, event hooks, kv, panels, frontmatter helpers; permission-checked capabilities, per-plugin VM behind one mutex, context timeouts, panic recovery, circuit breaker; a slow plugin delays itself and nothing else
- Per-notebook event bus: coalesced bursts, actor/origin self-suppression, depth-limited chains — plugin and integration writes can never loop the app
- Integrations: outbound signed webhooks from `.notebook/integrations/<name>.toml` (HMAC-SHA256, breaker, redirects refused); inbound HMAC-authenticated webhooks to `/hooks/{slug}/{name}` writing notes with `hook:` attribution; secrets in platform.db, rotation without reload; delivery depth carried across the HTTP round trip so an echo terminates
- Proof plugins in `plugins/` (copy into the notebook, hit reload): frontmatter tagger, status-bar word count, theme preference — rendered by the UI's plugin panel strip

### Planned

**Notes**
- Virtual collections (folders already work)
- Folder icons/colors synced through the server instead of per-browser storage

**Search**
- Optional semantic search via embeddings
- Near-duplicate detection for notes (SimHash)

**Safety**
- Per-notebook circuit breakers (server-side)
- `state.db` rebuild that also consults object storage

**Operations**
- Encrypted offsite backups with verification and scheduled restore drills
- A global disk budget
- Resumable import/export for very large notebooks
- Diagnostics bundle and health page

## Architecture

```
Client → reverse proxy (TLS, forward-auth) → noteroot
                                            │
                    ────────────────────────────────────────────
                    │                       │                   │
                  vault/               .notebook/      object store (stage 7, optional)
              (markdown, git)     (resources, db, cache)
```

Every layer below HTTP is instantiated **per notebook**. Nothing is shared except a registry that rebuilds from disk.

Key components:

- **`vault`** — the single write path. Serialized per path, atomic, actor-tagged, `If-Match` guarded.
- **`index`** — disposable FTS and refs. Deletable; rebuilt from a full vault pass.
- **`state`** — durable but rebuildable: identity map, resources, locks.
- **`resources`** — pure policy, then pipeline, then store and transformer. No policy in the store.
- **`editor`** — swappable backend behind one interface, isolated from the shell.
- **`platform`** — the shared plugin/integration substrate (stage 9).

## Data layout

Two separate things live in two separate repositories:

- **The app repo** (`noteroot/`) — Go source. Has a `.gitignore`.
- **Each notebook** — user data on disk. Needs **no `.gitignore`**, because `vault/` contains only markdown and nothing app-managed lives inside it.

```
noteroot-data/                       # NOTEROOT_DATA_DIR
└── notebooks/
    └── work/                        # one notebook = one isolation unit
        ├── notebook.toml            # manifest
        ├── vault/                   # your notes. git repo root. markdown only.
        │   ├── index.md
        │   └── projects/
        │       └── plan.md
        └── .notebook/               # app-managed. never edit by hand.
            ├── resources/           # content-addressed blobs
            ├── cache/               # disposable
            ├── drafts/              # buffer drafts and swap
            ├── trash/               # soft deletes
            ├── backups/             # git bundles + state snapshots
            ├── plugins/             # Lua plugins (plugin.toml + plugin.lua)
            ├── integrations/        # outbound webhook configs
            ├── hooks/               # inbound webhook configs
            ├── state.db
            ├── index.db
            └── platform.db          # kv, secrets, audit, API tokens (secrets live only here)
```

Three stores per notebook (state, index, platform) plus the vault. One rule: never touch `.notebook/`.

## Tech stack

### Runtime

| Component | Version | Why |
|---|---|---|
| Go | 1.27+ | Single static binary, no runtime deps |
| SQLite | via `modernc.org/sqlite` | Pure Go, **no cgo** |
| Docker | any | Alpine image (`git` included), non-root |
| Traefik | any | TLS termination and forward-auth |

### Libraries

**Present in the code**

| Library | Purpose | Wired up |
|---|---|---|
| `modernc.org/sqlite` | Pure-Go SQLite driver (FTS5 search index) | yes |
| `github.com/BurntSushi/toml` | `notebook.toml` parsing | yes |
| `github.com/yuin/goldmark` | Markdown rendering (GFM + footnotes) | yes |
| `github.com/microcosm-cc/bluemonday` | HTML sanitization | yes |
| `gopkg.in/yaml.v3` | Frontmatter YAML parsing | yes |
| `golang.org/x/text/unicode/norm` | NFC path normalization | yes |
| `net/http` (stdlib) | Router, using 1.22+ method patterns | yes |
| `log/slog` (stdlib) | Structured logging | yes |
| ULID identity (stdlib) | `crypto/rand` + CRC32 checksum, homegrown in `frontmatter` | yes |
| `ffmpeg` / `ffprobe` (external binary) | Image resize and video poster frames | yes (optional at runtime) |
| vim.wasm + xterm.js (vendored JS) | Real Vim running in-page | yes |
| `yuin/gopher-lua` | Sandboxed Lua plugin runtime (stage 9) | yes |

**Planned**

| Library / tool | Purpose |
|---|---|
| `fsnotify/fsnotify` | Vault watching (hint only; mtime scan is truth) |
| `golang.org/x/sync/singleflight` | Cache stampede protection |
| `hashicorp/golang-lru` | In-process cache |
| `libvips` (`vipsthumbnail`) | Higher-fidelity stills than ffmpeg (EXIF/ICC/HEIC/AVIF) |
| `restic` / `rclone` | Encrypted offsite backups |

### Why these

- **Pure Go, no cgo** — cross-compiles cleanly, static binaries, trivial containers. `modernc.org/sqlite` over `mattn/go-sqlite3` specifically for this; FTS5 is enabled and in use since stage 2.
- **One external tool (`ffmpeg`) for both stills and video** — ships resize and posters with zero extra build deps; the narrow `Transformer` interface lets libvips replace it later if EXIF/ICC/HEIC/AVIF fidelity becomes a need.
- **WASM Vim over embedding Neovim** — no subprocess, no RPC, no session manager; typing latency stays local. Failures fall back to a plain textarea via the circuit breaker.

## Quick start

### Requirements

- Go 1.27+
- Docker and Docker Compose (container path only)
- A reverse proxy that sets `X-Auth-User` (forward-auth) if you expose the container beyond localhost — noteroot ships no proxy of its own

### Local

```bash
git clone git@gitlab.com:YOUR_USERNAME/noteroot.git
cd noteroot

go mod tidy
go build -o bin/noteroot ./cmd/noteroot
```

**Production-ish run** — uses your real data directory:

```bash
export NOTEROOT_DATA_DIR="$HOME/.local/share/noteroot"
./scripts/new-notebook.sh work
NOTEROOT_AUTH_MODE=none ./bin/noteroot
```

**Development run** — uses throwaway data in the project:

```bash
export NOTEROOT_DATA_DIR="$PWD/.devdata"
./scripts/new-notebook.sh work
NOTEROOT_AUTH_MODE=none ./bin/noteroot
```

Then:

```bash
curl localhost:8080/healthz
curl localhost:8080/api/v1/notebooks
```

### Container

```bash
mkdir -p data
NOTEROOT_DATA_DIR="$PWD/data" ./scripts/new-notebook.sh work   # create a notebook (host-side)
docker compose -f deploy/compose.yaml up -d --build
```

The image is Alpine with `git` (vault autocommits and backup bundles shell out to it) and `ffmpeg`/`ffprobe` (image, video and audio transforms), runs as non-root uid 1000, and publishes `127.0.0.1:8080` only. `data/` is bind-mounted to `/data` — create it with `mkdir -p data` before the first start so Docker doesn't create it root-owned. Every variable in [Configuration](#configuration) is declared as an `ENV` with its documented default, so `docker inspect` shows the whole surface; the health check follows `NOTEROOT_LISTEN` rather than a hardcoded port.

Auth defaults to `forward`: everything except `/healthz`, `/readyz`, and the static shell returns 401 until a proxy sets `X-Auth-User`. For a local look without a proxy, `NOTEROOT_AUTH_MODE=none docker compose -f deploy/compose.yaml up`. A containerized reverse proxy should attach to the `noteroot_default` network instead of going through the published port; TLS stays your proxy's job.

CI (`.github/workflows/ci.yml`) runs the Go and web suites, lints the Dockerfile, smoke-tests the image — anonymous calls must 401 under `forward`, `/healthz`, `/api/v1/notebooks` and the shell must answer under `none` — then publishes to `ghcr.io/cforqol/noteroot`, tagged by branch, semver and sha.

## Development

Development uses [direnv](https://direnv.net/). `.envrc` is committed; `.envrc.local` is not. Dev data lives in `./.devdata` and is gitignored, so a dev run can never scribble on your real notes — `make guard` requires `NOTEROOT_DATA_DIR` to be set and warns if you point at production.

```bash
direnv allow
make setup     # air, staticcheck, govulncheck, golangci-lint
make dev       # hot reload
make check     # fmt, vet, lint, test, vuln
make help      # list targets
```

## Configuration

All configuration is environment variables.

| Variable | Default | Notes |
|---|---|---|
| `NOTEROOT_DATA_DIR` | `$XDG_DATA_HOME/noteroot` | Absolute path. Holds all notebooks. |
| `NOTEROOT_LISTEN` | `:8080` | Listen address. |
| `NOTEROOT_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error`. |
| `NOTEROOT_AUTH_MODE` | `forward` | `forward` trusts `X-Auth-User`. `none` is dev only. |
| `NOTEROOT_PUBLIC_ORIGIN` | `` (same host) | Exact `scheme://host` mutations must come from. Empty means the request's own host. |
| `NOTEROOT_S3_ENDPOINT` | `` (disabled) | S3-compatible endpoint URL. S3 config is all-or-nothing: all five `NOTEROOT_S3_*` required together. |
| `NOTEROOT_S3_BUCKET` | `` | Bucket name. |
| `NOTEROOT_S3_REGION` | `` | Region (part of the SigV4 scope; any value works for MinIO). |
| `NOTEROOT_S3_ACCESS_KEY` | `` | Access key ID. |
| `NOTEROOT_S3_SECRET_KEY` | `` | Secret access key. |
| `NOTEROOT_S3_PATH_STYLE` | `false` | `true` = path-style URLs (MinIO), `false` = virtual-hosted (AWS). |
| `NOTEROOT_S3_PRESIGN_TTL` | `` (empty = 15m) | Presigned upload/read URL lifetime (Go duration, capped at 168h). |
| `NOTEROOT_QUOTA_BYTES` | `0` (unlimited) | Per-notebook storage quota in bytes; uploads past the quota fail with 507. |
| `NOTEROOT_GC_INTERVAL` | `24h` | How often the background resource GC sweep runs (Go duration). |

**Authentication.** noteroot does not implement a login page. Traefik performs forward-auth against your identity provider and sets `X-Auth-User` on success, stripping any client-supplied value. Mutations are CSRF-gated server-side (content-type gate + Origin/Referer allowlist; no CORS is ever granted, no application session cookie exists). `/healthz` and `/readyz` are deliberately **unauthenticated** so orchestrators can reach them directly.

## Extensibility

Two distinct extension points, sharing one substrate:

| | Plugins | Integrations |
|---|---|---|
| What | Code inside the app | A separate service |
| Runs | In-process, sandboxed Lua | Its own container |
| Talks via | Capabilities and events | HTTP and webhooks |
| Can touch UI | Yes | No |
| Identity | Declared permissions | Scoped API token |

**Plugins** live in `.notebook/plugins/<dir>/` with a `plugin.toml` manifest (name, version, entry, permissions) and a `plugin.lua` entry point — both git-friendly, neither containing a secret. Capabilities (`note:read`/`write`, `kv`, `ui.set_panel`, `fm.parse`/`build`) are permission-checked at call time with the same `resource:action[:qualifier]` grammar as API tokens; plugins are **read-only by default** and grouped by risk at approval. Commands are human-triggered over `POST /n/{slug}/plugins/{name}/commands/{command}`; hooks subscribe to the notebook's event bus (coalesced, actor-suppressed, depth-limited); panels land in platform.db and render in the UI's status strip. Everything runs async with a timeout and a circuit breaker, so no plugin can block a save. `POST /n/{slug}/plugins/reload` picks up an edited directory without a restart.

**Integrations** come in two halves, configured by files under `.notebook/integrations/` (outbound) and `.notebook/hooks/` (inbound). Outbound deliveries are signed POSTs (`X-Noteroot-Signature: sha256=` + HMAC) carrying the event's hop depth; inbound webhooks verify the same signature (GitHub's `X-Hub-Signature-256` works too) and write through the real write path with `hook:` attribution, continuing the chain one hop deeper — a service that echoes deliveries back terminates at the depth limit. Secrets live in platform.db, set over the authenticated API, rotated in place.

The three proof plugins — frontmatter **tagger**, status-bar **word count**, **theme** — ship in the repo's `plugins/` directory: copy one into a notebook, reload, done.

## Roadmap

Stages 0–8 are the app. Stage 9 is a menu, not a queue.

- **1** — vault walk, markdown rendering, sanitizer, read-only UI
- **2** — FTS5 index and search
- **3** — write path, buffer/lock, crash pad, phone editor, git autocommit
- **4** — vim mode running in-page, with circuit-breaker failover
- **5** — resources: policy, local store, upload journal
- **6** — transforms: image resize, video posters
- **7** — object storage with presigned upload and authenticated reads
- **8** — dedup, rename/move, quota, backups, import/export (restore drills deferred)
- **9** — plugins and integrations ✓ shipped (Lua sandbox, tokens, webhooks, bus)

### Deferred

Everything below is deliberately out of scope until the core is good:

- End-to-end encryption — plaintext plus disk and backup encryption covers a self-hosted threat model, and `zk` would roughly double the code paths
- Note sharing and a public wiki view
- AI features, embeddings, and vector search
- Local-first replication — a crash pad covers the realistic loss cases
- Plugin updates and a marketplace

## Non-goals

- Multi-user and per-user isolation
- Real-time collaborative editing
- A block-based editor
- Native desktop or mobile clients
- Absorbing other tools — Karakeep stays Karakeep, it just syncs in

## Decisions

Architecture decisions live in `docs/adr/`:

- `0001` — no local-first
- `0002` — `state.db` is rebuildable
- `0003` — local storage and the consolidated app directory

Full reference: `docs/architecture.md`. Also `docs/threat-model.md` and `docs/schema/frontmatter.json`.

## Contributing

Single-maintainer project, but issues and patches are welcome. Before opening a PR:

```bash
make check
```

Expect strict review on two things in particular: **atomic writes** and **failure containment**. Those are the properties the whole design rests on.

## License

MIT + Commons Clause — see [LICENSE](LICENSE).
