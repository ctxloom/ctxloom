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
# (internal/cmdtestrunner: nonzero => killed), so a mutant that fails to build
# is credited to the test suite. The runners emit "ooze-invalid-mutant:" for
# exactly those; this subtracts them.
#
# SURVIVORS ARE DELIBERATELY UNTOUCHED. An invalid mutant exits nonzero, so it
# lands in Killed, never in Survived — which is why the survivor ratchet and
# every recorded baseline are unaffected by this correction.
#
# Exits nonzero when NO mutant compiled: that run measured nothing.
#
# Prints nothing when no invalid mutants are present: a run with nothing to
# correct must not grow a reassuring line saying so.
set -eu

output=$(cat)

invalid=$(printf '%s\n' "$output" | grep -c 'ooze-invalid-mutant:' || true)
[ "${invalid:-0}" -gt 0 ] || exit 0

# Summed over every box: a multi-target run prints one per target, and its
# markers are the markers of all of them.
sum_box() {
  printf '%s\n' "$output" | grep -oE "• $1:[[:space:]]+[0-9]+" | grep -oE '[0-9]+$' |
    awk '{ s += $1; n++ } END { if (n) print s }'
}
total=$(sum_box Total)
killed=$(sum_box Killed)

# No box to correct: say so rather than printing a half-answer. A run that
# emitted invalid-mutant markers but no summary measured nothing, and the
# no-score guard upstream is what fails it.
if [ -z "${total:-}" ] || [ -z "${killed:-}" ]; then
  echo
  echo "  ${invalid} mutant(s) DID NOT COMPILE, but no summary box was found to correct."
  exit 0
fi

# Nothing compiled: the box's perfect score is over nothing. This is the one
# step every lane runs (the unit lane has no ratchet), so it fails the run.
if [ "$invalid" -ge "$total" ]; then
  echo
  echo "  error: all ${total} mutant(s) DID NOT COMPILE — the run measured NOTHING." >&2
  echo "         ooze scored each build failure as a kill; the laboratory cannot build" >&2
  echo "         this tree. Read any mutant's output above for the compiler error." >&2
  exit 1
fi

echo
echo "  ${invalid} mutant(s) DID NOT COMPILE and were scored as killed. Corrected:"
echo "    valid total:  $(( total - invalid ))"
echo "    real kills:   $(( killed - invalid ))"
