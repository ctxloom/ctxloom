#!/usr/bin/env bash
# Self-test of one-writer-debt-check.sh: the count at the ceiling passes, one
# more fails, one fewer fails (lower the ceiling), a missing or non-numeric
# ceiling fails, and a mention outside a `reason = "..."` is not counted.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
check="$here/one-writer-debt-check.sh"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

fixture() { # name, ceiling, number of sites, [extra source line]
  local d="$work/$1" i
  mkdir -p "$d/crates/a/b/x/src" "$d/scripts"
  [ -n "$2" ] && printf '%s\n' "$2" > "$d/scripts/one-writer-debt.max"
  : > "$d/crates/a/b/x/src/lib.rs"
  for ((i = 0; i < $3; i++)); do
    printf '#[expect(clippy::disallowed_methods, reason = "P2 one-writer debt: x")]\n' >> "$d/crates/a/b/x/src/lib.rs"
  done
  [ -n "${4:-}" ] && printf '%s\n' "$4" >> "$d/crates/a/b/x/src/lib.rs"
  echo "$d"
}
expect() { # code, dir, label
  local got=0 out
  out=$("$check" "$2" 2>&1) || got=$?
  if [ "$got" != "$1" ]; then echo "selftest FAIL: $3 (exit $got, wanted $1)" >&2; echo "$out" >&2; exit 1; fi
}
expect 0 "$(fixture at 2 2)" "at the ceiling"
expect 1 "$(fixture over 2 3)" "over the ceiling"
expect 1 "$(fixture under 2 1)" "under the ceiling"
expect 0 "$(fixture none 0 0)" "no debt"
expect 1 "$(fixture no-max '' 1)" "no ceiling file"
expect 1 "$(fixture bad-max abc 1)" "a ceiling that is no number"
expect 0 "$(fixture prose 1 1 '// the P2 one-writer debt: is counted by reason only')" "prose is not a site"
echo "ok   one-writer-debt-check selftest"
