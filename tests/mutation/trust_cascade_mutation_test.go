//go:build mutation

// Package mutation runs a mechanical mutation-vs-CUCUMBER measurement: it
// mutates internal/operations/trust.go (the EffectiveTrust cascade and the
// withheld-reason surface, Reason()) and, for every mutant, rebuilds the
// ctxloom BINARY from the mutated source and drives the acceptance suite
// against it via github.com/gtramontina/ooze's WithTestCommand.
//
// WHY THIS EXISTS (do not re-litigate):
// gremlins mutates source then runs `go test`, but the acceptance suite
// drives a PRE-BUILT ctxloom binary via exec.Command
// (tests/integration/testenv/environment.go). A gremlins mutant compiled
// into that already-built binary is not there — the mutant can never be
// killed even in principle, and gremlins measured exactly that: 92 mutants
// on this same file, 0 runnable, 92 NOT COVERED, while 16 real scenarios
// were passing.
//
// ooze's laboratory.Test symlinks the whole repo into a tmpdir, overwrites
// ONLY the mutated file with real mutated bytes at that path (never the
// source tree — see internal/fsrepository/fstemporaryrepository.go
// Overwrite: os.Remove on a symlink removes the LINK, not the target), and
// runs the configured WithTestCommand with that tmpdir as cwd. Our test
// command (run_scoped_suite.sh) builds ctxloom FROM the mutated tree and
// then runs the scoped cucumber suite against that freshly built binary —
// the mutant reaches the subprocess under test.
//
// SCOPE: the mutated FILE is the whole of trust.go — ooze's public API has
// no way to restrict mutation to a line range within one file (only
// per-FILE inclusion/exclusion via IgnoreSourceFiles). The task's interest
// is specifically the EffectiveTrust cascade (~:230-312) and Reason()
// (~:159-177); mutating the rest of the file (SetItemTrust, SetBlacklist,
// trust.ParseSelector, computeItemPayload*, TrustStamper, ...) is a superset
// this scoping cannot avoid, but it is inside the same trust-decision file
// and its survivors are still reported — see the mutation report at
// tests/mutation/REPORT (or the invoking agent's summary) for which
// survivors fall inside vs. outside the named cascade.
//
// EVERY OTHER .go FILE IN THE MODULE IS EXCLUDED, PROGRAMMATICALLY: ooze's
// file discovery is a raw filepath.WalkDir with no go-list/module
// awareness (github.com/gtramontina/ooze/internal/fsrepository
// ListGoSourceFiles) — the exact blindness that gave gremlins 24,000
// phantom mutants from stray agent worktrees. Go's regexp is RE2 (no
// lookahead), so "ignore everything except X" cannot be written directly;
// the ignore pattern below is instead BUILT by walking the repo and
// alternating every non-target .go file, quoted, so the scope is explicit
// and reviewable in this file rather than hidden in a hand-written regex.
package mutation

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/gtramontina/ooze"
)

// targetRelPath is the SOLE file ooze is permitted to mutate. Every other
// .go file discovered under repoRoot() is added to the ignore alternation
// by buildIgnorePattern.
const targetRelPath = "internal/operations/trust.go"

// scopedFeatures narrows the acceptance suite (via ACCEPTANCE_PATHS, read by
// tests/acceptance/acceptance_test.go) to the feature files that claim
// exhaustive coverage of the EffectiveTrust cascade: approve/deny for every
// item kind, rejection beating a trusted signer, retraction, and fail-closed
// pending. A mutant that breaks the cascade and none of these three notice is
// a hollow SECURITY claim.
var scopedFeatures = []string{
	"features/trust_surface.feature",
	"features/j3_corporate_signed.feature",
	"features/j7_incident.feature",
}

// repoRoot locates the module root by walking up from this file's own
// location (tests/mutation/) rather than shelling out — this file lives at
// <root>/tests/mutation/trust_cascade_mutation_test.go.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not determine this file's path via runtime.Caller")
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(thisFile)))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("computed repo root %q does not contain go.mod: %v", root, err)
	}
	return root
}

// buildIgnorePattern walks root, collects every non-test .go file (mirroring
// ooze's own fsrepository.ListGoSourceFiles: skip directories, skip files not
// ending ".go", skip files ending "_test.go"), and returns a regexp matching
// every one of them EXCEPT targetRelPath — built as an explicit alternation
// of regexp.QuoteMeta'd relative paths, never a hand-authored "not X"
// pattern (Go's regexp is RE2: no lookahead, so that cannot be expressed
// directly). Also returns the count of ignored files, so the caller can
// sanity-check the scope before running anything (see TestMain).
func buildIgnorePattern(t *testing.T, root string) (*regexp.Regexp, int) {
	t.Helper()

	var others []string
	foundTarget := false

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == targetRelPath {
			foundTarget = true
			return nil
		}
		others = append(others, regexp.QuoteMeta(rel))
		return nil
	})
	if err != nil {
		t.Fatalf("walking repo root %q: %v", root, err)
	}
	if !foundTarget {
		t.Fatalf("target file %q was not found under repo root %q — refusing to build an ignore pattern that might silently mutate nothing", targetRelPath, root)
	}

	sort.Strings(others)
	pattern := "^(" + strings.Join(others, "|") + ")$"

	re, err := regexp.Compile(pattern)
	if err != nil {
		t.Fatalf("compiling ignore pattern: %v", err)
	}
	return re, len(others)
}

