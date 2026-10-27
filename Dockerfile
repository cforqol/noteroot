FROM golang:1.27-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X noteroot/internal/version.V=${VERSION}" -o /out/noteroot ./cmd/noteroot

FROM alpine:3.22

# git      — vault autocommits and backup bundles shell out to it.
# ffmpeg   — ffprobe/ffmpeg for image, video and audio transforms.
# ca-cert  — S3 over TLS.
# tzdata   — daily notes and log timestamps.
# Versions stay unpinned on purpose: alpine only keeps current releases,
# so `=rX` pins would rot into a broken build.
# hadolint ignore=DL3018
RUN apk add --no-cache git ca-certificates tzdata ffmpeg \
    && adduser -D -u 1000 noteroot \
    && mkdir -p /data \
    && chown noteroot:noteroot /data

COPY --from=build /out/noteroot /usr/local/bin/noteroot

# Numeric uid, same as the `noteroot` user created above.
# hadolint ignore=DL3066
USER noteroot

# Every variable noteroot reads, with its documented default (README,
# "Configuration"). Override any of them at run time; empty means
# "use the built-in default / disabled". Secrets (S3 keys) stay unset
# here and are injected by the orchestrator.
# hadolint ignore=DL3064
ENV NOTEROOT_DATA_DIR=/data \
    NOTEROOT_LISTEN=:8080 \
    NOTEROOT_LOG_LEVEL=info \
    NOTEROOT_AUTH_MODE=forward \
    NOTEROOT_PUBLIC_ORIGIN= \
    NOTEROOT_S3_ENDPOINT= \
    NOTEROOT_S3_BUCKET= \
    NOTEROOT_S3_REGION= \
    NOTEROOT_S3_ACCESS_KEY= \
    NOTEROOT_S3_SECRET_KEY= \
    NOTEROOT_S3_PATH_STYLE=false \
    NOTEROOT_S3_PRESIGN_TTL= \
    NOTEROOT_QUOTA_BYTES=0 \
    NOTEROOT_GC_INTERVAL=24h \
    NOTEROOT_READ_TIMEOUT=10s \
    NOTEROOT_WRITE_TIMEOUT=10s \
    NOTEROOT_IDLE_TIMEOUT=60s \
    NOTEROOT_MAX_HEADER_BYTES=65536 \
    GIT_CONFIG_COUNT=1 \
    GIT_CONFIG_KEY_0=safe.directory \
    GIT_CONFIG_VALUE_0=*

LABEL org.opencontainers.image.title="noteroot" \
      org.opencontainers.image.description="Markdown notes as plain files: notebooks, full-text search, vim mode, resources and Lua plugins." \
      org.opencontainers.image.source="https://github.com/cforqol/noteroot"

EXPOSE 8080

# The port is derived from NOTEROOT_LISTEN (:8080, 0.0.0.0:8080, [::]:9090 …)
# and /healthz is unauthenticated in both auth modes, so this holds whatever
# the container is configured to listen on. Shell form: the command is a
# port-derivation assignment followed by wget, so /bin/sh has to run it.
# hadolint ignore=DL3025
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD p="${NOTEROOT_LISTEN##*:}"; wget -q -O /dev/null "http://127.0.0.1:${p:-8080}/healthz" || exit 1

ENTRYPOINT ["/usr/local/bin/noteroot"]
