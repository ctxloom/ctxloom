//go:build mutation

// Package mutation mutates ONE source file per target and measures what
// notices, via github.com/gtramontina/ooze's WithTestCommand.
//
// TWO JUDGES, and the split is the whole design — see the judge interface.
// mutationTargets pairs a file with the acceptance features that CLAIM to cover
// it, rebuilding the ctxloom BINARY per mutant; a survivor is a mechanism a
// feature claims and does not verify. unitMutationTargets pairs a file with the
// SINGLE test that claims to verify it; a run where nothing dies means that test
// is vacuous. They differ in cost by roughly three orders of magnitude, so they
// are separate tables with separate recipes and only the acceptance one is
// ratcheted.
//
// The pairing is DATA — the tables below — not code: adding a target is one
// table entry, and every scoping,
// sanity-check and subtest mechanism below reads the entry it is running
// rather than a package-level constant. That is deliberate. The single
// hardcoded pair this file started as could measure exactly one file, and
// "add another" meant editing the scoping logic, which is the one part that
// must not be edited casually: if scoping breaks, ooze mutates the whole
// module and the run becomes both meaningless and enormous.
//
// THE INVARIANT (do not re-litigate): acceptance mutation runs through ooze
// ONLY, via `just test-mutation-acceptance`. There is no gremlins profile for
// the acceptance suite and there must not be one.
//
// gremlins is COVERAGE-GATED: it mutates source, runs `go test`, and scores
// a mutant only where Go's coverage instrumentation says the mutated line was
// executed by the test process. The acceptance suite executes almost nothing
// in its own process — it execs a PRE-BUILT ctxloom binary via exec.Command
// (tests/integration/testenv/environment.go). The mutant is therefore never
// present in the process under test, and Go coverage cannot observe across
// the exec boundary, so every mutant reports NOT COVERED while real scenarios
// pass. That is a property of how the suite is built, not a misconfiguration:
// no exclusion list, include list, coverage flag or threshold changes it, and
// a gremlins profile aimed at this suite can only ever measure nothing or go
// vacuously green over an empty mutant set.
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
// narrower than its file (the trust entry cares about Admit's cascade, not
// the Trust constructors or the withheld-ref bookkeeping beside it), the
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
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/gtramontina/ooze"

	"github.com/ctxloom/ctxloom/internal/testsupport/dockergate"
	"github.com/ctxloom/ctxloom/internal/testsupport/sourcedir"
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
	// Judge decides killed-vs-survived for every mutant of SourceRelPath.
	// It is a field rather than a hard-coded command because the two judges
	// answer different questions at costs three orders of magnitude apart —
	// see the judge interface.
	Judge judge
}

// judge decides the verdict for one mutant: it names the command ooze runs and
// prepares whatever that command reads.
//
// Two exist, and the split is the point. acceptanceJudge asks "does the suite
// that CLAIMS to cover this file actually verify it", rebuilding the binary and
// driving cucumber (~28s/mutant). unitJudge asks "does the test I just wrote
// look at the code it names", running one `go test -run` (~6s/mutant).
//
// A judge scoped WIDER than one test cannot answer unitJudge's question at all:
// it reports KILLED when ANY test catches the mutant, so a vacuous new test
// beside an older one that already covered the line is indistinguishable from a
// working one.
type judge interface {
	// testCommand is the ooze WithTestCommand string, relative to the
	// laboratory root.
	testCommand() string
	// prepare sets the environment its runner script reads. It takes *testing.T
	// and uses t.Setenv deliberately: entries run as sequential subtests, and a
	// process-global left set by one entry would silently become the next
	// entry's scope if that entry failed to set its own.
	prepare(t *testing.T)
	// label describes the judge for the run log.
	label() string
	// validate reports whatever would make this judge measure NOTHING while
	// still reporting a score. Each judge owns its own checks because each
	// fails differently, and both failures are silent.
	validate(t *testing.T, root string)
}

