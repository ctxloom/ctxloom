#!/usr/bin/env bash
# Self-test of one-writer-check.sh: a site in the daemon's crate or the store
# adapter passes, a per-test allowance passes, a site anywhere else fails, a
# leftover "P2 one-writer debt" fails, and a comment naming the lint is no site.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
check="$here/one-writer-check.sh"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

fixture() { # name, crate dir, source line(s)
  local d="$work/$1"
  mkdir -p "$d/crates/$2/src"
  printf '%s\n' "$3" > "$d/crates/$2/src/lib.rs"
  echo "$d"
}
expect() { # code, dir, label
  local got=0 out
  out=$("$check" "$2" 2>&1) || got=$?
  if [ "$got" != "$1" ]; then echo "selftest FAIL: $3 (exit $got, wanted $1)" >&2; echo "$out" >&2; exit 1; fi
}
site='#[expect(clippy::disallowed_methods, reason = "the daemon is the one writer")]'
test_allow='#[allow(clippy::disallowed_methods, reason = "a test kernel or store of its own")]'
expect 0 "$(fixture daemon root/daemon/ctxloom-daemon "$site")" "a site in the daemon"
expect 0 "$(fixture store adapters/store/ctxloom-store-sqlite "$site")" "a site in the store adapter"
expect 0 "$(fixture test core/application/x "$test_allow")" "a per-test allowance"
fake='#[expect(clippy::disallowed_methods, reason = "a test fake'"'"'s own in-memory kernel, never the store")]'
expect 0 "$(fixture fake core/application/x "$fake")" "a test fake in-memory kernel"
expect 1 "$(fixture outside root/lib/ctxloom-wiring "$site")" "a site outside the daemon"
expect 1 "$(fixture expect-test core/application/x '#[expect(clippy::disallowed_methods, reason = "a test kernel or store of its own")]')" "an expect is no per-test allowance"
expect 1 "$(fixture debt core/application/x '#[expect(clippy::disallowed_methods, reason = "P2 one-writer debt: x")]')" "a leftover debt"
expect 0 "$(fixture prose core/application/x '// clippy::disallowed_methods is the lint')" "prose is not a site"
echo "ok   one-writer-check selftest"
