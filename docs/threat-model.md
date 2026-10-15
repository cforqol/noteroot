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
3. **Disk theft / decommissioned hardware** — mitigated by LUKS at rest and
   encrypted offsite backups.
4. **Malicious or buggy plugin** — sandboxed Lua, declared permissions,
   read-only by default, no filesystem or `os.execute`, network allowlist,
   audit log, per-plugin circuit breaker.
5. **Accidental user error** — trash instead of unlink, git history, conflict
   copies, no destructive merges, dry-run imports.
6. **Bugs in our own code** — atomic writes, bounded failure domains,
   per-notebook isolation, quarantine over repair.

## Explicitly out of scope

- **Hostile server operator.** We are the operator. This is the primary reason
  `zk` (zero-knowledge) is deferred.
- **Multi-user isolation.** One user, one trust domain.
- **Public sharing.** Everything is private; the `/media` route requires auth.
- **Supply chain.** Go modules are pinned via `go.sum` but not vendored.
- **Side channels.** No defence against timing or cache attacks.

## Consequences

- `zk` is deferred; at-rest encryption plus encrypted backups are sufficient
  for the stated adversary set. Revisit if the server moves to a shared host.
- Backups **must** be encrypted, because they leave the host.
- The identity map is derived, so corrupting it degrades to git-based repair
  rather than breaking links outright.
- Plugins are untrusted by default even though we write them — the sandbox is
  cheap and removes a class of "which plugin did that" debugging.
- Cross-site request forgery is blocked by content-type and origin checks on
  every mutating route; no CORS is ever granted and the app mints no ambient
  cookie, so a malicious page cannot ride a forward-auth session into a write.
- `/healthz` stays unauthenticated, accepting that it reveals that the service
  is running.