// acceptanceJudge rebuilds ctxloom from the mutant and runs the acceptance
// features that claim to cover the mutated file. Features are ACCEPTANCE_PATHS
// entries relative to tests/acceptance, which is run_scoped_suite.sh's cwd.
//
// The TAG SELECTION is the suite's hermetic default, which excludes
// @container. WithContainer keeps the @container scenarios of Features in the
// run: without it, a mutant that only a real container launch can notice
// survives by construction, because no scenario that could kill it is
// selected. It is opt-in per entry because it makes the MEASURING HOST need a
// reachable container runtime and costs an image build per mutant.
type acceptanceJudge struct {
	Features      []string
	WithContainer bool
}

func (a acceptanceJudge) testCommand() string {
	return "sh " + filepath.ToSlash(filepath.Join("tests", "mutation", "run_scoped_suite.sh"))
}

// prepare sets the scope here rather than inside run_scoped_suite.sh so the
// scope decision stays in this reviewable Go file; cmdtestrunner.Test forwards
// os.Environ() to every mutant's subprocess, and runInLab does the same for the
// pre-flight, so both run exactly this selection.
//
// Every selection variable is SET, never left to the caller's shell: an
// ACCEPTANCE_TAGS or ACCEPTANCE_INCLUDE_CONTAINER exported for some other lane
// would otherwise silently become this entry's selection.
//
// WithContainer also demands a runtime (dockergate.EnvRequireDocker): without
// one the @container scenario would decline rather than fail, and the
// pre-flight must name the missing runtime instead of the run measuring
// nothing about the container path.
func (a acceptanceJudge) prepare(t *testing.T) {
	t.Helper()
	t.Setenv("ACCEPTANCE_PATHS", strings.Join(a.Features, ","))
	t.Setenv("ACCEPTANCE_TAGS", "")
	include := ""
	if a.WithContainer {
		include = "1"
		t.Setenv(dockergate.EnvRequireDocker, "1")
	}
	t.Setenv("ACCEPTANCE_INCLUDE_CONTAINER", include)
}

// validate catches the acceptance judge's two silent failures: no features at
// all means ACCEPTANCE_PATHS is empty, so the suite runs EVERYTHING and the run
// never finishes; a feature path that does not exist means godog runs zero
// scenarios, every mutant survives, and an hour of rebuilding reports 0.0 about
// nothing.
func (a acceptanceJudge) validate(t *testing.T, root string) {
	t.Helper()

	if len(a.Features) == 0 {
		t.Errorf("names no features — ACCEPTANCE_PATHS would be empty, the suite would run EVERYTHING, and the run would never finish")
	}
	for _, feature := range a.Features {
		// Relative to tests/acceptance — godog's cwd, per run_scoped_suite.sh.
		path := filepath.Join(root, "tests", "acceptance", filepath.FromSlash(feature))
		if _, err := os.Stat(path); err != nil {
			t.Errorf("feature %q does not exist at %s: %v — the suite would run zero scenarios and every mutant would survive", feature, path, err)
		}
	}
	if a.WithContainer && !anyFeatureTagged(root, a.Features, "@container") {
		t.Errorf("WithContainer is set but none of %v carries a @container scenario — the container selection would add nothing", a.Features)
	}
}

// anyFeatureTagged reports whether any feature file (relative to
// tests/acceptance) has a TAG LINE carrying tag. Only lines that start with
// '@' count: a comment that merely mentions the tag selects nothing.
func anyFeatureTagged(root string, features []string, tag string) bool {
	for _, feature := range features {
		b, err := os.ReadFile(filepath.Join(root, "tests", "acceptance", filepath.FromSlash(feature)))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(b), "\n") {
			if fields := strings.Fields(line); len(fields) > 0 && strings.HasPrefix(fields[0], "@") && slices.Contains(fields, tag) {
				return true
			}
		}
	}
	return false
}

func (a acceptanceJudge) label() string {
	l := "acceptance features: " + strings.Join(a.Features, ", ")
	if a.WithContainer {
		l += " — INCLUDING their @container scenarios, so this host needs a reachable container runtime (" + dockergate.EnvRequireDocker + "=1 makes its absence a failure)"
	}
	return l
}

// unitJudge runs exactly one test. Run should address a single test, or the
// verdict stops meaning "this test kills it".
type unitJudge struct {
	Pkg string
	Run string
}

