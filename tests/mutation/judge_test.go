//go:build mutation

package mutation

import (
	"os"
	"strings"
	"testing"
)

// The acceptance judge's whole contract with run_scoped_suite.sh is the
// ACCEPTANCE_PATHS environment variable: the script never learns the scope any
// other way. If prepare stops setting it, ACCEPTANCE_PATHS is empty, godog runs
// the ENTIRE suite for every mutant, and a multi-hour run becomes one that never
// finishes — discovered only by waiting.
//
// Pinned here because the acceptance table itself is far too expensive to run as
// a check on its own wiring.
func TestAcceptanceJudge_PreparesTheScopeTheRunnerReads(t *testing.T) {
	j := acceptanceJudge{Features: []string{"features/a.feature", "features/b.feature"}}
	j.prepare(t)

	got := os.Getenv("ACCEPTANCE_PATHS")
	if got != "features/a.feature,features/b.feature" {
		t.Errorf("ACCEPTANCE_PATHS = %q; the runner reads this and nothing else — an empty or wrong value runs the whole suite per mutant", got)
	}
	if !strings.Contains(j.testCommand(), "run_scoped_suite.sh") {
		t.Errorf("testCommand() = %q; must name the script that rebuilds the binary and drives cucumber", j.testCommand())
	}
}

// The unit judge's contract with run_unit_judge.sh is MUT_PKG/MUT_RUN, and the
// script refuses to run without both — but it refuses per MUTANT, after ooze has
// already built a laboratory. Catching it here is the difference between a typo
// and a run that fails 22 times slowly.
func TestUnitJudge_PreparesTheScopeTheRunnerReads(t *testing.T) {
	j := unitJudge{Pkg: "./internal/example", Run: "^TestSomething$"}
	j.prepare(t)

	if got := os.Getenv("MUT_PKG"); got != "./internal/example" {
		t.Errorf("MUT_PKG = %q, want ./internal/example", got)
	}
	if got := os.Getenv("MUT_RUN"); got != "^TestSomething$" {
		t.Errorf("MUT_RUN = %q, want ^TestSomething$", got)
	}
	if !strings.Contains(j.testCommand(), "run_unit_judge.sh") {
		t.Errorf("testCommand() = %q; must name the single-test runner", j.testCommand())
	}
}
