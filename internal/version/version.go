// Package version exposes the build version. Release builds stamp it
// with -ldflags "-X noteroot/internal/version.V=<version>"; plain
// `go build` / `make dev` leave the dev default below.
package version

// V is the version reported by /healthz, /readyz and `noteroot --version`.
var V = "dev"
