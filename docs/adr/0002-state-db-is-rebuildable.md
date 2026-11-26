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
| `resources` (local store) | `resources/` directory |
| `resources` (S3 store) | vault note refs, confirmed against the bucket (see Consequences — not a bucket listing, since every notebook shares one bucket) |
| `resource_sources` | note refs plus resource metadata |
| `tombstones` | `git log` for deleted paths |
| `locks` | rebuilt empty on boot |

`index.db` is fully disposable. `platform.db` is rebuildable except secrets.

## Consequences

- **In-flight uploads are the only loss.** Journal to disk for large imports;
  accept re-offer for casual uploads.
- **S3-backed resources are reconciled against the bucket, not relisted.**
  A rebuild wipes the upload journal; `openResources` runs a one-shot
  reconciliation afterward that scans the vault for `/resources/{sha}`
  links and re-adopts (`Journal.AdoptRecovered`) any referenced blob the
  bucket still has. This is deliberately not a raw bucket listing: all
  notebooks share one S3 bucket via server-wide config, so a listing
  cannot attribute an object to a single notebook. Ownership comes from
  the vault's own note links instead — a blob the bucket has but no note
  references stays unrecovered (and eligible for GC), which is correct:
  nothing in this notebook's vault claims it.
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
