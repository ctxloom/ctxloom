#!/usr/bin/env bash
# The red-tests sanity rule of `test-commit` (owner, 2026-11-19: R0 and R1),
# run by `_tests-checked` INSIDE the staged tree after a test run that
# exited 100 or 120, on nextest's output. Red tests are allowed off the
# default branch, but not a MASS ENVIRONMENTAL failure: once 5,944 tests
# "failed" in about 10 ms ("No such file", a deleted build tree) and were
# committed as red.
#
#   usage: red-sanity.sh <nextest output file> <the run's exit code>
#   exit:  0 plausible red tests; 122 refused (never red, on every branch)
#
# 122, not 121: `_tests`' 121 is "nextest failed some other way", and this
# says something else, that tests ran and failed for want of an environment.
#
# R0 (exact): after the run the staged worktree (the cwd), its target
# directory ($CARGO_TARGET_DIR, else target) and the programs the setup script
# named (the target directory's nextest-binaries.list, which
# scripts/nextest-build-binaries.sh writes beside CTXLOOM_TEST_BINARY and
# CTXLOOM_TEST_COMPANION_STUB) must still exist.
# R1 (backstop for ENOSPC, OOM and the like): among the packages that have a
# *.test.rs at HEAD, failures F over 100 and F/T at least 0.5 are refused.
# A package with no test file at HEAD (a red-first new crate) is exempt: its
# failures leave F and T both. T is the run's tests, less those exempt
# failures (a passing test of an exempt package is not known from the log).
# A log with no readable Summary, or a failing test of a package the
# workspace does not have, is refused (fail closed). Accepted: a deliberate
# commit breaking over 100 existing tests at once is refused; a gross but
# sub-threshold break (101 of 5,000) passes, and the land gate catches it.
set -uo pipefail
out=$1 rc=$2
floor=100
refuse() { echo "red-sanity: $*"; exit 122; }

# R0.
[ -e .git ] || refuse "the staged worktree is gone ($PWD): the tests failed for want of it, not as tests"
target=${CARGO_TARGET_DIR:-target}
[ -d "$target" ] || refuse "the target directory is gone ($target): the tests failed for want of it, not as tests"
if [ -f "$target/nextest-binaries.list" ]; then
    while IFS= read -r exe; do
        [ -z "$exe" ] || [ -e "$exe" ] || refuse "a test program the setup script built is gone ($exe): the tests failed for want of it, not as tests"
    done < "$target/nextest-binaries.list"
fi

# R1.
summary=$(grep -E '^ *Summary \[' "$out" | tail -n 1)
[ -n "$summary" ] || refuse "no Summary line in the test output (exit $rc): cannot tell a test failure from the environment's"
run=$(sed -nE 's/.*\] +([0-9]+) tests? run:.*/\1/p' <<< "$summary")
[ -n "$run" ] || refuse "the Summary line is unreadable: $summary"
failed=$(sed -nE 's/.* ([0-9]+) failed.*/\1/p' <<< "$summary")
failed=${failed:-0}

mapfile -t lines < <(grep -E '^ +(FAIL|SIGABRT|TIMEOUT|SIGSEGV|LEAK-FAIL|EXEC-FAIL) ' "$out" | sed -E 's/^ +[A-Z-]+ +\[[^]]*\] *//' | sort -u)
meta=$(cargo metadata --format-version 1 --no-deps --offline) || refuse "cannot read the workspace's packages"
tests_at_head=$(git ls-tree -r --name-only HEAD | grep -E '\.test\.rs$' || true)
declare -A has_tests=()
while IFS=$'\t' read -r name dir; do
    has_tests[$name]=
    while IFS= read -r path; do
        if [[ $path == "$dir"/* ]]; then has_tests[$name]=1; break; fi
    done <<< "$tests_at_head"
done < <(jq -r --arg root "$PWD/" '.packages[] | "\(.name)\t\(.manifest_path | ltrimstr($root) | rtrimstr("/Cargo.toml"))"' <<< "$meta")

exempt=0
for line in ${lines[@]+"${lines[@]}"}; do
    binary=${line%% *}
    [ -n "$line" ] && [ "$binary" != "$line" ] || refuse "an unreadable failure line: $line"
    pkg=${binary%%::*}
    [[ -v has_tests[$pkg] ]] || refuse "a failure in $pkg, a package this workspace does not have: $line"
    [ -n "${has_tests[$pkg]}" ] || exempt=$((exempt + 1))
done
[ "${#lines[@]}" -le "$failed" ] || failed=${#lines[@]}
f=$((failed - exempt))
t=$((run - exempt))
if [ "$f" -gt "$floor" ] && [ $((f * 2)) -ge "$t" ]; then
    refuse "$f of $t tests failed in packages that had tests at HEAD: more than $floor and at least half, which is the environment's doing (disk, memory, a lost file), not a commit's red tests"
fi
exit 0
