#!/bin/sh
# ctxloom agent-image entrypoint: runtime identity remap (the PUID/PGID
# pattern). The image bakes a generic `ctxloom` user (1000:1000); the isolation
# runtime passes PUID/PGID when the run should execute as the launching user
# (rootful docker, podman — where the entrypoint starts as root and can remap).
# Rootless docker passes NO PUID: container-root there already IS the launching
# user host-side, so the run proceeds as root unchanged. Started non-root
# (a user's own --user override), there is nothing to remap.
#
# When PUID is set the run must NOT fall back to root: a root engine writes
# root-owned files straight into the bind-mounted project. If the requested
# identity cannot be assumed (no usable gosu/setpriv), the entrypoint REFUSES
# to start the engine — a loud launch failure the host gate catches — unless
# CTXLOOM_ALLOW_ROOT=1 explicitly accepts root. ctxloom never sets it, in any
# mode (--degraded included); it exists only for a container a person starts
# by hand.
set -u

if [ "$(id -u)" = "0" ] && [ -n "${PUID:-}" ]; then
    PGID="${PGID:-$PUID}"
    remapped=1
    # stdout belongs to the command this execs, and callers read it byte for
    # byte; usermod reports "no changes" on stdout when PUID is already 1000.
    (
        set -e
        exec >&2
        groupmod -o -g "$PGID" ctxloom
        usermod -o -u "$PUID" -g "$PGID" ctxloom
    ) || remapped=0
    [ "$remapped" = 1 ] || echo "ctxloom-entrypoint: warning: remapping ctxloom to ${PUID}:${PGID} failed" >&2
    # Hand the (mostly fresh) home to the run user. Read-only mounts inside it
    # (credential files) refuse the chown — fine, their HOST owner already maps
    # to the run user.
    chown -R "$PUID:$PGID" /home/ctxloom 2>/dev/null || true
    # gosu drops to the NAMED user, so it is only correct when the remap stuck;
    # setpriv takes the numeric ids directly and is immune to a failed remap.
    if [ "$remapped" = 1 ] && command -v gosu >/dev/null 2>&1; then
        exec gosu ctxloom "$@"
    elif command -v setpriv >/dev/null 2>&1; then
        exec setpriv --reuid "$PUID" --regid "$PGID" --init-groups "$@"
    fi
    if [ "${CTXLOOM_ALLOW_ROOT:-}" != "1" ]; then
        echo "ctxloom-entrypoint: error: cannot run as ${PUID}:${PGID} (no usable gosu/setpriv in this image); refusing to run the engine as root — it would root-own files in the mounted project. Rebuild the image with the remap tools (ctxloom container build). ctxloom never overrides this refusal, --degraded included; only a container you start yourself with CTXLOOM_ALLOW_ROOT=1 runs as root" >&2
        exit 3
    fi
    echo "ctxloom-entrypoint: warning: cannot run as ${PUID}:${PGID} (no usable gosu/setpriv); CTXLOOM_ALLOW_ROOT=1 — running as root" >&2
fi
exec "$@"
