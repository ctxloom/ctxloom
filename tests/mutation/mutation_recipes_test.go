// No build tag, deliberately: these tests run the REAL public mutation recipes
// through `just`, with a stand-in `gomutants` first on PATH that replays a
// canned report, so the wiring from a recipe to its verdict is checked by an
// ordinary gate rather than only during the hours-long run it decides.
package mutation

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// recipeRun is what one fake mutation run left behind.
type recipeRun struct {
	code int
	out  string
	// argv is what the recipe handed gomutants, one argument per line; empty
	// when gomutants never ran.
	argv string
	// tmpdir is the TMPDIR gomutants ran under; mutationTmp is the recipe's
	// CTXLOOM_MUTATION_TMP.
	tmpdir, mutationTmp string
}

// fakeGomutants is a directory holding a stand-in `gomutants` that records its
// arguments and TMPDIR, writes report to wherever --output asks (nothing when
// report is empty), and exits 0 — which is all a caller sees of gomutants.
//
// It also LEAKS a test temp dir into its TMPDIR, as a test killed mid-run (a
// timed-out mutant, an interrupted lane) does — with a read-only directory
// inside, as a Go module cache leaves, so a plain `rm -rf` cannot remove it.
func fakeGomutants(t *testing.T, report string) string {
	t.Helper()
	dir := t.TempDir()
	if report != "" {
		if err := os.WriteFile(filepath.Join(dir, "report.json"), []byte(report), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	script := "#!/bin/sh\n" +
		"here=$(dirname \"$0\")\n" +
		"prev=''\n" +
		"for a in \"$@\"; do\n" +
		"  if [ \"$prev\" = --output ] && [ -f \"$here/report.json\" ]; then cp \"$here/report.json\" \"$a\"; fi\n" +
		"  prev=$a\n" +
		"done\n" +
		"printf '%s\\n' \"$@\" > \"$here/argv\"\n" +
		"printf '%s' \"${TMPDIR:-}\" > \"$here/tmpdir\"\n" +
		"leak=\"${TMPDIR:-/tmp}/ctxloom-test-sandbox-$$\"\n" +
		"mkdir -p \"$leak/mod\" && touch \"$leak/mod/go.mod\" && chmod 0555 \"$leak/mod\"\n" +
		"echo 'fake gomutants ran'\n"
	if err := os.WriteFile(filepath.Join(dir, "gomutants"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// recipeEnv is the environment a fake run gets: the fake first on PATH, a
// private mutation tmpdir, and NO baseline update mode — the ratchet recipe
// judges the repo's real survivor_baseline.txt, and an inherited
// CTXLOOM_MUTATION_BASELINE=update would let a fake run rewrite it.
//
// JUST_JUSTFILE is set to justfile.container, as CI's container jobs set it,
// on every machine: a recipe that recursed through a bare `just` would resolve
// that justfile instead of the one it was run from, and without this the
// break shows only on CI.
func recipeEnv(fake, mutationTmp string) []string {
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "CTXLOOM_MUTATION_BASELINE=") ||
			strings.HasPrefix(kv, "CTXLOOM_MUTATION_TMP=") ||
			strings.HasPrefix(kv, "JUST_JUSTFILE=") ||
			strings.HasPrefix(kv, "PATH=") {
			continue
		}
		env = append(env, kv)
	}
	return append(env,
		"PATH="+fake+string(os.PathListSeparator)+os.Getenv("PATH"),
		"CTXLOOM_MUTATION_TMP="+mutationTmp,
		"JUST_JUSTFILE=justfile.container")
}

// runRecipe runs a public mutation recipe exactly as a human would, against
// a fake gomutants that hands back report.
func runRecipe(t *testing.T, report string, recipe ...string) recipeRun {
	t.Helper()
	root := repoRootFromTest(t)
	// Everything the recipe reads, so an edit to any of it re-runs this test.
	repoInput(t, "justfile", "build/*.justfile", "tests/mutation/*.sh", "tests/mutation/survivor_baseline.txt",
		"scripts/mutshard/*.go", ".gomutants.yml")
	just, err := exec.LookPath("just")
	if err != nil {
		t.Fatalf("just is not on PATH (%v); these tests drive the real recipes", err)
	}
	fake := fakeGomutants(t, report)
	// --no-deps: the recipes depend on _mutation-prereqs (generation + build);
	// the wiring under test is what runs AFTER them.
	args := append([]string{"--no-deps", "--justfile", filepath.Join(root, "justfile"), "--working-directory", root}, recipe...)
	cmd := exec.Command(just, args...)
	mutationTmp := t.TempDir()
	cmd.Env = recipeEnv(fake, mutationTmp)
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("running %v: %v\n%s", recipe, err, out)
		}
		code = exitErr.ExitCode()
	}
	argv, _ := os.ReadFile(filepath.Join(fake, "argv"))
	tmpdir, _ := os.ReadFile(filepath.Join(fake, "tmpdir"))
	return recipeRun{code: code, out: string(out), argv: string(argv), tmpdir: string(tmpdir), mutationTmp: mutationTmp}
}

// gomutantsReport is a gomutants --output report scoring killed and lived
// mutants in the module-relative file, with the counters gomutants would
// compute from them.
func gomutantsReport(t *testing.T, file string, killed, lived int) string {
	t.Helper()
	type mutation struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Line   int    `json:"line"`
	}
	muts := []mutation{}
	for i := 0; i < killed+lived; i++ {
		status := "KILLED"
		if i >= killed {
			status = "LIVED"
		}
		muts = append(muts, mutation{ID: fmt.Sprintf("%s:F:CONDITIONALS_NEGATION#%d", file, i+1), Status: status, Line: i + 1})
	}
	b, err := json.Marshal(map[string]any{
		"go_module":      "github.com/ctxloom/ctxloom",
		"files":          []map[string]any{{"file_name": filepath.Base(file), "mutations": muts}},
		"mutants_total":  killed + lived,
		"mutants_killed": killed,
		"mutants_lived":  lived,
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// shardPackages is the real plan for shard k of n over the whole tree.
func shardPackages(t *testing.T, k, n string) []string {
	t.Helper()
	root := repoRootFromTest(t)
	cmd := exec.Command("just", "--justfile", filepath.Join(root, "justfile"), "--working-directory", root, "mutation-shard-plan", "tree", k, n)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("mutation-shard-plan tree %s %s: %v", k, n, err)
	}
	pkgs := strings.Fields(string(out))
	if len(pkgs) == 0 {
		t.Fatalf("shard %s of %s over the tree has no packages", k, n)
	}
	return pkgs
}

// The unsharded recipe runs gomutants and then the AGGREGATE, which decides:
// a run under the efficacy threshold fails with gomutants' own exit code, one
// over it passes. gomutants itself is handed every planned package and no
// threshold — the verdict must come from one place, over the whole run.
func TestMutationRecipes_TheAggregateJudgesTheRun(t *testing.T) {
	pkgs := shardPackages(t, "0", "1")
	file := pkgs[0] + "/f.go"
	cases := []struct {
		name           string
		killed, lived  int
		wantCode       int
		wantOutputHave string
	}{
		{"under the threshold", 5, 5, 10, "test efficacy is not above the threshold"},
		{"over the threshold", 9, 1, 0, "Test efficacy: 90.00%"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := runRecipe(t, gomutantsReport(t, file, c.killed, c.lived), "test-mutation")
			if r.code != c.wantCode || !strings.Contains(r.out, c.wantOutputHave) {
				t.Fatalf("exit %d (want %d), output must contain %q:\n%s", r.code, c.wantCode, c.wantOutputHave, r.out)
			}
			argv := strings.Fields(r.argv)
			if strings.Contains(r.argv, "--changed-since") || strings.Contains(r.argv, "threshold") {
				t.Errorf("gomutants argv = %q, want a whole-tree run with no threshold of its own", argv)
			}
			handed := map[string]bool{}
			for _, a := range argv {
				handed[a] = true
			}
			for _, p := range pkgs {
				if !handed["./"+p] && (p != "." || !handed["."]) {
					t.Errorf("gomutants argv = %q, want every planned package; %s is missing", argv, p)
				}
			}
		})
	}
}

// CI's shape: each shard on its own machine, then one aggregate over the
// reports. A shard that scored a package the plan gave another shard would be
// counted twice; the aggregate refuses that union, and passes a clean one.
func TestMutationRecipes_TheAggregateRefusesAShardThatStrayed(t *testing.T) {
	shard0, shard1 := shardPackages(t, "0", "2"), shardPackages(t, "1", "2")
	cases := []struct {
		name      string
		shard1Pkg string
		wantCode  int
		want      string
	}{
		{"shard 1 scored shard 0's package", shard0[0], 1, "a shard mutated a package the plan gave another shard"},
		{"each shard scored its own", shard1[0], 0, "Test efficacy: 90.00%"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			reports := t.TempDir()
			for k, pkg := range []string{shard0[0], c.shard1Pkg} {
				r := runRecipe(t, gomutantsReport(t, pkg+"/f.go", 9, 1),
					"test-mutation-shard", "tree", []string{"0", "1"}[k], "2", reports)
				if r.code != 0 {
					t.Fatalf("shard %d of 2: exit %d — a shard passes whatever it measured:\n%s", k, r.code, r.out)
				}
				if !strings.Contains(r.argv, "--output") {
					t.Fatalf("shard %d never ran gomutants; argv %q\n%s", k, r.argv, r.out)
				}
			}
			r := runRecipe(t, "", "test-mutation-aggregate", "tree", reports)
			if r.code != c.wantCode || !strings.Contains(r.out, c.want) {
				t.Errorf("aggregate exit %d (want %d), output must contain %q:\n%s", r.code, c.wantCode, c.want, r.out)
			}
		})
	}
}

