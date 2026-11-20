#!/usr/bin/env bash
# The ci-platform nextest profile (.config/nextest.toml) names, as rdeps(),
# every workspace crate that depends (any kind) on a platform seam (os,
# file-system, run-within): nextest's rdeps() matches workspace packages
# only, so the seams' direct users are listed by hand, and this check fails
# when one is missing. Prints `ok ci-platform-check (<n> crates)` or `FAIL
# ci-platform-check: ...` with the missing names.
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
users=$(cargo metadata --format-version 1 --no-deps --manifest-path "$root/Cargo.toml" |
    jq -r '.packages[] | select(any(.dependencies[]; .name == "os" or .name == "file-system" or .name == "run-within")) | .name' | sort -u)
profile=$(sed -n '/^\[profile\.ci-platform\]/,/^\[/p' "$root/.config/nextest.toml")
missing=()
while IFS= read -r name; do
    grep -qF "rdeps($name)" <<< "$profile" || missing+=("$name")
done <<< "$users"
if [ "${#missing[@]}" -gt 0 ]; then
    echo "FAIL ci-platform-check: .config/nextest.toml's ci-platform filter lacks rdeps() of: ${missing[*]}" >&2
    exit 1
fi
echo "ok   ci-platform-check ($(grep -c . <<< "$users") crates)"