func (u unitJudge) testCommand() string {
	return "sh " + filepath.ToSlash(filepath.Join("tests", "mutation", "run_unit_judge.sh"))
}

func (u unitJudge) prepare(t *testing.T) {
	t.Helper()
	t.Setenv("MUT_PKG", u.Pkg)
	t.Setenv("MUT_RUN", u.Run)
}

// validate catches the unit judge's silent failure: an UNANCHORED Run matches
// every test whose name contains it, so the verdict silently stops meaning
// "this test kills the mutant" and starts meaning "some test does" — which is
// the exact distinction this judge exists to draw. An empty Pkg or Run would
// judge far more than intended.
func (u unitJudge) validate(t *testing.T, root string) {
	t.Helper()

	if u.Pkg == "" {
		t.Errorf("names no package — the judge would run the whole module")
	}
	if u.Run == "" {
		t.Errorf("names no test — the judge would run every test in the package, and a kill would not be attributable to any one of them")
	}
	if u.Run != "" && (!strings.HasPrefix(u.Run, "^") || !strings.HasSuffix(u.Run, "$")) {
		t.Errorf("Run %q is not anchored with ^...$ — it would match every test whose name contains it, so a kill would no longer mean THIS test caught the mutant", u.Run)
	}
}

func (u unitJudge) label() string {
	return "go test -run " + u.Run + " " + u.Pkg
}

