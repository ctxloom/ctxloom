#!/usr/bin/env bash
# The counterpart of plain-destroy-check.sh: a `purge` build HAS the deleting
# verbs (owner ruling 2026-11-26: the purge flavour is built and released
# beside the plain one). This runs the binary a `--features purge` build made,
# in a temporary home, and refuses one where `session remove` or `session
# sweep` is an unknown subcommand, or whose `session --help` does not name
# them. `session purge` exists in no build (owner ruling 98) and is held
# unknown here too.
#
# Usage: purge-present-check.sh <ctxloom binary>
# Silent on a pass; on a failure the first line is the summary, then what the
# binary said; exit 1.
set -euo pipefail
bin=${1:?usage: purge-present-check.sh <ctxloom binary>}
# Windows names the binary <name>.exe (cargo's target/debug/ctxloom.exe).
[ -f "$bin.exe" ] && bin=$bin.exe
bin=$(realpath "$bin")
home=$(mktemp -d)
trap 'rm -rf "$home"' EXIT
windows=()
root=${SYSTEMROOT:-${SystemRoot:-}}
if [ -n "$root" ]; then
    native=$(cygpath -w "$home")
    windows=(USERPROFILE="$native" TEMP="$native" TMP="$native" SYSTEMROOT="$root" WINDIR="$root")
fi
ctxloom() { (cd "$home" && env -i PATH="$PATH" HOME="$home" TMPDIR="$home" ${windows[@]+"${windows[@]}"} "$bin" "$@"); }

help=$(ctxloom session --help 2>&1) || true
for verb in remove sweep; do
    if ! grep -qE "^\s+$verb\b" <<< "$help"; then
        echo "purge build: session --help does not name $verb"
        echo "$help"
        exit 1
    fi
    rc=0
    said=$(ctxloom session "$verb" --help 2>&1) || rc=$?
    if [ "$rc" -ne 0 ]; then
        echo "purge build: session $verb --help failed (exit $rc)"
        echo "$said"
        exit 1
    fi
done
rc=0
said=$(ctxloom session purge amber-swift-owl --yes --format text 2>&1) || rc=$?
if [ "$rc" -ne 48 ] || ! grep -qi "unrecognized subcommand\|unknown verb\|did you mean\|unexpected argument" <<< "$said"; then
    echo "purge build: session purge is not an unknown verb (exit $rc)"
    echo "$said"
    exit 1
fi
