//go:build mutation

// Package mutation runs a mechanical mutation-vs-CUCUMBER measurement: for
// each entry in mutationTargets it mutates ONE source file and, for every
// mutant, rebuilds the ctxloom BINARY from the mutated source and drives the
// acceptance features that CLAIM to cover that file, via
// github.com/gtramontina/ooze's WithTestCommand.
//
// The (file -> covering features) pairing is DATA — the mutationTargets table
// below — not code: adding a target is one table entry, and every scoping,
// sanity-check and subtest mechanism below reads the entry it is running
// rather than a package-level constant. That is deliberate. The single
// hardcoded pair this file started as could measure exactly one file, and
// "add another" meant editing the scoping logic, which is the one part that
// must not be edited casually: if scoping breaks, ooze mutates the whole
// module and the run becomes both meaningless and enormous.
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
// SCOPE: the mutated unit is a whole FILE — ooze's public API has no way to
// restrict mutation to a line range within one (only per-FILE
// inclusion/exclusion via IgnoreSourceFiles). Where an entry's interest is
// narrower than its file (the trust entry cares about the EffectiveTrust
// cascade and Reason(), not SetItemTrust/SetBlacklist/TrustStamper), the
// extra mutants are a superset this scoping cannot avoid; they sit in the
// same file, and their survivors are still reported, so the run's summary
// has to say which survivors fall inside vs. outside the named mechanism.
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

// mutationTarget pairs ONE source file with the acceptance feature files
// that CLAIM to cover it. It is the whole configuration surface of this
// harness: everything else here derives from the entry being run.
//
// The pairing is a CLAIM under test, and a wrong one is worse than no entry
// at all. Naming features that do not actually drive the file produces a
// wall of survivors that says nothing about the code (only about the
// pairing), after an hour or more of wall-clock — every mutant is a full
// rebuild plus a scoped suite run, measured at ~28s. Each entry below
// therefore carries the evidence for its pairing, not just an assertion.
type mutationTarget struct {
	// Name is the subtest name, so a failure names the target and so a
	// single entry can be run alone:
	//   go test -tags mutation -run 'TestAcceptanceMutation/^bundle_sign$' ./tests/mutation/...
	// Keep it a single token — no slashes, no spaces — or -run's own
	// slash-separated grammar will not address it.
	Name string
	// SourceRelPath is the SOLE file ooze may mutate for this entry,
	// slash-separated and relative to the module root. Every other .go file
	// discovered under repoRoot() is added to the ignore alternation by
	// buildIgnorePattern.
	SourceRelPath string
	// Features are ACCEPTANCE_PATHS entries (relative to tests/acceptance,
	// which is run_scoped_suite.sh's cwd for the suite) naming the feature
	// files that claim to cover SourceRelPath.
	Features []string
}

// trustCascadeTarget is the entry with a MEASURED result: 132 mutants, 81
// killed, 51 survived, score 0.61 (63 minutes, 2026-08-07). It is named
// separately because the guard-virus test below and
// TestGuardNegate_MatchesRealTrustGo both aim at trust.go specifically
// rather than at whatever happens to be first in the table.
//
// Its three features claim exhaustive coverage of the EffectiveTrust
// cascade: approve/deny for every item kind, rejection beating a trusted
// signer, retraction, and fail-closed pending. A mutant that breaks the
// cascade and none of these three notice is a hollow SECURITY claim.
var trustCascadeTarget = mutationTarget{
	Name:          "trust_cascade",
	SourceRelPath: "internal/operations/trust.go",
	Features: []string{
		"features/trust_surface.feature",
		"features/j001500_corporate_signed.feature",
		"features/j001700_incident.feature",
	},
}

