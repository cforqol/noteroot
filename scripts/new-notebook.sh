#!/usr/bin/env bash
set -euo pipefail

DATA="${NOTEROOT_DATA_DIR:-$HOME/.local/share/noteroot}"
SLUG="${1:?usage: new-notebook.sh <slug>}"
ROOT="$DATA/notebooks/$SLUG"

[[ "$SLUG" =~ ^[a-z0-9][a-z0-9-]{0,63}$ ]] || { echo "invalid slug: $SLUG" >&2; exit 1; }
[[ -e "$ROOT" ]] && { echo "exists: $ROOT" >&2; exit 1; }

mkdir -p "$ROOT/vault" "$ROOT/.notebook"/{resources,cache,drafts,trash}

cat > "$ROOT/notebook.toml" <<TOML
id = "$(uuidgen | tr 'A-Z' 'a-z')"
slug = "$SLUG"
name = "$SLUG"
profile = "plaintext"
layout_version = 1
quota_mb = 0
created = "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
TOML

git -C "$ROOT/vault" init -q
echo "created $ROOT"
