#!/usr/bin/env bash
# container-preflight.sh [ENGINE] — fail fast and loudly when the container
# engine cannot run containers, instead of letting a recipe hang on it.
#
# ENGINE defaults to $CONTAINER_CMD, else docker (the justfile's container_cmd).
# Called by every justfile recipe that starts a container or builds an image
# (dev-image, _run, the container-build-* recipes, test-mutation-container).
#
# WHY. A rootless docker daemon whose runtime state under $XDG_RUNTIME_DIR was
# deleted (rootlesskit's state dir with api.sock, and containerd.sock) keeps
# answering its API: MEASURED on such a wedge, `docker version`, `docker info`,
# `docker ps` and the /_ping endpoint all returned in under 40ms with exit 0,
# while `docker run --rm alpine true` never returned. Every containerized
# recipe then hung silently — `just install` for 9 hours, and every
# `git commit`, because the pre-commit hook reaches dev-image. So an API call
# alone cannot discriminate this wedge; the checks are:
#
#   1. `docker info` (other engines: `ENGINE version`), bounded by a timeout —
#      catches a daemon that is down or hung outright; for docker it is also
#      the one call that says "rootless".
#   2. Rootless docker at the dockerd-rootless.sh layout only (endpoint
#      unix://$XDG_RUNTIME_DIR/docker.sock): rootlesskit's
#      $XDG_RUNTIME_DIR/dockerd-rootless/api.sock must exist, and so must
#      containerd.sock under $XDG_RUNTIME_DIR/docker/containerd when dockerd
#      manages that directory. These are the sockets the wedge removed. Both
#      are created in the host's $XDG_RUNTIME_DIR (rootlesskit bind-mounts /run
#      through its copy-up), so they are visible from here on a healthy daemon.
#
# A trivial `run --rm alpine true` is deliberately NOT used: it costs a
# container start (and an image) on every recipe, and on the wedge it would
# need its own timeout and would leave a container behind that the wedged
# daemon cannot reap.
#
# Podman, rootful docker and remote DOCKER_HOST endpoints get check 1 only, so
# nothing here can false-fail a layout it does not recognise.
#
# Skipped (exit 0) under DEVCONTAINER/CI/GITHUB_ACTIONS, as the recipes'
# own host/container dispatch is, and when CTXLOOM_SKIP_CONTAINER_PREFLIGHT is
# set. CTXLOOM_CONTAINER_PREFLIGHT_TIMEOUT (seconds, default 10) bounds check 1.
set -uo pipefail

if [ -n "${DEVCONTAINER:-}" ] || [ -n "${CI:-}" ] || [ -n "${GITHUB_ACTIONS:-}" ] \
    || [ -n "${CTXLOOM_SKIP_CONTAINER_PREFLIGHT:-}" ]; then
    exit 0
fi

engine="${1:-${CONTAINER_CMD:-docker}}"
limit="${CTXLOOM_CONTAINER_PREFLIGHT_TIMEOUT:-10}"

fail() {
    {
        echo "container-preflight: FATAL — $engine cannot run containers: $1"
        shift
        for line in "$@"; do echo "  $line"; done
        echo "  (bypass at your own risk: CTXLOOM_SKIP_CONTAINER_PREFLIGHT=1)"
    } >&2
    exit 1
}

restart_hint=(
    "Fix: systemctl --user restart docker"
    "NOTE: that restarts the whole rootless daemon, so it also stops or restarts"
    "every other container on it (kind clusters, dev databases, other agents' runs)."
)

if ! command -v "$engine" >/dev/null 2>&1; then
    fail "'$engine' is not on PATH" "Install it, or set CONTAINER_CMD to your engine (docker or podman)."
fi

bound=()
if command -v timeout >/dev/null 2>&1; then
    bound=(timeout "$limit")
elif command -v gtimeout >/dev/null 2>&1; then
    bound=(gtimeout "$limit")
fi

# docker: `info`, which also says "rootless" (~35ms). Anything else: `version`
# — MEASURED, `podman info` costs ~1s (it walks storage), `podman version` ~40ms,
# and it still reaches the service for podman-remote/podman machine.
probe=version
[ "$(basename "$engine")" = docker ] && probe=info
info="$(${bound[@]+"${bound[@]}"} "$engine" "$probe" 2>&1)"
rc=$?
if [ "$rc" -eq 124 ]; then
    if [ "$probe" = info ]; then
        fail "'$engine info' did not answer within ${limit}s (daemon hung)" "${restart_hint[@]}"
    fi
    fail "'$engine $probe' did not answer within ${limit}s (engine hung)"
elif [ "$rc" -ne 0 ]; then
    fail "'$engine $probe' failed (exit $rc): $(printf '%s' "$info" | tail -n 3 | tr '\n' ' ')" \
        "Start the engine (docker: systemctl --user start docker, or the rootful service), or set CONTAINER_CMD."
fi

# Check 2: rootless docker at the dockerd-rootless.sh layout.
[ "$probe" = info ] || exit 0
printf '%s' "$info" | grep -qi rootless || exit 0
[ -n "${XDG_RUNTIME_DIR:-}" ] || exit 0

endpoint="${DOCKER_HOST:-}"
if [ -z "$endpoint" ]; then
    endpoint="$(${bound[@]+"${bound[@]}"} "$engine" context inspect --format '{{.Endpoints.docker.Host}}' 2>/dev/null || true)"
fi
[ "$endpoint" = "unix://$XDG_RUNTIME_DIR/docker.sock" ] || exit 0

missing=()
api="$XDG_RUNTIME_DIR/dockerd-rootless/api.sock"
[ -S "$api" ] || missing+=("$api")
cdir="$XDG_RUNTIME_DIR/docker/containerd"
if [ -d "$cdir" ] && [ ! -S "$cdir/containerd.sock" ]; then
    missing+=("$cdir/containerd.sock")
fi
if [ "${#missing[@]}" -ne 0 ]; then
    fail "the rootless daemon's runtime state is gone (missing: ${missing[*]})." \
        "Its API still answers (docker ps/info work), but containers never start or exit:" \
        "every containerized recipe would hang here instead of failing." \
        "${restart_hint[@]}"
fi
exit 0
