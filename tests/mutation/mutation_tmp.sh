#!/usr/bin/env bash
# Runs a mutation tool with its OWN temp dir under the shared mutation tmp as
# TMPDIR, and removes that dir when the run ends, however it ends.
#
# Usage: mutation_tmp.sh <mutation tmp> <command> [args...]
#
# Every recipe that releases a mutation tool goes through this, so there is one
# answer to "where does a run's scratch go, and who removes it".
#
# WHY PER RUN, NOT THE SHARED DIR: the tools' test processes put their own temp
# dirs in TMPDIR, and a test killed mid-run — every gremlins timeout kills one,
# as does an interrupted lane — never removes them. Sweeping only gremlins' own
# module copies out of a shared TMPDIR left every other test's dir behind, run
# after run. A per-run dir also keeps concurrent runs from sweeping each other.
#
# WHY UNDER THE MUTATION TMP AT ALL: gremlins copies the whole module once per
# worker, and on a tmpfs /tmp that has exhausted RAM; the mutation tmp is on
# disk.
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
# chmod first: Go leaves module-cache directories read-only, and rm -rf cannot
# unlink inside a directory it may not write.
cleanup() {
    chmod -R u+w "$run" 2>/dev/null || true
    rm -rf "$run"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM HUP

export TMPDIR="$run" GOTMPDIR="$run"
"$@"