// trustCascadeTarget is named separately because the guard-virus test below
// and TestGuardNegate_MatchesRealTrustGo both aim at the cascade file
// specifically rather than at whatever happens to be first in the table.
//
// The file is the one holding the DECISION — (*authorizer).Admit and the
// helpers it asks (retractionVerdict, localReason, pendingReason) — not
// operations' TrustStamper, which only adapts Admit's verdict to the CLI.
//
// Its three features claim exhaustive coverage of that cascade:
// approve/deny for every item kind, rejection beating a trusted signer,
// retraction, and fail-closed pending. A mutant that breaks the cascade and
// none of these three notice is a hollow SECURITY claim.
var trustCascadeTarget = mutationTarget{
	Name:          "trust_cascade",
	SourceRelPath: "internal/core/composite/trust.go",
	Judge: acceptanceJudge{Features: []string{
		"features/journeys/trust_surface.feature",
		"features/journeys/j001500_corporate_signed.feature",
		"features/journeys/j001700_incident.feature",
	}},
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
//  2. Check the file has live callers at all. internal/adapters/operations/
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
		// from internal/adapters/cli/sign.go (resolveSignTargets ->
		// ListLocalBundleNames for --all; operations.SignBundleFile per
		// target) and internal/adapters/cli/bundle_push_cli.go.
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
		SourceRelPath: "internal/adapters/operations/sign.go",
		Judge:         acceptanceJudge{Features: []string{"features/journeys/j001600_signing.feature"}},
	},
	{
		// `ctxloom signer trust|show|list|delete`: AddSigner,
		// ListSigners, ShowSigner, RemoveSigner, and the allowed_signers
		// line editing beneath them (appendAllowedSignersLine,
		// removeFromAllowedSignersFile, suppressEmbeddedPrincipal) —
		// reached from internal/adapters/cli/signer.go.
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
		SourceRelPath: "internal/adapters/operations/signer.go",
		Judge:         acceptanceJudge{Features: []string{"features/journeys/j001600_signing.feature"}},
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
		// executed steps.
		//
		// WithContainer: the container-only guards in this file are reachable
		// only by j002200's @container scenario, which really launches the
		// mock engine in a container. The default filter excludes it, and
		// without it those guards would survive by construction rather than
		// by any gap in what the feature verifies.
		//
		// Expect survivors elsewhere in the container-image and mount
		// plumbing: the other scenarios drive a recording spy, not a real
		// engine. That is a true statement about the acceptance suite's
		// reach, which is the measurement.
		Name:          "isolation_axes",
		SourceRelPath: "internal/adapters/isolation/isolation.go",
		Judge: acceptanceJudge{
			Features:      []string{"features/journeys/j002200_isolation.feature"},
			WithContainer: true,
		},
	},
	{
		// The remote REGISTRY: Add, Update, Remove, Get, List, SetDefault,
		// GetDefault, SetForge, GetOrCreateByURL, ResolveItemRemote and the
		// load/save pair beneath them — the code that decides what
		// .ctxloom/remotes.yaml says, and therefore which address every
		// dependency operation resolves to.
		//
		// EVIDENCE, by census of `ctxloom remote ` across the whole corpus:
		// cli/remote.feature holds 21 of the 44 invocations and cli/deps.feature
		// 15, together 36 of 44. The remaining eight are spread one to five
		// across content_decision, mcp_resources, fault_tolerance and init,
		// none of which drives a registry verb these two do not.
		//
		// BOTH are named because they reach different halves: remote.feature
		// drives the write verbs (create/edit/remove/default) while
		// deps.feature drives the READ path that resolves a remote name to an
		// address during a pull. An entry naming only the first would leave
		// every resolution mutant surviving for want of a caller.
		//
		// Expect survivors in the GitHub-specific and network-adjacent
		// branches: the suite drives seeded file:// repositories, so a mutant
		// reachable only through a real forge API has no scenario that can
		// kill it. That is a true statement about the suite's reach.
		Name:          "remote_registry",
		SourceRelPath: "internal/adapters/remote/registry.go",
		Judge: acceptanceJudge{Features: []string{
			"features/cli/remote.feature",
			"features/cli/deps.feature",
		}},
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
	root, err := sourcedir.RepoRoot()
	if err != nil {
		t.Fatalf("could not determine the module root: %v", err)
	}
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

	testCmd := m.Judge.testCommand()

	m.Judge.prepare(t)

	t.Logf("test command: %s", testCmd)
	t.Logf("judge: %s", m.Judge.label())

	opts := []ooze.Option{
		ooze.WithRepositoryRoot(root),
		ooze.IgnoreSourceFiles(ignorePattern.String()),
		ooze.WithTestCommand(testCmd),
		ooze.WithMinimumThreshold(0),
	}

	// PRE-FLIGHT: the judge must PASS on the unmutated tree, in a laboratory,
	// before any mutant is judged by it. ooze has no baseline run and reads any
	// nonzero exit as a kill, so a laboratory that cannot build — or a judge
	// already red — kills every mutant and reports a perfect score over
	// nothing. That is not detectable from ooze's summary; it is only
	// detectable here. It costs one mutant's run.
	err := preflightThenRelease(root, testCmd, func() {
		// PER-TARGET ATTRIBUTION for the survivor ratchet. ooze's summary box
		// says what it counted and never which target it counted for, so a run
		// of the whole table emits several indistinguishable boxes and no
		// baseline can be applied to any of them. This marker names the one
		// that follows.
		//
		// os.Stdout, not t.Logf: testing buffers a subtest's log until the
		// subtest ends, which is AFTER ooze has summarized in its t.Cleanup.
		// Writing to the same stream ooze's reporter writes to, from this
		// goroutine, immediately before the release, makes marker-then-box an
		// ordering this code establishes rather than one the ratchet has to
		// infer from `go test`'s own bookkeeping lines.
		//
		// t.Name(), not m.Name: TestTrustCascadeGuardMutation releases this
		// same ENTRY under a different virus set, and its mutant set is a
		// different measurement that must not share a baseline row with the
		// stock run.
		fmt.Fprintf(os.Stdout, "\nooze-target: %s\n", t.Name())
		ooze.Release(t, append(opts, extra...)...)
	})
	if err != nil {
		t.Fatalf("pre-flight failed: the judge (%s) does not pass on the UNMUTATED tree, so every mutant would be scored as a kill and the run would measure nothing.\n%v", m.Judge.label(), err)
	}
}

// TestAcceptanceMutation releases ooze against each entry of mutationTargets
// in turn, one subtest per entry, driving that entry's scoped cucumber suite
// as the mutant test command.
//
// COST: every mutant is a full rebuild plus a scoped suite run, measured at
// ~28s, so a single entry is tens of minutes and the whole table is a
// multi-hour job. Run one entry with
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
// cascade steps — REJECTED, RETRACTED, APPROVED — are plain boolean
// guards with no comparison in them, so the stock run emits zero mutants
// against them. A green "no survivors in the cascade"
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
