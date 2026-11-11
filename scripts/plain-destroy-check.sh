#!/usr/bin/env bash
# A plain build (no `session-destroy`) has no way to delete a session (owner
# rulings 94, 94b, 98): `session remove`, which destroys a session (its
# stream, its runs' streams, its directory and lock), and `session sweep`,
# whose apply is that destroy, are compiled only under the `session-destroy`
# feature, and `session purge` exists in no build. This runs the binary a
# plain `cargo build` made, in a temporary home, and refuses one where any of
# `session remove`, `session purge` or `session sweep` is anything but clap's
# unknown subcommand, or whose `session --help` names one.
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

help=$(ctxloom session --help 2>&1) || true
for verb in remove purge sweep; do
    rc=0
    said=$(ctxloom session "$verb" amber-swift-owl --yes --format text 2>&1) || rc=$?
    if [ "$rc" -ne 20 ] || ! grep -qi "unrecognized subcommand\|unknown verb\|did you mean\|unexpected argument" <<< "$said"; then
        echo "plain build: session $verb is not an unknown verb (exit $rc)"
        echo "$said"
        exit 1
    fi
    if grep -qE "^\s+$verb\b" <<< "$help"; then
        echo "plain build: session --help names $verb"
        echo "$help"
        exit 1
    fi
done
