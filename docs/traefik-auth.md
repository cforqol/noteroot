# Deploying noteroot behind Traefik: `AUTH_MODE` and `PUBLIC_ORIGIN`

Deployment reference for the two configuration variables that decide whether a
reverse-proxied install works, and the proxy setups each combination assumes.

noteroot ships no proxy of its own. It authenticates nothing by itself: it
trusts a header, and it gates mutations on an origin allowlist. Both are the
proxy's responsibility to get right. See [Configuration](../README.md#configuration)
for the full variable list.

* * *

## The two variables

| Variable | Default | Behaviour |
| --- | --- | --- |
| `NOTEROOT_AUTH_MODE` | `forward` | `forward` — every request except `/healthz`, `/readyz`, the static shell and `GET /api/v1/notebooks` returns `401 {"error":"unauthenticated"}` unless the request carries `X-Auth-User`. `none` — the header is never read. |
| `NOTEROOT_PUBLIC_ORIGIN` | empty | CSRF allowlist. When set, the `Origin` (or `Referer`) header must **string-equal** this value. When empty, it is compared against the request `Host` instead. |

Both are read only under the `NOTEROOT_` prefix. `AUTH_MODE=none` is silently
ignored, and an unrecognised value for `NOTEROOT_AUTH_MODE` fails boot with
`unknown NOTEROOT_AUTH_MODE`.

## Semantics

The two are unrelated failure paths and should never be reasoned about together.
`AUTH_MODE` answers *who is this*. `PUBLIC_ORIGIN` answers *where did this come
from*.

`PUBLIC_ORIGIN` is either/or, not additive:

```go
if publicOrigin != "" {
    return value == publicOrigin   // literal comparison, Host ignored
}
return u.Host == host              // derived from the request
```

Setting it therefore **replaces** the Host check rather than hardening it. Set it
only when the proxy rewrites `Host`. Set it wrong and every mutation becomes
`403 {"error":"cross-origin forbidden"}`.

## Setups

### 1. Client certificates, `Host` passed through

Recommended when TLS client auth terminates at the proxy. mTLS has already
authenticated the user, so the app needs no identity layer of its own.

```yaml
services:
  noteroot:
    image: ghcr.io/cforqol/noteroot:latest
    # no ports: — reach it over the internal network only
    environment:
      NOTEROOT_DATA_DIR: /data
      NOTEROOT_AUTH_MODE: none
      # NOTEROOT_PUBLIC_ORIGIN intentionally unset
```

```yaml
tls:
  options:
    mtls:
      clientAuth:
        clientAuthType: RequireAndVerifyClientCert
        caFiles:
          - /certs/ca-root.crt
```

`RequireAnyClientCert` accepts a self-signed certificate; `RequestClientCert`
does not require one at all. Only `RequireAndVerifyClientCert` gates access. The
options must also be referenced by the router
(`traefik.http.routers.<name>.tls.options=mtls@file`).

### 2. Client certificates, `Host` rewritten

As above, but something rewrites `Host` to an internal value (`noteroot:8080`).
The derived check now compares the browser's `https://notes.example.com` against
`noteroot:8080` and fails.

```yaml
    environment:
      NOTEROOT_AUTH_MODE: none
      NOTEROOT_PUBLIC_ORIGIN: https://notes.example.com
```

Exact value: scheme, host, port when non-default, no trailing slash.

### 3. Forward-auth, `Host` passed through

```yaml
    environment:
      NOTEROOT_AUTH_MODE: forward
      # NOTEROOT_PUBLIC_ORIGIN intentionally unset
    labels:
      - "traefik.enable=true"
      - "traefik.http.routers.noteroot.rule=Host(`notes.example.com`)"
      - "traefik.http.middlewares.nr-auth.forwardauth.address=http://authelia:9091/api/verify?rd=https://auth.example.com"
      - "traefik.http.middlewares.nr-auth.forwardauth.authResponseHeaders=X-Auth-User"
      - "traefik.http.routers.noteroot.middlewares=nr-auth@docker"
```

`authResponseHeaders` is mandatory. Traefik discards the auth service's response
headers otherwise, and the symptom is a page that loads normally with a 401 on
every mutation.

If the auth service emits a different name (`X-Forwarded-User`, `Remote-User`,
`X-authentik-username`), rename it with a static header rather than rewriting the
app:

```yaml
      - "traefik.http.middlewares.nr-user.headers.customrequestheaders.X-Auth-User=me"
      - "traefik.http.routers.noteroot.middlewares=nr-auth@docker,nr-user@docker"
```

Order matters: the header middleware runs after auth, so it overwrites any
client-supplied value.

### 4. Forward-auth, `Host` rewritten

Combines 2 and 3.

```yaml
    environment:
      NOTEROOT_AUTH_MODE: forward
      NOTEROOT_PUBLIC_ORIGIN: https://notes.example.com
```

### 5. TLS only, no client auth, no forward-auth

`AUTH_MODE=none` with nothing in front leaves the notebook open to anyone who
finds the hostname. Add either client auth or forward-auth.

### 6. Any of the above, port still published

```yaml
    ports:
      - "127.0.0.1:8080:8080"   # bypasses the proxy
```

Client certificates and forward-auth both terminate at the proxy, so a direct
request to 8080 skips them. Under `none` that is full unauthenticated
read/write/delete for anything on the host. Remove the `ports:` block and let the
proxy reach `noteroot:8080` over the internal network.

## Symptoms

| Response | Cause | Fix |
| --- | --- | --- |
| `401 {"error":"unauthenticated"}` | `forward` with no `X-Auth-User` arriving | add forward-auth with `authResponseHeaders`, or use `none` behind mTLS |
| `403 {"error":"cross-origin forbidden"}` | `PUBLIC_ORIGIN` set and not equal to `Origin` | correct the value, or unset it when `Host` passes through |
| `403 {"error":"forbidden content type"}` | form or multipart body on a route that forbids it | send JSON; multipart is accepted only on upload and import |
| Works in `curl`, fails in a browser | `curl` sent no `Origin`, so CSRF passed unconditionally | retest with an explicit `Origin` header |

A mutation that fails with 401 is not a CSRF problem, and a 403 is not an auth
problem. Read the error string before changing either variable.

## Verification

Determine whether `Host` survives the proxy. A throwaway `whoami` behind
identical labels prints the headers a backend would receive; compare `Host:` to
`X-Forwarded-Host:`. Equal means pass-through and `PUBLIC_ORIGIN` is
unnecessary. Different means it is rewritten and the variable is required.

```bash
docker compose config | grep -i passhost
```

Test a mutation the way a browser does, with a real `Origin`:

```bash
curl -i -X POST https://notes.example.com/api/v1/notebooks \
  --cert client.crt --key client.key \
  -H 'Content-Type: application/json' \
  -H 'Origin: https://notes.example.com' \
  -d '{"slug":"probe"}'
```

Confirm once in a browser and read the `Origin` request header in the developer
tools. That value — not the address bar — is what the variable must match.

## Rules of thumb

- Single-user install behind client certificates: `none`.
- Set `PUBLIC_ORIGIN` only when `Host` is rewritten. Never set it speculatively.
- One auth mode, never both.
- `none` without the port closed is an open notebook.
