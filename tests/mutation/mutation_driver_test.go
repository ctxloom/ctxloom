// No build tag, deliberately — same reasoning as invalid_mutant_test.go. These
// tests run the REAL public mutation recipes through `just`, with a stand-in
// `go` first on PATH that replays a canned run, so the wiring from a recipe to
// its lane to the survivor ratchet's argument is checked by an ordinary gate
// instead of only during the hour-long run it decides the verdict of. The
// ratchet was once wired in the recipe alone, where deleting the call failed
// nothing.
package mutation

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// driverRun is what one fake mutation run left behind.
type driverRun struct {
	code int
	out  string
	// argv is what the recipe handed `go`, one argument per line.
	argv string
	// tmpdir is the TMPDIR the fake tool ran under; mutationTmp is the
	// recipe's CTXLOOM_MUTATION_TMP.
	tmpdir, mutationTmp string
	// fake is the directory holding the stand-in tools and what they recorded.
	fake string
}

// fakeGoDir writes a stand-in `go` that records its arguments, prints the
// canned run output, and exits with status — the whole of what the driver
// sees of a real `go test` run. It stands in for `go test` ONLY: `go run` and
// `go build` reach the real toolchain, because the gremlins recipes launch
// their planner (scripts/mutshard) with `go run`. A `gremlins` identical to it
// stands in for gremlins itself, and additionally copies the config it was
// handed to config.yaml beside it and, when the test left a report.json there,
// writes it where --output asks — which is all a caller sees of gremlins.
//
// Both also LEAK a test temp dir into their TMPDIR, as a test killed mid-run
// (a gremlins timeout, an interrupted lane) does — with a read-only directory
// inside, as a Go module cache leaves, so a plain `rm -rf` cannot remove it.
func fakeGoDir(t *testing.T, output, status string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "output"), []byte(output), 0o644); err != nil {
		t.Fatalf("write fake output: %v", err)
	}
	realGo, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("go is not on PATH: %v", err)
	}
	script := "#!/bin/sh\n" +
		"here=$(dirname \"$0\")\n" +
		"case \"$(basename \"$0\") $1\" in 'go run'|'go build') exec '" + realGo + "' \"$@\" ;; esac\n" +
		"prev=''\n" +
		"for a in \"$@\"; do\n" +
		"  case \"$prev\" in\n" +
		"    --config) cp \"$a\" \"$here/config.yaml\" ;;\n" +
		"    --output) [ -f \"$here/report.json\" ] && cp \"$here/report.json\" \"$a\" ;;\n" +
		"  esac\n" +
		"  prev=$a\n" +
		"done\n" +
		"printf '%s\\n' \"$@\" > \"$here/argv\"\n" +
		"printf '%s' \"${TMPDIR:-}\" > \"$here/tmpdir\"\n" +
		"leak=\"${TMPDIR:-/tmp}/ctxloom-test-sandbox-$$\"\n" +
		"mkdir -p \"$leak/mod\" && touch \"$leak/mod/go.mod\" && chmod 0555 \"$leak/mod\"\n" +
		"cat \"$here/output\"\n" +
		"exit " + status + "\n"
	for _, tool := range []string{"go", "gremlins"} {
		if err := os.WriteFile(filepath.Join(dir, tool), []byte(script), 0o755); err != nil {
			t.Fatalf("write fake %s: %v", tool, err)
		}
	}
	return dir
}

// driverEnv is the environment a fake run gets: the fake `go` first on PATH,
// a private mutation tmpdir, and NO baseline update mode — the acceptance and
// package lanes ratchet against the repo's real survivor_baseline.txt, and an
// inherited CTXLOOM_MUTATION_BASELINE=update would let a fake run rewrite it.
//
// JUST_JUSTFILE is set to justfile.container, as CI's container jobs set it,
// on every machine: a lane that recursed through a bare `just` would resolve
// that justfile instead of the one it was run from, and without this the
// break shows only on CI.
func driverEnv(t *testing.T, fakeDir, mutationTmp string) []string {
	t.Helper()
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
		"PATH="+fakeDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"CTXLOOM_MUTATION_TMP="+mutationTmp,
		"JUST_JUSTFILE=justfile.container")
}

// runMutationRecipe runs a public mutation recipe exactly as a human would,
// against a fake `go` that replays output and exits with status.
func runMutationRecipe(t *testing.T, output, status string, recipe ...string) driverRun {
	t.Helper()
	return runRecipeWithFake(t, fakeGoDir(t, output, status), recipe...)
}

