#!/bin/sh
# Invoked by ooze (see unit_judge_mutation_test.go) as the mutant test command,
# with cwd = ooze's laboratory: a tmpdir where every file is a symlink back to
# the real checkout EXCEPT the one mutated source file, which ooze has
# overwritten with real mutated bytes. The source tree is never written.
#
# The judge is a SINGLE go test, named by MUT_PKG/MUT_RUN, rather than a
# rebuilt binary driving cucumber. That is the whole point of this runner: it
# answers "does THIS test kill this mutant", which a package-wide or
# suite-wide judge cannot — a mutant killed by some other test reports KILLED
# and tells you nothing about the test you just wrote.
#
# go:embed refuses to embed a SYMLINK, which applies to `go test` exactly as it
# does to `go build`: materialize_embeds.sh replaces every embedded file's
# symlink with a real copy before anything compiles.
set -eu

sh "$(dirname "$0")/materialize_embeds.sh"

: "${MUT_PKG:?run_unit_judge.sh: MUT_PKG unset — the judge would run the whole module}"
: "${MUT_RUN:?run_unit_judge.sh: MUT_RUN unset — the judge would run every test in the package}"

# A mutant that does not COMPILE is not a mutant the test caught. ooze's verdict
# is the exit code and nothing else (internal/cmdtestrunner: nonzero => killed),
# so without this the compiler is scored as if it were the test suite and the
# kill count is inflated by mutants no test ever saw.
#
# There is no third verdict to return — result.Result is Ok|Err and lives in
# ooze's internal/ — so the mutant is still reported killed and this marker is
# what lets the reporting layer subtract it. Compile FIRST, separately, so the
# distinction is made by exit status rather than by parsing `go test` prose.
if ! go build -o /dev/null "$MUT_PKG" >/dev/null 2>&1; then
  echo "ooze-invalid-mutant: $MUT_PKG did not compile"
  exit 1
fi

# -count=1 defeats the test cache: a cached PASS from the unmutated build would
# score every mutant as SURVIVED without running anything.
exec go test -count=1 -run "$MUT_RUN" "$MUT_PKG"
