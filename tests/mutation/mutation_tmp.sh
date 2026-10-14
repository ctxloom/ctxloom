#!/usr/bin/env bash
# Runs a mutation tool with its OWN temp dir under the shared mutation tmp as
# TMPDIR, and removes that dir when the run ends, however it ends. It also
# installs the per-mutant test timeout cap (see the block above the command).
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

# THE PER-MUTANT TIMEOUT CAP. gremlins' budget is a multiplier with no ceiling;
# gremlins_exec.sh, installed as `go test -exec` for everything under this run,
# caps a mutant's test timeout by the rule documented there and beside
# timeout-coefficient in .gremlins.yaml, which is where its constants live. A
# constant that cannot be read refuses the run: an uncapped run is the defect
# this exists to prevent, and it does not announce itself.
here=$(cd "$(dirname "$0")" && pwd)
config="$here/../../.gremlins.yaml"
yaml_int() {
    sed -n "s/^[[:space:]]*$1:[[:space:]]*\([0-9][0-9]*\)[[:space:]]*\(#.*\)\{0,1\}$/\1/p" "$config" | head -n 1
}
coef=$(yaml_int timeout-coefficient)
cap=$(yaml_int ctxloom-timeout-cap-seconds)
floor=$(yaml_int ctxloom-timeout-floor-coefficient)
if [ -z "$coef" ] || [ -z "$cap" ] || [ -z "$floor" ]; then
    echo "mutation_tmp.sh: $config must set integer unleash.timeout-coefficient, ctxloom-timeout-cap-seconds and ctxloom-timeout-floor-coefficient (got '$coef', '$cap', '$floor')" >&2
    exit 2
fi
wrapper="$here/gremlins_exec.sh"
case "$wrapper" in
*[[:space:]]*)
    # GOFLAGS is space-separated with no quoting.
    echo "mutation_tmp.sh: the -exec wrapper path '$wrapper' contains whitespace, which GOFLAGS cannot carry" >&2
    exit 2
    ;;
esac
case " ${GOFLAGS:-} " in
*" -exec=$wrapper "*) ;;
*) GOFLAGS="${GOFLAGS:+$GOFLAGS }-exec=$wrapper" ;;
esac
export GOFLAGS CTXLOOM_MUTATION_TIMEOUT_COEF="$coef" CTXLOOM_MUTATION_TIMEOUT_CAP="$cap" CTXLOOM_MUTATION_TIMEOUT_FLOOR="$floor"

"$@"