// runRecipeWithFake is runMutationRecipe against a fake the caller built (and
// may have seeded, e.g. with the report.json the fake gremlins hands back).
func runRecipeWithFake(t *testing.T, fake string, recipe ...string) driverRun {
	t.Helper()
	root := repoRootFromTest(t)
	// Everything the recipe reads, so an edit to any of it re-runs this test.
	repoInput(t, "justfile", "build/*.justfile", "tests/mutation/*.sh", "scripts/mutshard/*.go", ".gremlins.yaml")
	just, err := exec.LookPath("just")
	if err != nil {
		t.Fatalf("just is not on PATH (%v); these tests drive the real recipes", err)
	}
	// --no-deps: the lane recipes depend on _mutation-prereqs (generation + build in the
	// dev container); the wiring under test is what runs AFTER them.
	args := append([]string{"--no-deps", "--justfile", filepath.Join(root, "justfile"), "--working-directory", root}, recipe...)
	cmd := exec.Command(just, args...)
	mutationTmp := t.TempDir()
	cmd.Env = driverEnv(t, fake, mutationTmp)
	out, err := cmd.CombinedOutput()
	argv, _ := os.ReadFile(filepath.Join(fake, "argv"))
	tmpdir, _ := os.ReadFile(filepath.Join(fake, "tmpdir"))
	return driverRun{code: exitCode(t, err, out), out: string(out), argv: string(argv),
		tmpdir: string(tmpdir), mutationTmp: mutationTmp, fake: fake}
}

func exitCode(t *testing.T, err error, out []byte) int {
	t.Helper()
	if err == nil {
		return 0
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("running: %v\n%s", err, out)
	}
	return exitErr.ExitCode()
}

// judgedRow returns the first row of the real survivor baseline under prefix
// that the ratchet JUDGES (a recorded count, not `unknown`), so a fake run
// exceeding it is a regression whatever the row's current numbers are.
func judgedRow(t *testing.T, prefix string) string {
	t.Helper()
	f, err := os.Open(filepath.Join(repoRootFromTest(t), "tests", "mutation", "survivor_baseline.txt"))
	if err != nil {
		t.Fatalf("open baseline: %v", err)
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 4 && strings.HasPrefix(fields[0], prefix) && fields[3] != "unknown" {
			return fields[0]
		}
	}
	t.Fatalf("survivor_baseline.txt has no judged row under %q", prefix)
	return ""
}

// More survivors than any recorded row: a regression against any baseline.
const regressed = 1000000

// The acceptance lane ratchets against the baseline. A run with more survivors
// than the row must fail as a REGRESSION — which it cannot if the lane skipped
// the ratchet, or handed it --no-baseline. Its score is corrected for mutants
// that did not compile.
func TestMutationDriver_AcceptanceLaneRatchetsAgainstTheBaseline(t *testing.T) {
	row := judgedRow(t, "TestAcceptanceMutation/")
	r := runMutationRecipe(t, oozeLogWithInvalid(row, regressed, 2, regressed-2, 2), "0",
		"test-mutation-acceptance", "-run", "X")

	if r.code == 0 || !strings.Contains(r.out, row+" REGRESSED") {
		t.Errorf("exit %d: a run over %s's recorded survivors must fail the acceptance lane as a regression — the lane skipped the survivor ratchet or did not hand it the baseline.\noutput:\n%s", r.code, row, r.out)
	}
	if !strings.Contains(r.out, "2 mutant(s) DID NOT COMPILE and were scored as killed") {
		t.Errorf("the acceptance lane must correct its score for mutants that did not compile; output:\n%s", r.out)
	}
}

// The package lane ratchets against the same baseline, addressed by NAME.
func TestMutationDriver_PackageLaneRatchetsAgainstTheBaseline(t *testing.T) {
	row := judgedRow(t, "TestPackageMutation/")
	name := strings.TrimPrefix(row, "TestPackageMutation/")
	r := runMutationRecipe(t, gremlinsLog(row, 0, regressed, 0, 0, 0, 0), "0",
		"test-mutation-package", name)

	if r.code == 0 || !strings.Contains(r.out, row+" REGRESSED") {
		t.Errorf("exit %d: a run over %s's recorded survivors must fail the package lane as a regression — the lane skipped the survivor ratchet or did not hand it the baseline.\noutput:\n%s", r.code, row, r.out)
	}
	if !strings.Contains(r.argv, "TestPackageMutation/^"+name+"$\n") {
		t.Errorf("the package lane must run only the named entry; go was handed:\n%s", r.argv)
	}
}

