# noteroot - development Makefile
#
# Environment comes from .envrc (direnv), never from here. Anything that
# touches data depends on `guard`, so a dev run can never silently
# operate on production notes.

.DEFAULT_GOAL := help

BIN  := bin/noteroot
PKG  := ./cmd/noteroot
DATA := $(NOTEROOT_DATA_DIR)

.PHONY: help setup tidy fmt vet lint test vuln check \
        build run dev guard \
        notebook-new notebook-ls data-wipe \
        docker-build docker-up docker-down docker-logs \
        clean

## help: list targets
help:
	@grep -E '^## ' Makefile | sed 's/^## //' \
	  | awk -F': ' '{printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

## setup: install dev tools
setup:
	go install github.com/air-verse/air@latest
	go install honnef.co/go/tools/cmd/staticcheck@latest
	go install golang.org/x/vuln/cmd/govulncheck@latest
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest

## tidy: sync go.mod and go.sum
tidy:
	go mod tidy

## fmt: format all Go source
fmt:
	gofmt -l -w .

## vet: run go vet
vet:
	go vet ./...

## lint: staticcheck and golangci-lint
lint:
	staticcheck ./...
	golangci-lint run

## test: run tests with the race detector
test:
	go test -race -count=1 ./...

test-web:
	cd web && npm test

test-all: test test-web

## vuln: scan dependencies for known CVEs
vuln:
	govulncheck ./...

## check: fmt, vet, lint, test, vuln in one pass
check: fmt vet lint test vuln

## build: compile to bin/noteroot
build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o $(BIN) $(PKG)

## run: build and run once
run: guard build
	./$(BIN)

## dev: hot reload with air
dev: guard
	air

## guard: verify the environment before touching data
guard:
	@test -n "$(DATA)" || { \
	  printf '\033[31m?\033[0m NOTEROOT_DATA_DIR unset ? is direnv active?\n'; exit 1; }
	@case "$(DATA)" in \
	  */.devdata) printf '\033[32m?\033[0m dev data: %s\n' "$(DATA)" ;; \
	  *) printf '\033[33m!\033[0m not dev data: %s\n' "$(DATA)" ;; \
	esac

## notebook-new: create a notebook in dev data
notebook-new: guard
	@read -p "slug: " slug; ./scripts/new-notebook.sh "$$slug"

## notebook-ls: list notebooks in dev data
notebook-ls: guard
	@ls -1 "$(DATA)/notebooks" 2>/dev/null || echo "(none)"

## data-wipe: delete dev data
data-wipe: guard
	rm -rf "$(DATA)"
	@echo "wiped $(DATA)"

## docker-build: build the container image
docker-build:
	docker compose -f deploy/compose.yaml build

## docker-up: start the stack
docker-up:
	docker compose -f deploy/compose.yaml up -d --build

## docker-down: stop the stack
docker-down:
	docker compose -f deploy/compose.yaml down

## docker-logs: follow container logs
docker-logs:
	docker compose -f deploy/compose.yaml logs -f

## clean: remove build output
clean:
	rm -rf bin
