#!/usr/bin/env bash
# The mutation DRIVER: runs the mutation-tagged tests for one LANE and refuses
# any result that must not read as a pass.
#
# Usage: mutation_driver.sh <lane> [go test args...]
#
#   acceptance  ooze over the acceptance table; ratcheted against the baseline
#   package     gremlins over the package table; ratcheted against the baseline
#   unit        ooze judged by one unit test per target; unratcheted
#
# The LANE, not the caller, decides the ratchet's argument, so which lane is
# ratcheted lives here, where mutation_driver_test.go drives it, and not in a
# recipe no test reads. The recipes that call this set the environment only
# they know (TMPDIR, CTXLOOM_VERSION_LDFLAG) and pass the lane through.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
cd "$here/../.."

usage() {
    echo "usage: $0 <acceptance|package|unit> [go test args...]" >&2
    exit 2
}
[ "$#" -ge 1 ] || usage
lane=$1
shift
# The BASELINE follows the GATE, not the tool. An acceptance or package run is a
# scheduled measurement whose whole point is "did it get worse"; the unit judge
# is an authoring-time check you run once and read, for which a baseline is
# meaningless — there is no previous run to have regressed from. An unknown
# lane is refused rather than defaulted: a typo must not select "no baseline".
case "$lane" in
    acceptance | package) baseline=tests/mutation/survivor_baseline.txt ;;
    unit) baseline=--no-baseline ;;
    *) usage ;;
esac

runlog=$(mktemp)
trap 'rm -f "$runlog"' EXIT

set +e
# -v is LOAD-BEARING, not a debugging convenience. ooze prints its per-mutant
# diffs and its summary box to STDOUT, and `go test` swallows a PASSING test's
# stdout. The mutation test uses WithMinimumThreshold(0), so it ALWAYS passes —
# which means without -v this driver runs for an hour and emits one line:
#   ok  github.com/ctxloom/ctxloom/tests/mutation  339.740s
# See taskloom unwanted-deviate.
#
# 240m, not 120m: the whole table's mutants run serially and a full run has
# measured close to 120m on its own, and a timeout mid-table loses the whole
# run's results. Re-measure before lowering it.
output=$(go test -trimpath -tags mutation -v -count=1 -timeout 240m ./tests/mutation/... "$@" 2>&1)
status=$?
set -e
printf '%s\n' "$output"
if [ "$status" -ne 0 ]; then
    exit "$status"
fi
if grep -q '\[no tests to run\]' <<<"$output"; then
    echo "error: -run matched no tests (typo'd or renamed test name?)" >&2
    exit 1
fi
# THE INVARIANT: a mutation run that produced no score has told you nothing,
# and must never read as a clean bill of health. Exit 0 here would be the
# exact failure this gate replaced (gremlins reporting success over an empty
# mutant set).
# ooze reports `Score:`; gremlins (the package lane) reports `Test efficacy:`.
if ! grep -qE 'Score:|Test efficacy:' <<<"$output"; then
    echo "error: the run produced no mutation score — it measured NOTHING." >&2
    echo "       ooze and gremlins print their summaries to stdout; if that is" >&2
    echo "       missing, either no target was released or the output was" >&2
    echo "       swallowed. Do not read this as a pass." >&2
    exit 1
fi
# Repeat the summary AFTER the -v firehose, so the number is not buried
# thousands of scenario lines up.
echo
echo "=== mutation summary ==="
grep -E 'Total:|Killed:|Survived:|Score:|Timed out:|Test efficacy:' <<<"$output" || true
# ooze's box counts a mutant that DID NOT COMPILE as killed: its verdict is
# the runner's exit code and nothing else, so the compiler is scored as if it
# were the test suite. The runners mark those; subtract them here so the
# number reported is over mutants a test could actually have caught.
# Survivors are untouched by this — an invalid mutant never lands there — so
# the ratchet and its baselines are unaffected.
printf '%s\n' "$output" | sh tests/mutation/score_correction.sh
# THE SECOND HALF OF THE SAME INVARIANT: the guard above refuses a run that
# measured nothing; this one refuses a run that measured something WORSE
# than what is already recorded — per target, because one number for the
# whole table lets an improvement in one entry mask a regression in another.
# Its per-target "measured nothing" refusals run in EVERY lane: a target none
# of whose mutants compiled is a dead measurement beside healthy ones in
# either, and only a per-target count can see it.
printf '%s\n' "$output" > "$runlog"
bash tests/mutation/survivor_ratchet.sh "$baseline" "$runlog"