// mutationTargets is the table. ADDING A TARGET IS A DATA CHANGE — no
// scoping code below is target-specific.
//
// Two rules for a new entry, both learned the expensive way:
//
//  1. Name features you can show DRIVE the file, by census, not by theme.
//     Every pairing below was checked by grepping the whole feature corpus
//     for the CLI surface the file implements and confirming the named
//     features are where those invocations actually live.
//  2. Check the file has live callers at all. internal/operations/
//     lockfile.go was a candidate here (LockDependencies, paired with the
//     remote features) and was REJECTED: `grep -rn "LockDependencies("
//     internal` finds no caller outside its own file, so no feature can
//     reach it and every mutant would survive — a full run reporting a
//     score of 0.0 about nothing.
var mutationTargets = []mutationTarget{
	trustCascadeTarget,
	{
		// `ctxloom sign` / `ctxloom bundle sign`: ResolveSignTarget,
		// SignBundleFile, signBundleTree, ListLocalBundleNames — reached
		// from internal/cli/sign.go (resolveSignTargets ->
		// ListLocalBundleNames for --all; operations.SignBundleFile per
		// target) and internal/cli/bundle_push_cli.go.
		//
		// EVIDENCE: of the 12 `ctxloom sign`/`bundle sign` occurrences in
		// the entire feature corpus, 11 are in j001600_signing.feature and the
		// 12th is a COMMENT in j001900_diagnosis.feature. j001600 is not merely the
		// best claimant, it is the only one. Its 16 scenarios (all of them
		// live under the default tag filter — the file carries @doc and
		// nothing else) name every branch of this file: sign by bare name,
		// sign a fragment and get its containing bundle, --all, re-sign a
		// directory bundle beside its manifest, refuse a key the repo does
		// not authorise, and refuse --all with nothing to sign rather than
		// report success over zero bytes.
		Name:          "bundle_sign",
		SourceRelPath: "internal/operations/sign.go",
		Features:      []string{"features/j001600_signing.feature"},
	},
	{
		// `ctxloom signer trust|show|list|delete`: AddSigner,
		// ListSigners, ShowSigner, RemoveSigner, and the allowed_signers
		// line editing beneath them (appendAllowedSignersLine,
		// removeFromAllowedSignersFile, suppressEmbeddedPrincipal) —
		// reached from internal/cli/signer.go.
		//
		// EVIDENCE: `trust signer` appears in exactly two feature files;
		// all three occurrences in j001900_diagnosis.feature are COMMENTS, so
		// j001600_signing.feature holds every real invocation. Its scenarios
		// cover the create/show/delete round trip across BOTH stores
		// (project and user), the fingerprint and namespace rendering, and
		// the removal count.
		//
		// A SEPARATE entry from bundle_sign despite sharing a feature file:
		// ooze mutates one file per release, and this is the file that
		// decides WHICH KEYS ARE TRUSTED — a different security question
		// from whether a signature was written. Sharing the feature scope
		// means the two runs cost the same suite per mutant.
		Name:          "signer_store",
		SourceRelPath: "internal/operations/signer.go",
		Features:      []string{"features/j001600_signing.feature"},
	},
	{
		// Workspace/runtime AXIS RESOLUTION: Axes, WantsWorktree,
		// WantsContainer, chainFor, Prepare, prepareChain, WorkspaceNames,
		// RuntimeNames, warnUnknownAxes — the code that decides which
		// isolation policy chain a run gets.
		//
		// EVIDENCE: j002200_isolation.feature is the axis matrix. 30 of the 47
		// `workspace` mentions across the corpus are in it, and its
		// scenarios are written directly against this file's decisions:
		// "The same run lands in the workspace its axis dictates"
		// (chainFor/Prepare), "Requesting a container with no runtime fails
		// loud, or degrades under --degraded" (WantsContainer plus the
		// no-runtime hint), and three scenarios on workspace "none".
		// isolation_probe.feature is deliberately NOT listed: it is @live,
		// so the default tag filter (~@live) drops every scenario in it and
		// naming it would add a feature file that contributes zero
		// executed steps. All 15 of j002200's scenarios do run by default.
		//
		// Expect survivors in this file's container-image and mount
		// plumbing: the suite drives a recording spy, not a real engine or
		// a container runtime. That is a true statement about the
		// acceptance suite's reach, which is the measurement.
		Name:          "isolation_axes",
		SourceRelPath: "internal/lm/isolation/isolation.go",
		Features:      []string{"features/j002200_isolation.feature"},
	},
}

