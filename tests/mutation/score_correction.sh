#!/bin/sh
# Reads a mutation run's output on stdin and, when that run contains mutants
# that DID NOT COMPILE, prints the corrected totals.
#
# WHY THIS IS A SCRIPT AND NOT INLINE IN THE RECIPE: it is the arithmetic that
# decides whether a reported kill count is honest, and shell living inside a
# just recipe cannot be tested. It was inline, and therefore the correction for
# a truthfulness defect was itself unverified.
#
# ooze scores a mutant by the runner's EXIT CODE and nothing else
# (internal/cmdtestrunner: nonzero => killed), and gremlins maps `go test`'s
# exit 1 — which is also what a build failure exits — to KILLED, so in both a
# mutant that fails to build is credited to the test suite. The ooze runners
# emit "ooze-invalid-mutant:" for exactly those, the package lane
# "gremlins-invalid-mutant:"; this subtracts them. gremlins also prints an
# efficacy (killed / killed+lived), restated here from the corrected kills,
# since that percentage is the figure a reader takes away.
#
# SURVIVORS ARE DELIBERATELY UNTOUCHED. An invalid mutant exits nonzero, so it
# lands in Killed, never in Survived — which is why the survivor ratchet and
# every recorded baseline are unaffected by this correction.
#
# It does not decide whether a run measured anything. A target none of whose
# mutants compiled is refused per target by survivor_ratchet.sh, which every
# lane runs; a whole-run sum here cannot see one dead target beside healthy
# ones.
#
# Prints nothing when no invalid mutants are present: a run with nothing to
# correct must not grow a reassuring line saying so.
set -eu

# ANSI colour is stripped once, here: gremlins colours its tally on a TTY.
esc=$(printf '\033')
output=$(sed "s/${esc}\[[0-9;]*[a-zA-Z]//g")

ooze_invalid=$(printf '%s\n' "$output" | grep -c 'ooze-invalid-mutant:' || true)
gremlins_invalid=$(printf '%s\n' "$output" | grep -c 'gremlins-invalid-mutant:' || true)
invalid=$((${ooze_invalid:-0} + ${gremlins_invalid:-0}))
[ "$invalid" -gt 0 ] || exit 0

# Summed over every box and tally: a multi-target run prints one per target,
# and its markers are the markers of all of them. Yields "total killed lived
# found": lived is gremlins' alone, for its efficacy; found is 0 when neither
# tool's summary was seen.
# shellcheck disable=SC2046 # four integers, split on purpose
set -- $(printf '%s\n' "$output" | awk '
    function nums(s, out,   k, m, i, parts) {
        k = 0; m = split(s, parts, /[^0-9]+/)
        for (i = 1; i <= m; i++) if (parts[i] != "") out[++k] = parts[i] + 0
        return k
    }
    # ooze: the summary box, one figure per line.
    /• Total:[[:space:]]+[0-9]+/  { nums($0, g); total += g[1]; found = 1; next }
    /• Killed:[[:space:]]+[0-9]+/ { nums($0, g); killed += g[1]; next }
    # gremlins: two tally lines; the second only completes the total.
    /^[[:space:]]*Killed: [0-9]+, Lived: [0-9]+, Not covered: [0-9]+/ {
        nums($0, g); killed += g[1]; lived += g[2]; total += g[1] + g[2] + g[3]; found = 1; next
    }
    /^[[:space:]]*Timed out: [0-9]+, Not viable: [0-9]+, Skipped: [0-9]+/ {
        nums($0, g); total += g[1] + g[2] + g[3]; next
    }
    END { printf "%d %d %d %d\n", total, killed, lived, found }
')
total=$1 killed=$2 lived=$3 found=$4

# No box to correct: say so rather than printing a half-answer. A run that
# emitted invalid-mutant markers but no summary measured nothing, and the
# no-score guard upstream is what fails it.
if [ "$found" -eq 0 ]; then
  echo
  echo "  ${invalid} mutant(s) DID NOT COMPILE, but no summary box was found to correct."
  exit 0
fi

echo
echo "  ${invalid} mutant(s) DID NOT COMPILE and were scored as killed. Corrected:"
echo "    valid total:  $((total - invalid))"
echo "    real kills:   $((killed - invalid))"
if [ "$gremlins_invalid" -gt 0 ]; then
  awk -v k="$((killed - invalid))" -v l="$lived" 'BEGIN {
    if (k + l > 0) printf "    real efficacy: %.2f%%\n", 100 * k / (k + l)
    else print "    real efficacy: n/a (no mutant was both valid and run)"
  }'
fi
