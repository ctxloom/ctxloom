#!/usr/bin/env bash
# gates/red-sanity.sh (owner, 2026-11-19: R0 and R1), checked on a fixture
# workspace and made-up nextest output: red tests that are a broken
# environment (a deleted build tree, a disk that filled) are refused, 122,
# and ordinary red tests are not. `_tests-checked` runs it after a red run;
# the pieces are in gates/gates.just. The script is gates/red-sanity.sh (or
# $RED_SANITY).
set -euo pipefail
while IFS= read -r v; do unset "$v"; done < <(git rev-parse --local-env-vars)
here=$(cd "$(dirname "$0")" && pwd)
sanity=${RED_SANITY:-$here/../gates/red-sanity.sh}
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
fail() { echo "FAIL red-commit-sanity.selftest: $*" >&2; exit 1; }

# `old` has a test file at HEAD, `fresh` (a red-first new crate) has none.
fx=$tmp/fx
mkdir -p "$fx/crates/old/src" "$fx/crates/fresh/src" "$fx/target"
cat > "$fx/Cargo.toml" <<'T'
[workspace]
members = ["crates/old", "crates/fresh"]
resolver = "2"
T
for c in old fresh; do
    printf '[package]\nname = "%s"\nversion = "0.1.0"\nedition = "2021"\n' "$c" > "$fx/crates/$c/Cargo.toml"
    : > "$fx/crates/$c/src/lib.rs"
done
: > "$fx/crates/old/src/lib.test.rs"
git -C "$fx" init -q -b main
git -C "$fx" add -A
git -C "$fx" -c user.email=t@e -c user.name=t commit -q -m init
# The programs the setup script named, and its list of them.
: > "$tmp/bin-a"; : > "$tmp/bin-b"
printf '%s\n%s\n' "$tmp/bin-a" "$tmp/bin-b" > "$fx/target/nextest-binaries.list"

# log <package> <failed> <total> [<ms>]: nextest's output for `failed` failing
# tests of `package` in a run of `total` tests.
log() {
    local pkg=$1 failed=$2 total=$3 ms=${4:-10}
    {
        for ((i = 0; i < failed; i++)); do printf '        FAIL [   0.%03ds] %s tests::t%d\n' "$ms" "$pkg" "$i"; done
        if [ "$failed" -gt 0 ]; then
            printf '     Summary [   1.000s] %d tests run: %d passed, %d failed, 0 skipped\n' "$total" $((total - failed)) "$failed"
        else
            printf '     Summary [   1.000s] %d tests run: %d passed, 0 skipped\n' "$total" "$total"
        fi
    } > "$tmp/log"
}

verdict() { # name expected-rc nextest-rc [dir]
    local rc=0 dir=${4:-$fx}
    (cd "$dir" && CARGO_TARGET_DIR=$fx/target "$sanity" "$tmp/log" "$3" > "$tmp/out" 2>&1) || rc=$?
    [ "$rc" -eq "$2" ] || fail "$1: exit $rc, want $2: $(head -n 5 "$tmp/out")"
}
said() { grep -q "$1" "$tmp/out" || fail "$2: not said ($1): $(cat "$tmp/out")"; }

# 1. The incident: 5,944 of 5,944 failed in 10 ms, the build tree gone.
log old 5944 5944
verdict "the incident, R1 (and R0 once the tree is gone)" 122 100
mv "$fx/target" "$fx/target.gone"
verdict "the incident, build tree gone" 122 100
said 'the target directory is gone' "the incident, build tree gone"
mv "$fx/target.gone" "$fx/target"
log old 5944 5944
verdict "the incident, R1 alone (everything exists)" 122 100
said '5944 of 5944 tests failed' "the incident, R1"
# R0 alone: one failure, but a program the setup script built is gone.
log old 1 664 3
rm "$tmp/bin-b"
verdict "a built test program is gone" 122 100
said 'a test program the setup script built is gone' "a built program gone"
: > "$tmp/bin-b"
mkdir "$tmp/nogit"
verdict "the staged worktree is gone" 122 100 "$tmp/nogit"
said 'the staged worktree is gone' "worktree gone"

# 2. One failing gherkin test of 664.
log old 1 664 3
verdict "one of 664" 0 100
# 3. A new crate 120 of 120 red, no test file at HEAD.
log fresh 120 120
verdict "a new crate, 120 of 120" 0 100
# 4. An existing crate, 30 of 400.
log old 30 400
verdict "30 of 400" 0 100
# 5. An existing crate, 150 of 160.
log old 150 160
verdict "150 of 160" 122 100
# 6. The boundary: 100 of 100 passes, 101 of 101 does not.
log old 100 100
verdict "100 of 100" 0 100
log old 101 101
verdict "101 of 101" 122 100
# The ratio: 101 of 202 refused, 101 of 203 allowed.
log old 101 202
verdict "101 of 202" 122 100
log old 101 203
verdict "101 of 203" 0 100
# The exempt crate's failures leave both counts: 200 red in fresh beside 50 of
# 400 in old (the run is 600 tests).
{ for ((i = 0; i < 200; i++)); do printf '        FAIL [   0.010s] fresh tests::t%d\n' "$i"; done
  for ((i = 0; i < 50; i++)); do printf '        FAIL [   0.010s] old tests::t%d\n' "$i"; done
  printf '     Summary [   1.000s] 600 tests run: 350 passed, 250 failed, 0 skipped\n'; } > "$tmp/log"
verdict "an exempt crate's red beside an old crate's few" 0 100
# ... and an old crate's 101 of the 200 tests that are not the exempt crate's
# are refused though the run is 600 tests (101 + 400 failed).
{ for ((i = 0; i < 400; i++)); do printf '        FAIL [   0.010s] fresh tests::t%d\n' "$i"; done
  for ((i = 0; i < 101; i++)); do printf '        FAIL [   0.010s] old tests::t%d\n' "$i"; done
  printf '     Summary [   1.000s] 600 tests run: 99 passed, 501 failed, 0 skipped\n'; } > "$tmp/log"
verdict "an old crate's 101 of 200 beside an exempt crate's red" 122 100
# 7. rc 120 (a doc test) with an unparsable log: refused; with a readable
# Summary of passing tests, allowed.
printf 'test result: FAILED. 0 passed; 1 failed\n' > "$tmp/log"
verdict "rc 120, no Summary" 122 120
said 'no Summary line' "rc 120, no Summary"
printf '     Summary [   1.000s] banana\n' > "$tmp/log"
verdict "an unreadable Summary" 122 120
log old 0 664
verdict "rc 120, a readable Summary" 0 120
# A failure line that names no package of the workspace is refused.
{ printf '        FAIL [   0.010s] ghost tests::t\n'; printf '     Summary [   1.000s] 5 tests run: 4 passed, 1 failed, 0 skipped\n'; } > "$tmp/log"
verdict "a failure in no package" 122 100
# A binary id is its package up to "::".
{ printf '        FAIL [   0.010s] old::bin/old tests::t\n'; printf '     Summary [   1.000s] 5 tests run: 4 passed, 1 failed, 0 skipped\n'; } > "$tmp/log"
verdict "a binary id" 0 100
echo "ok   red-commit-sanity.selftest"