// The unit lane is unratcheted: it has no rows, so a healthy target with no row
// must pass — which it cannot if the lane handed the ratchet the baseline file.
// The ratchet must still RUN there (its per-target "measured nothing" refusals
// apply in every lane), and its MEASURED line is the proof that it did.
func TestMutationDriver_UnitLaneChecksSurvivorsWithoutABaseline(t *testing.T) {
	const target = "TestUnitMutation/no_such_baseline_row"
	r := runMutationRecipe(t, oozeLog(target, 10, 6, 4), "0",
		"test-mutation-unit", "-run", "X")

	if r.code != 0 {
		t.Errorf("exit %d: a healthy unit target has no baseline row and must pass — the unit lane handed the ratchet a baseline.\noutput:\n%s", r.code, r.out)
	}
	if !strings.Contains(r.out, "MEASURED  "+target+": 4 survivors of 10") {
		t.Errorf("the unit lane must run the survivor ratchet (without a baseline); it reported no MEASURED line.\noutput:\n%s", r.out)
	}
}

// Every lane refuses a run that measured nothing, however it got there, and
// passes a failing `go test` straight through.
func TestMutationDriver_EveryLaneRefusesARunThatMeasuredNothing(t *testing.T) {
	lanes := map[string][]string{
		"acceptance": {"test-mutation-acceptance", "-run", "X"},
		"unit":       {"test-mutation-unit", "-run", "X"},
		"package":    {"test-mutation-package", "x"},
	}
	cases := []struct {
		name, output, status string
		wantCode             int
		want                 string
	}{
		{"no tests ran", "testing: warning: no tests to run\nPASS\nok  x 0.1s [no tests to run]\n", "0", 1, "matched no tests"},
		{"no score", "=== RUN   TestX\n--- PASS: TestX\nok  x 339.740s\n", "0", 1, "measured NOTHING"},
		{"go test failed", "--- FAIL: TestX\nFAIL\n", "3", 3, "--- FAIL: TestX"},
	}
	for lane, recipe := range lanes {
		for _, c := range cases {
			t.Run(lane+"/"+c.name, func(t *testing.T) {
				r := runMutationRecipe(t, c.output, c.status, recipe...)
				if r.code != c.wantCode || !strings.Contains(r.out, c.want) {
					t.Errorf("exit %d (want %d), output must contain %q:\n%s", r.code, c.wantCode, c.want, r.out)
				}
			})
		}
	}
}

// A lane the driver does not know is a usage error, never a silent default:
// a typo'd lane must not fall through to "no baseline".
func TestMutationDriver_RefusesAnUnknownLane(t *testing.T) {
	root := repoRootFromTest(t)
	fake := fakeGoDir(t, oozeLog("TestAcceptanceMutation/x", 1, 1, 0), "0")
	cmd := exec.Command("bash", repoInput(t, "tests/mutation/mutation_driver.sh")[0], "no-ratchet")
	cmd.Dir = root
	cmd.Env = driverEnv(t, fake, t.TempDir())
	out, err := cmd.CombinedOutput()
	if code := exitCode(t, err, out); code != 2 {
		t.Errorf("exit %d, want 2 (usage) for an unknown lane; output:\n%s", code, out)
	}
	if _, err := os.Stat(filepath.Join(fake, "argv")); err == nil {
		t.Errorf("an unknown lane must be refused BEFORE go test runs")
	}
}

// Every recipe that runs a mutation tool gives the run its OWN temp dir under
// the mutation tmp, and removes it on exit. The tools' test processes put
// their own temp dirs in TMPDIR, and a test killed mid-run — every gremlins
// timeout kills one — never cleans up after itself; with TMPDIR at the shared
// mutation tmp those piled up there run after run, since the sweep only knew
// gremlins' own copies. (test-mutation-container and test-mutation-diff take
// the same path; the first needs docker and the second a diff, so they are
// not driven here.)
func TestMutationRecipes_LeaveNothingUnderTheMutationTmp(t *testing.T) {
	recipes := map[string][]string{
		"driver lane":       {"test-mutation-unit", "-run", "X"},
		"test-mutation-pkg": {"test-mutation-pkg", "internal/x"},
		"test-mutation":     {"test-mutation"},
	}
	for name, recipe := range recipes {
		t.Run(name, func(t *testing.T) {
			r := runMutationRecipe(t, oozeLog("TestUnitMutation/x", 10, 6, 4), "0", recipe...)
			if !strings.HasPrefix(r.tmpdir, r.mutationTmp+string(os.PathSeparator)) {
				t.Fatalf("the tool ran with TMPDIR=%q, not a per-run dir under the mutation tmp %q.\noutput:\n%s", r.tmpdir, r.mutationTmp, r.out)
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
