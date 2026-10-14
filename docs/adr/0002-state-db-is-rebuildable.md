# ADR 0002 — state.db is rebuildable

**Status:** accepted
**Date:** 2026-10

## Context

`state.db` holds the note identity map, which link resolution depends on.
Treating it as primary would make it a single point of loss and would
contradict the principle that no database holds primary data.

## Decision

`state.db` holds **no primary data**. Every row is reconstructible from the
vault, git history, or object storage. Rebuild is a supported code path with an
acceptance test, not an emergency script.

Rebuild sources:

| Table | Source |
|---|---|
| `note_identity` | frontmatter `id` on every note |
| `resources` | `resources/` directory or bucket listing |
| `resource_sources` | note refs plus resource metadata |
| `tombstones` | `git log` for deleted paths |
| `locks` | rebuilt empty on boot |

`index.db` is fully disposable. `platform.db` is rebuildable except secrets.

## Consequences

- **In-flight uploads are the only loss.** Journal to disk for large imports;
  accept re-offer for casual uploads.
- A corrupt database is **quarantined, not repaired** — regenerating is more
  predictable than SQLite recovery. `.recover` is a best-effort salvage for
  `attempts` and tombstones before the rebuild.
- Migrations are forward-only and idempotent.
- A boot-time **rebuildability guard** checks for divergence (empty identity map
  while notes exist) — `integrity_check` alone will not catch silent row loss.
- The acceptance test is: after a rebuild, every media ref and uid link
  resolves.
- **Discipline required:** do not let `state.db` accumulate convenience data.
  The moment it holds something underivable, the property is lost.
