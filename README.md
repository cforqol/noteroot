# noteroot

Plain markdown notes on your own server. Real vim. Nothing you can't `grep`.

`noteroot` is a self-hosted notebook for one person who wants their notes to be *files* — readable, portable, versioned, and never locked inside a database you can't open.

> **Status:** stage 8 complete — Dedup-aware quota (507 on exceed), background GC, note rename/move (ULID travels), git bundle + VACUUM INTO backups, tar.gz export/import (staged validation, dry-run, idempotent).

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
| 4 | Vim mode (wasi-vim in a sandboxed iframe) | ✓ done |
| 5 | Resources: policy, local store, upload journal | ✓ done |
| 6 | Transforms: image resize, video posters | ✓ done |
| 7 | Remote object storage (S3-compatible, presigned URLs) | ✓ done |
| 8 | Dedup, bulk ops, quota, backups, import/export | ✓ done |
| 9 | Plugins and integrations | — |

## Features

### Working today

- Notebook registry discovered from disk; **one broken notebook never takes down the app**
- `/healthz` (unauthenticated) and `/readyz` (unauthenticated, returns notebook count)
- Forward-auth integration via Traefik
- Structured JSON logging, request IDs, panic recovery
- Safe path validation: traversal guards, NFC normalization, reserved-name and control-character checks, cross-platform filename rules
- Vault read path: tree walk, note read with frontmatter parse, raw markdown, strict sanitized HTML rendering
- Minimal read-only web UI (notebook picker, tree, rendered note)
- Per-notebook FTS5 search index with incremental updates, ranked snippets, and a search box in the UI
- Write path: atomic `PUT` with `If-Match`/`If-None-Match` (412 + force flow), soft-delete trash with restore, advisory TTL locks, coalesced git autocommit, CSRF-gated mutations, phone editor with autosave and crash pad
- Embedded, forward-only, idempotent migrations (schema only — nothing calls them yet)
- Corrupt databases quarantined rather than repaired
- Graceful shutdown with a drain timeout

### Planned

**Notes**
- Stable ULID identity — links survive renames and moves
- Wikilinks and backlinks
- Virtual collections (folders already work)

**Editor**
- Vim mode: real Vim (WASM) in an isolated iframe, with automatic fallback on failure
- Buffer model with draft autosave and swap files (desktop vim mode)

**Search**
- Optional semantic search via embeddings

**Resources**
- Drag-and-drop upload into content-addressed storage
- Local-first with a strict size policy; object storage when configured
- S3-compatible backend: presigned direct-to-S3 upload URLs (AWS SigV4, stdlib-only — no SDK dependency) with journal confirm
- Presigned read URLs for authenticated downloads
- Automatic image resize and video poster frames
- Upload journal with resume, verification, and crash recovery
- Per-notebook dedup by content hash
- Near-duplicate detection for notes (SimHash)

**Safety**
- Per-notebook circuit breakers
- Integrity checks and a boot-time rebuildability guard
- `state.db` rebuild from disk, git, and object storage

**Operations**
- Encrypted offsite backups with verification and scheduled restore drills
- Per-notebook quota and a global disk budget
- Diagnostics bundle and health page
- Streaming, resumable, idempotent import/export

**Extensibility**
- Sandboxed Lua plugins: commands, hooks, renderers, panels
- Integrations over HTTP and webhooks, with scoped tokens
- Per-notebook event bus with coalescing and loop suppression

## Architecture

```
Client → Traefik (TLS, forward-auth) → noteroot
                                            │
                    ────────────────────────────────────────────
                    │                       │                   │
                  vault/               .notebook/      object store (stage 7, optional)
              (markdown, git)     (resources, db, cache)
```

Every layer below HTTP is instantiated **per notebook**. Nothing is shared except a registry that rebuilds from disk.

Key components:

- **`vault`** — the single write path. Serialized per path, atomic, actor-tagged, `If-Match` guarded.
- **`index`** — disposable FTS and refs. Intended to be deletable; rebuilders land in stage 8.
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
            ├── state.db
            └── index.db
```

Three entries per notebook. One rule: never touch `.notebook/`.

## Tech stack

### Runtime

| Component | Version | Why |
|---|---|---|
| Go | 1.27+ | Single static binary, no runtime deps |
| SQLite | via `modernc.org/sqlite` | Pure Go, **no cgo** |
| Docker | any | Distroless image, non-root |
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

**Planned**

| Library / tool | Purpose |
|---|---|
| `oklog/ulid` | Note identity |
| `fsnotify/fsnotify` | Vault watching (hint only; mtime scan is truth) |
| `golang.org/x/sync/singleflight` | Cache stampede protection |
| `hashicorp/golang-lru` | In-process cache |
| `yuin/gopher-lua` | Plugin sandbox |
| xterm.js | Terminal rendering for vim mode |
| wasi-vim | Real Vim compiled to WebAssembly |
| `libvips` (`vipsthumbnail`) | Still-image resize, HEIC, AVIF |
| `ffmpeg` | Video posters, transcodes |
| `restic` / `rclone` | Encrypted backups |

### Why these

- **Pure Go, no cgo** — cross-compiles cleanly, static binaries, trivial containers. `modernc.org/sqlite` over `mattn/go-sqlite3` specifically for this; FTS5 is enabled and in use since stage 2.
- **`libvips` over `ffmpeg` for stills** — correct EXIF orientation, ICC profiles, HEIC, and AVIF, at far lower memory than ffmpeg's full-frame decode. ffmpeg still handles video.
- **WASM Vim over embedding Neovim** — no subprocess, no RPC, no session manager; typing latency stays local. Neovim remains the documented fallback behind the same interface.

## Quick start

### Requirements

- Go 1.27+
- Docker and Docker Compose (container path only)
- A Traefik instance with forward-auth (container path only)

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
docker compose -f deploy/compose.yaml up -d --build
```

Edit the `Host(...)` rule and the forward-auth address in `deploy/compose.yaml` first.

## Development

Development uses [direnv](https://direnv.net/). `.envrc` is committed; `.envrc.local` is not. Dev data lives in `./.devdata` and is gitignored, so a dev run can never scribble on your real notes — `make guard` enforces this and warns if you point at production.

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
| `NOTEROOT_S3_PRESIGN_TTL` | `15m` | Presigned upload/read URL lifetime (Go duration, capped at 168h). |

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

Plugins are **read-only by default**. Permissions use `resource:action[:qualifier]` and are grouped by risk at approval. Everything runs async with a timeout and a circuit breaker, so no plugin can block a save.

The plan is to prove the API with three plugins before writing a spec: a theme, a status-bar word count, and a frontmatter tagger.

## Roadmap

Stages 0–8 are the app. Stage 9 is a menu, not a queue.

- **1** — vault walk, markdown rendering, sanitizer, read-only UI
- **2** — FTS5 index and search
- **3** — write path, buffer/lock, crash pad, phone editor, git autocommit
- **4** — vim mode in an isolated iframe, with circuit-breaker failover
- **5** — resources: policy, local store, upload journal
- **6** — transforms: image resize, video posters
- **7** — object storage with presigned upload and authenticated reads
- **8** — dedup, bulk operations, quota, backups, restore drills, import/export
- **9** — plugins and integrations; AI is optional and comes last

### Deferred

Everything below is deliberately out of scope until the core is good:

- End-to-end encryption — plaintext plus disk and backup encryption covers a self-hosted threat model, and `zk` would roughly double the code paths
- Note sharing and a public wiki view
- AI features, embeddings, and vector search
- Bidirectional integrations
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
