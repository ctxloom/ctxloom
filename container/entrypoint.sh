#!/bin/sh
# ctxloom agent-image entrypoint: runtime identity remap (the PUID/PGID
# pattern). The image bakes a generic `ctxloom` user (1000:1000); the isolation
# runtime passes PUID/PGID when the run should execute as the launching user
# (rootful docker, podman — where the entrypoint starts as root and can remap).
# Rootless docker passes NO PUID: container-root there already IS the launching
# user host-side, so the run proceeds as root unchanged. Started non-root
# (a user's own --user override), there is nothing to remap.
#
# Never blocks the run: a failed remap or a missing privilege-drop helper warns
# and falls back to the current identity — fault tolerance over strictness.
set -u

if [ "$(id -u)" = "0" ] && [ -n "${PUID:-}" ]; then
    PGID="${PGID:-$PUID}"
    (
        set -e
        groupmod -o -g "$PGID" ctxloom
        usermod -o -u "$PUID" -g "$PGID" ctxloom
    ) || echo "ctxloom-entrypoint: warning: remapping ctxloom to ${PUID}:${PGID} failed; continuing" >&2
    # Hand the (mostly fresh) home to the run user. Read-only mounts inside it
    # (credential files) refuse the chown — fine, their HOST owner already maps
    # to the run user.
    chown -R "$PUID:$PGID" /home/ctxloom 2>/dev/null || true
    if command -v gosu >/dev/null 2>&1; then
        exec gosu ctxloom "$@"
    elif command -v setpriv >/dev/null 2>&1; then
        exec setpriv --reuid "$PUID" --regid "$PGID" --init-groups "$@"
    fi
    echo "ctxloom-entrypoint: warning: no gosu/setpriv on this base; running as root" >&2
fi
exec "$@"