// minIgnoredFiles is the floor for buildIgnorePattern's ignored count. An
// unscoped ooze run over this module would enumerate ~830 non-test .go files
// across internal/, cmd/, tests/, container/, examples/, prototypes/ — the
// scope-blindness that gave gremlins 24,000 phantom mutants from stray agent
// worktrees. Every entry ignores "the whole module minus one file", so the
// count must be in the hundreds; anything smaller means the walk saw a
// handful of files and repoRoot() resolved somewhere wrong.
const minIgnoredFiles = 400

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
// sanity-check the scope before running anything.
//
// targetRelPath is a PARAMETER, per entry of mutationTargets, not a package
// constant: this is the load-bearing part of the harness, and scoping it to
// the wrong file is not a loud failure but a silently enormous run over the
// whole module.
func buildIgnorePattern(t *testing.T, root, targetRelPath string) (*regexp.Regexp, int) {
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

// release runs one mutationTarget: it scopes ooze to that entry's file,
// points ACCEPTANCE_PATHS at that entry's features, and releases. extra
// options are appended last so a caller can override the virus set (see
// TestTrustCascadeGuardMutation).
//
// WithMinimumThreshold(0): this measures and reports the real score — it does
// NOT gate on one, per the standing rule that choosing a threshold before
// measuring is how this project's mutation gate got tuned into measuring
// nothing four separate times.
func (m mutationTarget) release(t *testing.T, extra ...ooze.Option) {
	t.Helper()

	root := repoRoot(t)

	ignorePattern, ignoredCount := buildIgnorePattern(t, root, m.SourceRelPath)
	if ignoredCount < minIgnoredFiles {
		t.Fatalf("only %d files were queued for ignoring — expected several hundred (whole module minus %s); repoRoot() likely resolved to the wrong directory: %s", ignoredCount, m.SourceRelPath, root)
	}
	t.Logf("mutation scope: %s only; %d other .go files explicitly ignored", m.SourceRelPath, ignoredCount)

	testCmd := "sh " + filepath.ToSlash(filepath.Join("tests", "mutation", "run_scoped_suite.sh"))

	// ACCEPTANCE_PATHS is read by tests/acceptance/acceptance_test.go. Setting
	// it here (rather than inside run_scoped_suite.sh) keeps the actual scope
	// decision in this reviewable Go file; cmdtestrunner.Test forwards
	// os.Environ() (including this) to every mutant's subprocess.
	//
	// t.Setenv, not os.Setenv: entries run as sequential SUBTESTS now, and a
	// process-global left set by one entry would silently become the next
	// entry's scope if that entry ever failed to set its own. t.Setenv
	// restores the previous value when the subtest ends. (It also forbids
	// t.Parallel, which this must never be: mutants are already run one at a
	// time and each one rebuilds the binary.)
	t.Setenv("ACCEPTANCE_PATHS", strings.Join(m.Features, ","))

	t.Logf("test command: %s", testCmd)
	t.Logf("scoped features: %s", strings.Join(m.Features, ", "))

	opts := []ooze.Option{
		ooze.WithRepositoryRoot(root),
		ooze.IgnoreSourceFiles(ignorePattern.String()),
		ooze.WithTestCommand(testCmd),
		ooze.WithMinimumThreshold(0),
	}
	ooze.Release(t, append(opts, extra...)...)
}

// TestAcceptanceMutation releases ooze against each entry of mutationTargets
// in turn, one subtest per entry, driving that entry's scoped cucumber suite
// as the mutant test command.
//
// COST: every mutant is a full rebuild plus a scoped suite run, measured at
// ~28s; the trust_cascade entry alone is 132 mutants ≈ 63 minutes. Running
// the whole table is a multi-hour job. Run one entry with
// -run 'TestAcceptanceMutation/^bundle_sign$'.
//
// This was TestTrustCascadeMutation, singular, when the harness could only
// measure one file.
func TestAcceptanceMutation(t *testing.T) {
	for _, target := range mutationTargets {
		t.Run(target.Name, func(t *testing.T) {
			target.release(t)
		})
	}
}

// TestTrustCascadeGuardMutation is the measurement that actually answers the
// security question, and it exists because the stock run above CANNOT.
//
// The stock viruses mutate comparisons and arithmetic only. Three of the
// EffectiveTrust steps — REJECTED, RETRACTED, APPROVED — are plain boolean
// guards with no comparison in them, so the stock run emits zero mutants
// against them (verified: 0 of 114). A green "no survivors in the cascade"
// from that run therefore means "the tool never attacked the cascade", not
// "the cascade is covered" — exactly the kind of comfortable non-measurement
// this project has been burned by four times.
//
// This test releases ONLY guardNegate (see guard_virus.go), which negates
// each cascade guard in turn — one mutant per step, each a direct assault on
// a single step of the deny cascade. A survivor here is a mechanism the
// journeys CLAIM to enforce and do not.
//
// It stays aimed at trustCascadeTarget specifically rather than ranging over
// mutationTargets: guardNegate's targets are trust.go's cascade conditions by
// literal source text, and releasing it against any other entry's file would
// match nothing and assert nothing.
func TestTrustCascadeGuardMutation(t *testing.T) {
	v := newGuardNegate()
	trustCascadeTarget.release(t, ooze.WithViruses(v))
	// A clean mutation report from the line above is not evidence
	// this virus actually attacked all the cascade guards — it could mean
	// a refactor silently moved a guard's rendered source text out from
	// under cascadeGuards' literal keys, so ooze walked the file and this
	// virus emitted zero mutants for that step. Fail loud instead of
	// reading a floor-less "no survivors" as coverage.
	v.AssertAllTargetsMatched(t)
}
