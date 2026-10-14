# ADR 0001 — No local-first in v1

**Status:** accepted
**Date:** 2026-10

## Context

"Local-first" is used to mean two different things:

1. **Local write buffering** — edits persist in the browser before hitting the
   network, so a failed save does not lose them.
2. **Local replica** — the browser holds the whole vault, works offline, and
   syncs deltas.

Building the second is roughly 1500+ lines: IndexedDB stores, an outbox, a
delta protocol, conflict merge, and multi-tab leader election.

The motivating trauma was Joplin losing a large notebook. But that was
**sync corruption**, not unsaved edits — and local-first does nothing about
corruption.

## Decision

Ship no local-first in v1. The server is the source of truth.

Loss is prevented by: debounced autosave, atomic server writes, git autocommit,
in-session retry, a close guard, and a crash pad covering tab crash and offline.

Two constraints keep the option open at near-zero cost:

1. All writes go through a single `saveNote` chokepoint.
2. Writes use `If-Match` with a content-hash ETag.

## Consequences

- No IndexedDB layer, no batch `/sync` endpoint, no diff3 merge, no leader
  election in v1.
- Adding a durable outbox later is a change inside one function plus one new
  module — not an editor rewrite.
- Revisit if editing happens regularly on mobile or flaky networks, or if an
  edit is lost twice.
