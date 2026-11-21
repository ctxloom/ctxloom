#!/usr/bin/env bash
# A plain build (no `test-support`) knows only the real engines (ruling 52):
# the mock engine and the mock-* test engines are compiled and registered only
# under the `test-support` feature the acceptance tests build with. This runs
# the binary a plain `cargo build` made, in a temporary home, and refuses one
# whose config created on a mock (the library's list, which the refusal names)
# or `manage install --engine mock` (the surface registry) is not the
# unknown-engine refusal. It reaches no daemon: `config create --engine`
# judges the list in process (`agent create`, which judges the same list,
# `library_application::agents::ENGINES`, is the daemon's write since step
# 10b U3).
#
# The unit tests cannot see this: a workspace test build unifies features, so
# every crate there has `test-support` on. Only a build without the
# dev-dependencies (`just build`) makes the binary that ships.
#
# Usage: plain-engines-check.sh <ctxloom binary>
# Silent on a pass; on a failure the first line is the summary, then what the
# binary said; exit 1.
set -euo pipefail
bin=${1:?usage: plain-engines-check.sh <ctxloom binary>}
# Windows names the binary <name>.exe (cargo's target/debug/ctxloom.exe).
[ -f "$bin.exe" ] && bin=$bin.exe
bin=$(realpath "$bin")
home=$(mktemp -d)
trap 'rm -rf "$home"' EXIT
# On Windows (SYSTEMROOT set; never on unix, where nothing is added) the
# binary's home is USERPROFILE and its temporary directory TEMP or TMP, in
# Windows' own form, and a Windows program needs SYSTEMROOT and WINDIR.
windows=()
root=${SYSTEMROOT:-${SystemRoot:-}}
if [ -n "$root" ]; then
    native=$(cygpath -w "$home")
    windows=(USERPROFILE="$native" TEMP="$native" TMP="$native" SYSTEMROOT="$root" WINDIR="$root")
fi
# An empty array is spelled ${a[@]+"${a[@]}"}: macOS's bash 3.2 calls a bare
# "${a[@]}" of an empty one unbound under set -u.
# A Windows jq ends its lines CRLF; its answers here are compared as text.
jq() { command jq "$@" | tr -d '\r'; }
ctxloom() { (cd "$home" && env -i PATH="$PATH" HOME="$home" TMPDIR="$home" ${windows[@]+"${windows[@]}"} "$bin" "$@"); }

# The library's list (`engine list` and `show` are the daemon's reads since
# step 10a U2, `agent create` its write since 10b U3, and this check reaches
# no daemon): a config created on an engine the list does not hold is
# refused, in process, naming the list.
project="$home/project"
mkdir -p "$project"
inproject() { (cd "$project" && env -i PATH="$PATH" HOME="$home" TMPDIR="$home" ${windows[@]+"${windows[@]}"} "$bin" "$@"); }
for engine in mock mock-launch mock-lossy mock-noskills; do
    rc=0
    said=$(inproject config create --engine "$engine" 2>&1) || rc=$?
    code=$(jq -r '.code? // empty' <<< "$said" 2>/dev/null || true)
    valid=$(jq -r '.args.valid? // empty' <<< "$said" 2>/dev/null || true)
    if [ "$rc" -eq 0 ] || [ "$code" != library-application-write-config-engine ]; then
        echo "plain build: a config on $engine is not refused as an unknown engine (exit $rc)"
        echo "$said"
        exit 1
    fi
    if grep -q mock <<< "$valid"; then
        echo "plain build: the engines known name a test engine: $valid"
        exit 1
    fi
done
# The surface registry: an engine it does not hold is refused by name.
rc=0
said=$(ctxloom manage install --engine mock --format json 2>&1) || rc=$?
code=$(jq -r '.code? // empty' <<< "$said" 2>/dev/null || true)
if [ "$rc" -eq 0 ] || [ "$code" != setup-application-unknown-engine ]; then
    echo "plain build: manage install --engine mock is not refused as an unknown engine (exit $rc)"
    echo "$said"
    exit 1
fi
