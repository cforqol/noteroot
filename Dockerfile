FROM golang:1.27-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X noteroot/internal/version.V=${VERSION}" -o /out/noteroot ./cmd/noteroot

FROM alpine:3.22

RUN apk add --no-cache git ca-certificates tzdata ffmpeg ffmpeg \
    && adduser -D -u 1000 noteroot \
    && mkdir -p /data \
    && chown noteroot:noteroot /data

COPY --from=build /out/noteroot /usr/local/bin/noteroot

USER noteroot

ENV NOTEROOT_DATA_DIR=/data \
    NOTEROOT_LISTEN=:8080 \
    GIT_CONFIG_COUNT=1 \
    GIT_CONFIG_KEY_0=safe.directory \
    GIT_CONFIG_VALUE_0=*

EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD wget -q -O /dev/null http://127.0.0.1:8080/healthz || exit 1

ENTRYPOINT ["/usr/local/bin/noteroot"]
