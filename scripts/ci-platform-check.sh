#!/bin/sh
# The ci-platform nextest profile (.config/nextest.toml) names, as rdeps(),
# every workspace crate that depends (any kind) on a platform seam (os,
# file-system, run-within): nextest's rdeps() matches workspace packages
# only, so the seams' direct users are listed by hand, and this check fails
# when one is missing. Prints `ok ci-platform-check (<n> crates)` or `FAIL
# ci-platform-check: ...` with the missing names.
# POSIX sh (it runs under `sh` as under bash): no arrays, here-strings or
# pipefail, so the metadata is read whole first, and a failed `cargo
# metadata` fails the check rather than reading as no crates.
set -eu
root=$(cd "$(dirname "$0")/.." && pwd)
metadata=$(cargo metadata --format-version 1 --no-deps --manifest-path "$root/Cargo.toml")
users=$(printf '%s\n' "$metadata" |
    jq -r '.packages[] | select(any(.dependencies[]; .name == "os" or .name == "file-system" or .name == "run-within")) | .name' | sort -u)
if [ -z "$users" ]; then
    echo "FAIL ci-platform-check: no workspace crate depends on os, file-system or run-within" >&2
    exit 1
fi
profile=$(sed -n '/^\[profile\.ci-platform\]/,/^\[/p' "$root/.config/nextest.toml")
missing=
for name in $users; do
    case $profile in
    *"rdeps($name)"*) ;;
    *) missing="$missing $name" ;;
    esac
done
if [ -n "$missing" ]; then
    echo "FAIL ci-platform-check: .config/nextest.toml's ci-platform filter lacks rdeps() of:$missing" >&2
    exit 1
fi
echo "ok   ci-platform-check ($(printf '%s\n' "$users" | grep -c .) crates)"
