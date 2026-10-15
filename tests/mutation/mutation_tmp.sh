#!/usr/bin/env bash
# Runs a mutation tool with its OWN temp dir under the shared mutation tmp as
# TMPDIR and GOTMPDIR, and a fresh XDG_RUNTIME_DIR inside it, and removes that
# dir when the run ends, however it ends.
#
# Usage: mutation_tmp.sh <mutation tmp> <command> [args...]
#
# Every recipe that releases a mutation tool goes through this, so there is one
# answer to "where does a run's scratch go, and who removes it".
#
# WHY PER RUN, NOT THE SHARED DIR: the tool's test processes put their own temp
# dirs in TMPDIR, and a test killed mid-run — every timed-out mutant kills one,
# as does an interrupted lane — never removes them. A per-run dir removed on
# exit takes them all, and keeps concurrent runs from sweeping each other.
#
# WHY UNDER THE MUTATION TMP AT ALL: it is on disk. Those leftovers, and the
# per-mutant overlays and test binaries the tool builds, add up over a long
# run, and on a tmpfs /tmp they are RAM.
#
# WHY GOTMPDIR TOO: the caller's GOTMPDIR is not neutral. An agent cell exports
# one into its own session directory, which is deep and sits inside the real app
# dir; a tool's coverage run inherits it, the suite's longer fixtures then
# overflow the file-name limit, and its sandbox assertions resolve into the
# real app dir — zero mutants, exit 1. The run dir is short, on disk, and
# removed below, so it serves as GOTMPDIR as well as TMPDIR.
#
# The command's TMPDIR is exported, so a recipe that must name it to something
# else (a container mount) reads "$TMPDIR" inside the command.
set -euo pipefail

if [ "$#" -lt 2 ]; then
    echo "usage: $0 <mutation tmp> <command> [args...]" >&2
    exit 2
fi
base=$1
shift

mkdir -p "$base"
run=$(mktemp -d "$base/run-XXXXXX")
# chmod first: rm -rf cannot unlink inside a directory it may not write (Go
# leaves module-cache directories read-only) nor list one it may not read or
# search (a mutant that flips a directory mode leaves those behind).
cleanup() {
    chmod -R u+rwX "$run" 2>/dev/null || true
    rm -rf "$run"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM HUP

export TMPDIR="$run" GOTMPDIR="$run"

# INVARIANT: A MUTANT MUST NEVER REACH THE HOST'S RUNTIME DIR. Tests sweep
# XDG_RUNTIME_DIR (isolation's scratch reaper does), guarded by a filter a
# mutant negates — and the host's runtime dir holds the rootless docker
# daemon's socket and state. A `||`->`&&` mutant of that filter, run against
# the real /run/user/<uid>, deleted the daemon twice. So the run gets a fresh,
# empty runtime dir inside its own scratch, removed with it, and the daemon is
# reached through an explicit DOCKER_HOST instead: pinned from the caller's
# runtime socket before that dir is hidden, unless the caller already set one.
if [ -z "${DOCKER_HOST:-}" ] && [ -n "${XDG_RUNTIME_DIR:-}" ] && [ -S "$XDG_RUNTIME_DIR/docker.sock" ]; then
    export DOCKER_HOST="unix://$XDG_RUNTIME_DIR/docker.sock"
fi
mkdir -m 0700 "$run/xdg-runtime"
export XDG_RUNTIME_DIR="$run/xdg-runtime"

"$@"