// TestTrustCascadeMutation releases ooze against internal/operations/trust.go
// only (see package doc), driving the scoped cucumber suite as the mutant
// test command. WithMinimumThreshold(0): this measures and reports the real
// score — it does NOT gate on one, per the standing rule that choosing a
// threshold before measuring is how this project's mutation gate got tuned
// into measuring nothing four separate times.
func TestTrustCascadeMutation(t *testing.T) {
	root := repoRoot(t)

	ignorePattern, ignoredCount := buildIgnorePattern(t, root)

	// Sanity check per the known trap: an unscoped ooze run over this module
	// would enumerate ~565 non-test .go files across internal/, cmd/,
	// third_party/, tests/, container/, examples/, prototypes/ — the same
	// scope-blindness that gave gremlins 24,000 phantom mutants. We expect
	// "everything except one file" here, so the ignored count must be large
	// (hundreds) and must NOT be small enough to suggest the walk silently
	// only saw a handful of files (e.g. a wrong repoRoot).
	if ignoredCount < 400 {
		t.Fatalf("only %d files were queued for ignoring — expected several hundred (whole module minus %s); repoRoot() likely resolved to the wrong directory: %s", ignoredCount, targetRelPath, root)
	}
	t.Logf("mutation scope: %s only; %d other .go files explicitly ignored", targetRelPath, ignoredCount)

	testCmd := "sh " + filepath.ToSlash(filepath.Join("tests", "mutation", "run_scoped_suite.sh"))

	// ACCEPTANCE_PATHS is read by tests/acceptance/acceptance_test.go. Setting
	// it here (rather than inside run_scoped_suite.sh) keeps the actual scope
	// decision in this reviewable Go file; cmdtestrunner.Test forwards
	// os.Environ() (including this) to every mutant's subprocess.
	if err := os.Setenv("ACCEPTANCE_PATHS", strings.Join(scopedFeatures, ",")); err != nil {
		t.Fatalf("setting ACCEPTANCE_PATHS: %v", err)
	}

	t.Logf("test command: %s", testCmd)
	t.Logf("scoped features: %s", strings.Join(scopedFeatures, ", "))

	ooze.Release(t,
		ooze.WithRepositoryRoot(root),
		ooze.IgnoreSourceFiles(ignorePattern.String()),
		ooze.WithTestCommand(testCmd),
		ooze.WithMinimumThreshold(0),
	)
}

// TestTrustCascadeGuardMutation is the measurement that actually answers the
// security question, and it exists because the stock run above CANNOT.
//
// The stock viruses mutate comparisons and arithmetic only. Five of the seven
// EffectiveTrust steps — REJECTED, RETRACTED, LOCAL, BUILTIN, APPROVED — are
// plain boolean guards with no comparison in them, so the stock run emits zero
// mutants against them (verified: 0 of 114). A green "no survivors in the
// cascade" from that run therefore means "the tool never attacked the
// cascade", not "the cascade is covered" — exactly the kind of comfortable
// non-measurement this project has been burned by four times.
//
// This test releases ONLY guardNegate (see guard_virus.go), which negates each
// of those five guards in turn — five mutants, each one a direct assault on a
// single step of the deny cascade. A survivor here is a mechanism the journeys
// CLAIM to enforce and do not.
func TestTrustCascadeGuardMutation(t *testing.T) {
	root := repoRoot(t)

	ignorePattern, ignoredCount := buildIgnorePattern(t, root)
	if ignoredCount < 400 {
		t.Fatalf("only %d files queued for ignoring — repoRoot() likely wrong: %s", ignoredCount, root)
	}

	testCmd := "sh " + filepath.ToSlash(filepath.Join("tests", "mutation", "run_scoped_suite.sh"))
	if err := os.Setenv("ACCEPTANCE_PATHS", strings.Join(scopedFeatures, ",")); err != nil {
		t.Fatalf("setting ACCEPTANCE_PATHS: %v", err)
	}

	v := newGuardNegate()
	ooze.Release(t,
		ooze.WithRepositoryRoot(root),
		ooze.IgnoreSourceFiles(ignorePattern.String()),
		ooze.WithTestCommand(testCmd),
		ooze.WithViruses(v),
		ooze.WithMinimumThreshold(0),
	)
	// A clean mutation report from the line above is not evidence
	// this virus actually attacked all five cascade guards — it could mean
	// a refactor silently moved a guard's rendered source text out from
	// under cascadeGuards' literal keys, so ooze walked the file and this
	// virus emitted zero mutants for that step. Fail loud instead of
	// reading a floor-less "no survivors" as coverage.
	v.AssertAllTargetsMatched(t)
}
