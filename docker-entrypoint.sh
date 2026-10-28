#!/bin/sh
# Remap the noteroot user/group to PUID:PGID so that the bind-mounted
# data directory is accessible regardless of the host uid/gid.
#
# PUID  — desired uid  (default: current uid of the noteroot user, 1000)
# PGID  — desired gid  (default: current gid of the noteroot group, 1000)
#
# If PUID/PGID are not set or already match, this is a no-op so the
# startup cost is negligible in the common case.
set -e

CURRENT_UID=$(id -u noteroot)
CURRENT_GID=$(id -g noteroot)
WANT_UID=${PUID:-$CURRENT_UID}
WANT_GID=${PGID:-$CURRENT_GID}

if [ "$WANT_GID" != "$CURRENT_GID" ]; then
    groupmod -o -g "$WANT_GID" noteroot
fi

if [ "$WANT_UID" != "$CURRENT_UID" ]; then
    usermod -o -u "$WANT_UID" noteroot
fi

# Fix ownership of the data directory so the remapped user can write.
# Only chown the top level and one level deep to keep startup fast on
# large vaults; the server itself creates sub-paths as needed.
chown -R noteroot:noteroot "${NOTEROOT_DATA_DIR:-/data}"

exec su-exec noteroot "$@"
