#!/usr/bin/env bash
# A plain build (no `session-destroy`) has no way to destroy a session (owner
# rulings 94, 94b): `session purge`, which purges a session's stream, its runs'
# streams and its directory, is compiled only under the `session-destroy`
# feature. This runs the binary a plain `cargo build` made, in a temporary
# home, and refuses one whose `session purge` is anything but clap's unknown
# subcommand, or whose `session --help` names `purge`.
#
# The unit tests cannot see this: a workspace test build unifies features.
# Only a build without the dev-dependencies (`just build`) makes the binary
# that ships.
#
# Usage: plain-destroy-check.sh <ctxloom binary>
# Silent on a pass; on a failure the first line is the summary, then what the
# binary said; exit 1.
set -euo pipefail
bin=$(realpath "${1:?usage: plain-destroy-check.sh <ctxloom binary>}")
home=$(mktemp -d)
trap 'rm -rf "$home"' EXIT
ctxloom() { (cd "$home" && env -i PATH="$PATH" HOME="$home" TMPDIR="$home" "$bin" "$@"); }

rc=0
said=$(ctxloom session purge amber-swift-owl --yes --format text 2>&1) || rc=$?
if [ "$rc" -ne 2 ] || ! grep -qi "unrecognized subcommand\|unknown verb\|did you mean\|unexpected argument" <<< "$said"; then
    echo "plain build: session purge is not an unknown verb (exit $rc)"
    echo "$said"
    exit 1
fi
help=$(ctxloom session --help 2>&1) || true
if grep -qE '^\s+purge\b' <<< "$help"; then
    echo "plain build: session --help names purge"
    echo "$help"
    exit 1
fi
