#!/usr/bin/env bash
# Self-test of the red-commit judge (scripts/red-commit-judge.sh), which
# `test-commit` asks before it lets red tests be committed off the default
# branch. A red commit may add red tests and change the expectations of the
# tests it touches; it may never turn red a scenario of a feature file it does
# not touch (commit fd50e0b77 did: run.feature went red, untouched, and the
# gate committed it as RED).
#
# Checked in a fixture repository with the judge's seams: RED_JUDGE_LOG (the
# test-staged log), RED_JUDGE_STAGED_LIST and RED_JUDGE_BASE_LIST (the
# feature runner's GHERKIN_RATCHET lists of the staged tree and of HEAD).
#   - no feature-runner failure in the log: allowed, no list read;
#   - a scenario red at HEAD and still red, its file untouched: allowed;
#   - a scenario green at HEAD turned red, its file untouched: refused, named;
#   - the same scenario with its feature file staged: allowed;
#   - an added red scenario in a staged feature file: allowed;
#   - a failure outside any scenario of an untouched file: refused;
#   - a list missing: refused (nothing can be judged).
set -euo pipefail
while IFS= read -r v; do unset "$v"; done < <(git rev-parse --local-env-vars)
here=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
fail() { echo "FAIL red-commit-judge.selftest: $*" >&2; exit 1; }
list() { # <file> <red|green>:<id>...
    local file=$1 entry
    shift
    : > "$file"
    for entry in "$@"; do printf '%s\t%s\n' "${entry%%:*}" "${entry#*:}" >> "$file"; done
}

fx=$tmp/fx
features=crates/root/bin/ctxloom/features
mkdir -p "$fx/$features/cli" "$fx/crates/lib/src"
printf 'Feature: run\n' > "$fx/$features/cli/run.feature"
printf 'Feature: agent\n' > "$fx/$features/cli/agent.feature"
printf 'pub fn f() {}\n' > "$fx/crates/lib/src/lib.rs"
git -C "$fx" init -q -b main
git -C "$fx" add -A
git -C "$fx" -c user.email=t@e -c user.name=t commit -q -m init
git -C "$fx" checkout -q -b feature

red_log=$tmp/red.log
printf '        FAIL [   5.253s] ctxloom::bin/ctxloom tests::gherkin_features::every_feature_runs\n' > "$red_log"
unit_log=$tmp/unit.log
printf '        FAIL [   0.010s] lib tests::t\n' > "$unit_log"

judge() { # <log> -> rc in $rc, output in $out
    rc=0
    out=$(cd "$fx" && RED_JUDGE_LOG=$1 RED_JUDGE_STAGED_LIST=$tmp/staged.list \
        RED_JUDGE_BASE_LIST=$tmp/base.list "$here/red-commit-judge.sh" 2>&1) || rc=$?
}
stage() { # reset the index to HEAD, then stage a change to each path given
    git -C "$fx" reset -q
    git -C "$fx" checkout -q -- .
    local path
    for path in "$@"; do echo "# changed" >> "$fx/$path"; git -C "$fx" add "$path"; done
}

list "$tmp/base.list" "green:cli/run.feature :: Her settings are hers" \
    "red:cli/agent.feature :: Still red" "green:cli/agent.feature :: Green"

# A red unit test alone is not the judge's: allowed, and no list is read.
stage crates/lib/src/lib.rs
rm -f "$tmp/staged.list"
judge "$unit_log"
[ "$rc" -eq 0 ] || fail "a red unit test was refused ($rc): $out"

# A scenario red at HEAD stays red, its file untouched: allowed.
list "$tmp/staged.list" "green:cli/run.feature :: Her settings are hers" \
    "red:cli/agent.feature :: Still red" "green:cli/agent.feature :: Green"
judge "$red_log"
[ "$rc" -eq 0 ] || fail "a scenario already red at HEAD was refused ($rc): $out"

# fd50e0b77: a green scenario of an untouched feature file turned red.
list "$tmp/staged.list" "red:cli/run.feature :: Her settings are hers" \
    "red:cli/agent.feature :: Still red" "green:cli/agent.feature :: Green"
judge "$red_log"
[ "$rc" -eq 1 ] || fail "a green scenario of an untouched file turned red was allowed ($rc): $out"
grep -q '^FAIL red-commit: 1 scenario turned red in a feature file this commit does not touch' <<< "$out" \
    || fail "the refusal line: $out"
grep -qx '  cli/run.feature :: Her settings are hers' <<< "$out" || fail "the scenario is not named: $out"

# The same, its feature file staged (a red commit changing its expectation).
stage crates/lib/src/lib.rs "$features/cli/run.feature"
judge "$red_log"
[ "$rc" -eq 0 ] || fail "a red scenario of a staged feature file was refused ($rc): $out"

# An added red scenario in a staged feature file: allowed.
stage "$features/cli/agent.feature"
list "$tmp/staged.list" "green:cli/run.feature :: Her settings are hers" \
    "red:cli/agent.feature :: Still red" "green:cli/agent.feature :: Green" "red:cli/agent.feature :: New"
judge "$red_log"
[ "$rc" -eq 0 ] || fail "an added red scenario was refused ($rc): $out"

# A failure outside any scenario of an untouched file: refused.
stage crates/lib/src/lib.rs
list "$tmp/staged.list" "green:cli/run.feature :: Her settings are hers" \
    "red:cli/agent.feature :: Still red" "green:cli/agent.feature :: Green" \
    "red:cli/run.feature :: (outside a scenario)"
judge "$red_log"
[ "$rc" -eq 1 ] || fail "a failure outside a scenario of an untouched file was allowed ($rc): $out"

# A list that cannot be read: refused.
rm -f "$tmp/staged.list"
judge "$red_log"
[ "$rc" -eq 1 ] || fail "a missing staged list was allowed ($rc): $out"
grep -q '^FAIL red-commit: ' <<< "$out" || fail "the missing list is not said: $out"

echo "ok   red-commit-judge.selftest"
