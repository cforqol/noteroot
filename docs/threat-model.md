# Threat model

Scope: what noteroot defends against, what it explicitly does not, and the
consequences for design decisions elsewhere.

## Assets

| Asset | Loss impact |
|---|---|
| Note content (`vault/`) | High — irreplaceable |
| Note identity (frontmatter `id`) | High — links break without it |
| Resources (`resources/`) | Medium — usually recoverable |
| Secrets (integration tokens) | Medium |
| Git history | High — the primary recovery mechanism |
| `state.db` | Low — rebuildable |

## Adversaries

1. **Network attacker** — mitigated by Traefik TLS. Assumes a valid cert chain.
2. **Unauthenticated internet** — mitigated by forward-auth. Only `/healthz` and
   `/readyz` are open, and they expose nothing sensitive.
3. **Disk theft / decommissioned hardware** — partially mitigated. LUKS at
   rest is the operator's job; backups are plaintext local files
   (encrypted offsite backup is deferred), so anyone holding the disk
   holds the notes.
4. **Malicious or buggy plugin** — sandboxed Lua, declared permissions,
   read-only by default, no filesystem, no `os.execute`, no network
   access at all, audit log, per-plugin circuit breaker.
5. **Accidental user error** — trash instead of unlink, git history, conflict
   copies, no destructive merges, dry-run imports.
6. **Bugs in our own code** — atomic writes, bounded failure domains,
   per-notebook isolation, quarantine over repair.

## Explicitly out of scope

- **Hostile server operator.** We are the operator. This is the primary reason
  `zk` (zero-knowledge) is deferred.
- **Multi-user isolation.** One user, one trust domain.
- **Public sharing.** Everything is private; note and resource routes require auth.
- **Supply chain.** Go modules are pinned via `go.sum` but not vendored.
- **Side channels.** No defence against timing or cache attacks.

## Consequences

- `zk` is deferred; at-rest encryption plus encrypted backups are sufficient
  for the stated adversary set. Revisit if the server moves to a shared host.
- Backups are plaintext and local-only today; they must be encrypted
  (or kept on encrypted storage) because a stolen disk yields them with
  the notes. Encrypted offsite backup is deferred work, not a shipped
  mitigation.
- The identity map is derived, so corrupting it degrades to git-based repair
  rather than breaking links outright.
- Plugins are untrusted by default even though we write them — the sandbox is
  cheap and removes a class of "which plugin did that" debugging.
- Cross-site request forgery is blocked by content-type and origin checks on
  every mutating route; no CORS is ever granted and the app mints no ambient
  cookie, so a malicious page cannot ride a forward-auth session into a write.
- `/healthz` stays unauthenticated, accepting that it reveals that the service
  is running.
