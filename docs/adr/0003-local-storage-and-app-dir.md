# ADR 0003 — Local storage and the consolidated app directory

**Status:** accepted
**Date:** 2026-10

## Context

v1 has no object-storage integration, so resources are local. A location was
needed that (a) keeps the git repo text-only, (b) avoids collisions with user
folder names, and (c) makes backup unambiguous.

## Decision

Resources live in `notebooks/<slug>/.notebook/resources/`, content-addressed,
gitignored.

All app-managed state is consolidated under one directory:
```
notebooks/<slug>/
├── notebook.toml
├── vault/          user content, git repo root, markdown only
└── .notebook/      app-managed
    ├── resources/
    ├── cache/
    ├── drafts/
    ├── trash/
    ├── state.db
    └── index.db
```

Because `vault/` contains only markdown and nothing app-managed is inside it,
**no `.gitignore` is needed in a notebook.** The scanner walks `vault/` only.

The backup unit is the notebook directory minus `cache/`.

Note links use `/n/{slug}/media/{sha}` — a neutral URL segment, so the word
"resource" can change without rewriting notes.

## Consequences

- Three entries per notebook, one rule: never hand-edit `.notebook/`.
- Collision-proof: a user folder named `resources` or `cache` cannot clash.
- The store is self-verifying — the filename is the checksum, so integrity
  checking is hash-and-compare with no manifest.
- Offload to object storage later needs no note changes, since links are
  app-relative.
- **A single disk is a single point of loss**, so the nightly local snapshot is
  not optional in v1.