// A package diff is planned narrowed to the package, and a package with no
// mutable Go in the diff skips under its own name without launching gomutants.
// tests/mutation holds no production Go, so its diff is empty on any checkout.
// The real tool's package diff is pinned by TestPackageDiff_FindsAKnownMutant
// (-tags mutation).
func TestMutationRecipes_PackageDiffIsPlannedUnderThePackage(t *testing.T) {
	for _, args := range [][]string{{"--diff", "HEAD"}, {"--diff=HEAD"}} {
		r := runRecipe(t, "", append([]string{"test-mutation-pkg", "tests/mutation"}, args...)...)
		if r.code != 0 || !strings.Contains(r.out, "diff:HEAD under tests/mutation/") {
			t.Fatalf("%q: exit %d, want 0 with the package-narrowed plan's skip:\n%s", args, r.code, r.out)
		}
		if r.argv != "" {
			t.Errorf("%q: gomutants ran with %q over a package diff with nothing to mutate", args, r.argv)
		}
	}
}

// Anything but --diff BASE is refused rather than forwarded: the run's settings
// are .gomutants.yml's, and a flag that reached gomutants would change what one
// package run measures against every other.
func TestMutationRecipes_PackageRunRefusesExtraArgs(t *testing.T) {
	for _, args := range [][]string{
		{"--diff", "HEAD", "-E", "x"},
		{"--workers", "1", "--diff=HEAD"},
		{"--diff"},
		{"--diff="},
		{"--workers=1"},
	} {
		r := runRecipe(t, "", append([]string{"test-mutation-pkg", "internal/core/spool"}, args...)...)
		if r.code == 0 || r.argv != "" {
			t.Errorf("%q: exit %d, gomutants argv %q; want a refusal before gomutants runs:\n%s", args, r.code, r.argv, r.out)
		}
	}
}

