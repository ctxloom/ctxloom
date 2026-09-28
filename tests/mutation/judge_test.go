//go:build mutation

package mutation

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/testsupport/dockergate"
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

// The tag selection is declared PER ENTRY, and the two sides of the split are
// pinned by name. isolation_axes must run j002200's @container scenario: its
// container-only guards are reachable by nothing else, so under the default
// filter they survive by construction. trust_cascade must NOT: it would gain a
// runtime requirement and an image build per mutant for scenarios it has none
// of — and an ACCEPTANCE_INCLUDE_CONTAINER leaked from the caller's shell must
// not widen it either.
func TestAcceptanceJudge_TagSelectionIsDeclaredPerEntry(t *testing.T) {
	judgeOf := func(t *testing.T, name string) acceptanceJudge {
		t.Helper()
		for _, m := range mutationTargets {
			if m.Name == name {
				j, ok := m.Judge.(acceptanceJudge)
				if !ok {
					t.Fatalf("%s: judge is %T, want acceptanceJudge", name, m.Judge)
				}
				return j
			}
		}
		t.Fatalf("no mutationTargets entry named %s", name)
		return acceptanceJudge{}
	}

	t.Run("isolation_axes includes @container and demands a runtime", func(t *testing.T) {
		judgeOf(t, "isolation_axes").prepare(t)
		if got := os.Getenv("ACCEPTANCE_INCLUDE_CONTAINER"); got != "1" {
			t.Errorf("ACCEPTANCE_INCLUDE_CONTAINER = %q, want 1 — the @container scenario is excluded and the container-only guards cannot be killed", got)
		}
		if got := os.Getenv(dockergate.EnvRequireDocker); got != "1" {
			t.Errorf("%s = %q, want 1 — a missing runtime would decline the container scenario instead of failing the pre-flight", dockergate.EnvRequireDocker, got)
		}
		if got := os.Getenv("ACCEPTANCE_TAGS"); got != "" {
			t.Errorf("ACCEPTANCE_TAGS = %q, want empty — the hermetic default owns the exclusions, and a non-empty value leaves the hermetic lane", got)
		}
	})

	t.Run("trust_cascade does not, even when the shell exports it", func(t *testing.T) {
		t.Setenv("ACCEPTANCE_INCLUDE_CONTAINER", "1")
		t.Setenv("ACCEPTANCE_TAGS", "@live")
		judgeOf(t, "trust_cascade").prepare(t)
		if got := os.Getenv("ACCEPTANCE_INCLUDE_CONTAINER"); got != "" {
			t.Errorf("ACCEPTANCE_INCLUDE_CONTAINER = %q after prepare, want empty — a leaked value widened this entry's selection", got)
		}
		if got := os.Getenv("ACCEPTANCE_TAGS"); got != "" {
			t.Errorf("ACCEPTANCE_TAGS = %q after prepare, want empty — a leaked value replaced this entry's selection", got)
		}
	})

	// The variable name is a binding across a build-tag boundary: nothing
	// compiles one side against the other. If the suite stops reading it, the
	// judge's opt-in silently selects nothing extra.
	t.Run("the suite reads the variable the judge sets", func(t *testing.T) {
		src, err := os.ReadFile(filepath.Join(repoRootFromTest(t), "tests", "acceptance", "acceptance_test.go"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(src), `os.Getenv("ACCEPTANCE_INCLUDE_CONTAINER")`) {
			t.Error(`tests/acceptance/acceptance_test.go no longer reads ACCEPTANCE_INCLUDE_CONTAINER — the judge's container opt-in selects nothing`)
		}
	})
}

// A comment that mentions @container selects no scenario, so it must not
// satisfy WithContainer's validation.
func TestAnyFeatureTagged_CountsTagLinesOnly(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "tests", "acceptance", "features")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("comment.feature", "Feature: x\n  # a scenario tagged @container lives elsewhere\n  Scenario: y\n")
	write("tagged.feature", "Feature: x\n  @container @image-mock-agent\n  Scenario: y\n")

	if anyFeatureTagged(root, []string{"features/comment.feature"}, "@container") {
		t.Error("a comment mentioning @container counted as a tagged scenario")
	}
	if !anyFeatureTagged(root, []string{"features/comment.feature", "features/tagged.feature"}, "@container") {
		t.Error("a real @container tag line was not found")
	}
}