// The ratchet recipe judges the package by its survivor count, not by the
// threshold: a run under the efficacy threshold is reported and does not fail,
// and a run that measured ZERO mutants — which the threshold path passes as
// nothing scored — is refused, because zero survivors would read as an
// improvement on any row.
func TestMutationRecipes_TheRatchetJudgesThePackage(t *testing.T) {
	const pkg = "internal/adapters/isolation"
	r := runRecipe(t, gomutantsReport(t, pkg+"/f.go", 0, 1), "test-mutation-ratchet", pkg)
	if r.code != 0 || !strings.Contains(r.out, "not the verdict") {
		t.Errorf("an efficacy miss: exit %d, want 0 with the miss reported:\n%s", r.code, r.out)
	}
	if !strings.Contains(r.argv, "./"+pkg) || strings.Contains(r.argv, "--changed-since") {
		t.Errorf("gomutants argv = %q, want the whole package", r.argv)
	}

	r = runRecipe(t, gomutantsReport(t, pkg+"/f.go", 0, 0), "test-mutation-ratchet", pkg)
	if r.code == 0 || !strings.Contains(r.out, "ZERO mutants") {
		t.Errorf("zero mutants: exit %d, want a refusal naming it:\n%s", r.code, r.out)
	}
}

// Every recipe that runs gomutants gives the run its OWN temp dir under the
// mutation tmp, and removes it on exit. A test killed mid-run — every
// timed-out mutant kills one — never cleans up after itself; with TMPDIR at
// the shared mutation tmp those would pile up run after run.
// (test-mutation-container takes the same path but needs docker, and
// test-mutation-diff needs a diff, so they are not driven here.)
func TestMutationRecipes_LeaveNothingUnderTheMutationTmp(t *testing.T) {
	const pkg = "internal/core/spool"
	recipes := map[string][]string{
		"test-mutation-pkg":     {"test-mutation-pkg", pkg},
		"test-mutation-ratchet": {"test-mutation-ratchet", "internal/adapters/isolation"},
		"test-mutation":         {"test-mutation"},
	}
	for name, recipe := range recipes {
		t.Run(name, func(t *testing.T) {
			r := runRecipe(t, gomutantsReport(t, pkg+"/f.go", 0, 0), recipe...)
			if !strings.HasPrefix(r.tmpdir, r.mutationTmp+string(os.PathSeparator)) {
				t.Fatalf("gomutants ran with TMPDIR=%q, not a per-run dir under the mutation tmp %q.\noutput:\n%s", r.tmpdir, r.mutationTmp, r.out)
			}
			entries, err := os.ReadDir(r.mutationTmp)
			if err != nil {
				t.Fatalf("read mutation tmp: %v", err)
			}
			for _, e := range entries {
				t.Errorf("left behind under the mutation tmp: %s", e.Name())
				// Let t.TempDir's cleanup remove what the recipe did not.
				_ = filepath.WalkDir(filepath.Join(r.mutationTmp, e.Name()), func(p string, d os.DirEntry, _ error) error {
					if d != nil && d.IsDir() {
						_ = os.Chmod(p, 0o755)
					}
					return nil
				})
			}
		})
	}
}
