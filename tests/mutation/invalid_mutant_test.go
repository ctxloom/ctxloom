// No build tag, deliberately. The rest of this package is //go:build mutation
// because it drives hour-long ooze campaigns. THIS file must run in an ordinary
// gate: it guards the arithmetic that decides whether a reported kill count is
// honest, and a guard that only runs during the thing it guards is not a guard.
package mutation

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// repoRootFromTest walks up to the module root so the tests can invoke the
// scripts by their repo-relative paths regardless of where `go test` is run.
func repoRootFromTest(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod found walking up from the test's working directory")
		}
		dir = parent
	}
}

// repoInput returns the absolute path of each repo-relative file (globs
// allowed) a SUBPROCESS of this test will read, after reading it here.
//
// The read is the point. `go test` caches a passing result keyed on the files
// the test binary itself opened; a script run through exec.Command is read by
// bash, not by the binary, so without this an edit to the script — deleting the
// very check the test pins — is answered with the cached PASS.
func repoInput(t *testing.T, rel ...string) []string {
	t.Helper()
	root := repoRootFromTest(t)
	var paths []string
	for _, r := range rel {
		matches, err := filepath.Glob(filepath.Join(root, r))
		if err != nil || len(matches) == 0 {
			t.Fatalf("no repo file matches %s: %v", r, err)
		}
		for _, m := range matches {
			if _, err := os.ReadFile(m); err != nil {
				t.Fatalf("read %s: %v", m, err)
			}
		}
		paths = append(paths, matches...)
	}
	return paths
}

// A mutant that DOES NOT COMPILE is not a mutant a test caught. ooze scores by
// the runner's exit code alone, so without the marker the compiler is credited
// to the test suite and the kill count is inflated by mutants no test ever saw.
//
// This drives the real runner against a package that cannot build, and asserts
// BOTH halves: the marker is emitted, AND the exit status is still nonzero.
// The exit status matters as much as the marker — survivor counts must not
// move, because the ratchet and every recorded baseline gate on them.
func TestRunUnitJudge_MarksAMutantThatDoesNotCompile(t *testing.T) {
	src := fakeModule(t, map[string]string{
		"go.mod":           labGoMod,
		"broken/broken.go": "package broken\n\nfunc Broken() {}\n",
	})
	t.Setenv("MUT_PKG", "./broken")
	t.Setenv("MUT_RUN", "^TestNothing$")
	// Deliberately not valid Go, written over the laboratory's symlink as ooze
	// writes a mutant: the shape a delete-a-call mutation produces in practice —
	// an orphaned reference the compiler rejects.
	out, err := runInLab(src, "sh tests/mutation/run_unit_judge.sh", map[string]string{
		"broken/broken.go": "package broken\n\nfunc Broken() { this is not go }\n",
	})

	if !strings.Contains(out, "ooze-invalid-mutant:") {
		t.Errorf("runner did not mark a non-compiling target as an invalid mutant.\nWithout the marker the score correction cannot subtract it and the compiler is counted as a kill.\noutput:\n%s", out)
	}
	if err == nil {
		t.Errorf("runner exited 0 on a target that does not compile; ooze reads 0 as SURVIVED, which would move survivor counts and break the ratchet's baselines")
	}
}

// The correction is what makes the reported number honest. It is asserted on
// its own because it used to live inline in a just recipe, where nothing could
// reach it — the fix for a truthfulness defect was itself unverified.
func TestScoreCorrection_SubtractsInvalidMutantsFromTheKillCount(t *testing.T) {
	script := repoInput(t, "tests/mutation/score_correction.sh")[0]

	run := func(t *testing.T, in string) string {
		t.Helper()
		cmd := exec.Command("sh", script)
		cmd.Stdin = strings.NewReader(in)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("score_correction.sh failed: %v\n%s", err, out)
		}
		return string(out)
	}

	withInvalid := run(t, "ooze-invalid-mutant: a\nooze-invalid-mutant: b\n"+
		"┃ • Total:       22 ┃\n┃ • Killed:       5 ┃\n┃ • Survived:    17 ┃\n")

	if !strings.Contains(withInvalid, "valid total:  20") {
		t.Errorf("total not corrected: 22 reported minus 2 that never compiled is 20.\ngot:\n%s", withInvalid)
	}
	if !strings.Contains(withInvalid, "real kills:   3") {
		t.Errorf("kill count not corrected: 5 reported minus 2 that never compiled is 3 — the whole point of the correction.\ngot:\n%s", withInvalid)
	}
	if strings.Contains(withInvalid, "17") && strings.Contains(withInvalid, "urvived") {
		t.Errorf("survivors were altered; they must not move, because the ratchet's baselines gate on them.\ngot:\n%s", withInvalid)
	}

	// Absence must be SILENT. A run with nothing to correct that prints a
	// reassuring line teaches readers to skim past the line that matters.
	clean := run(t, "┃ • Total:       22 ┃\n┃ • Killed:       5 ┃\n")
	if strings.TrimSpace(clean) != "" {
		t.Errorf("printed something when there was nothing to correct: %q", clean)
	}
}

// THE FAILURE PATH: markers present, but NO summary box to correct.
//
// This arm was written during this change and covered by nothing, which is the
// shape the whole exercise exists to catch — a correction for a truthfulness
// defect carrying its own untested branch.
//
// It happens when a run produced invalid mutants and then died, or was filtered
// down to nothing, before ooze summarised. The honest output SAYS SO rather than
// printing a half-corrected number: subtracting from a total that was never
// found would invent one. The upstream no-score guard is what actually fails
// such a run; this only has to avoid lying about it.
func TestScoreCorrection_SaysSoWhenThereIsNoSummaryToCorrect(t *testing.T) {
	script := repoInput(t, "tests/mutation/score_correction.sh")[0]

	cmd := exec.Command("sh", script)
	cmd.Stdin = strings.NewReader("ooze-invalid-mutant: a\nooze-invalid-mutant: b\nsome output with no box at all\n")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("script failed: %v\n%s", err, out)
	}

	got := string(out)
	if !strings.Contains(got, "no summary box was found") {
		t.Errorf("with markers but no box, the script must SAY there was nothing to correct.\ngot: %q", got)
	}
	if strings.Contains(got, "valid total:") || strings.Contains(got, "real kills:") {
		t.Errorf("it must NOT print corrected figures with no total to correct — subtracting from a number that was never found invents one.\ngot: %q", got)
	}
	if !strings.Contains(got, "2 mutant(s)") {
		t.Errorf("it must still report HOW MANY did not compile; that count is real even when the box is missing.\ngot: %q", got)
	}
}

// A multi-target run prints one box per target and the markers of all of
// them; subtracting every target's invalid mutants from the FIRST box alone
// invents a number. The correction is over the whole run.
func TestScoreCorrection_CorrectsAcrossEveryTargetsBox(t *testing.T) {
	cmd := exec.Command("sh", repoInput(t, "tests/mutation/score_correction.sh")[0])
	cmd.Stdin = strings.NewReader(
		"ooze-invalid-mutant: a\n┃ • Total:       10 ┃\n┃ • Killed:       4 ┃\n" +
			"ooze-invalid-mutant: b\nooze-invalid-mutant: c\n┃ • Total:       20 ┃\n┃ • Killed:      15 ┃\n")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("failed on a run with valid mutants: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "valid total:  27") || !strings.Contains(string(out), "real kills:   16") {
		t.Errorf("want valid total 27 (30-3) and real kills 16 (19-3); got:\n%s", out)
	}
}

// gremlins has the same defect as ooze by a different route: `go test` exits 1
// on a build failure, and gremlins maps exit 1 to KILLED (only 2 is NOT
// VIABLE). The package lane marks those mutants `gremlins-invalid-mutant:`,
// after the tally, and the correction must subtract them from gremlins' own
// tally lines — which are not ooze's box — and restate the efficacy it prints,
// since that percentage is the figure a reader takes away.
func TestScoreCorrection_CorrectsAGremlinsTally(t *testing.T) {
	cmd := exec.Command("sh", repoInput(t, "tests/mutation/score_correction.sh")[0])
	cmd.Stdin = strings.NewReader(
		gremlinsLog("TestPackageMutation/x", 5, 2, 3, 1, 0, 0) +
			"gremlins-invalid-mutant: ./x.go:3:42: invalid operation\n" +
			"gremlins-invalid-mutant: ./x.go:9:1: invalid operation\n")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("score_correction.sh failed: %v\n%s", err, out)
	}
	got := string(out)
	// Total is every tallied mutant: 5+2+3 on the first line, 1+0+0 on the second.
	if !strings.Contains(got, "valid total:  9") {
		t.Errorf("total not corrected: 11 tallied minus 2 that never compiled is 9.\ngot:\n%s", got)
	}
	if !strings.Contains(got, "real kills:   3") {
		t.Errorf("kill count not corrected: 5 reported minus 2 that never compiled is 3.\ngot:\n%s", got)
	}
	// gremlins' efficacy is killed/(killed+lived): 5/7 as printed, 3/5 honest.
	if !strings.Contains(got, "real efficacy: 60.00%") {
		t.Errorf("gremlins' efficacy not corrected: 3 real kills over 3+2 is 60.00%%.\ngot:\n%s", got)
	}
}

// runToolexec drives gremlins_toolexec.sh the way `go` does under
// -toolexec — wrapper, then the tool's path, then its arguments — with a
// stand-in tool that prints msg to stderr and exits with status.
func runToolexec(t *testing.T, invalidDir, tool, status, msg string) (int, string) {
	t.Helper()
	wrapper := repoInput(t, "tests/mutation/gremlins_toolexec.sh")[0]
	bin := filepath.Join(t.TempDir(), tool)
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho \""+msg+"\" >&2\nexit "+status+"\n"), 0o755); err != nil {
		t.Fatalf("write fake %s: %v", tool, err)
	}
	cmd := exec.Command("sh", wrapper, bin, "-p", "x")
	cmd.Env = append(os.Environ(), "CTXLOOM_GREMLINS_INVALID_DIR="+invalidDir)
	out, err := cmd.CombinedOutput()
	return exitCode(t, err, out), string(out)
}

func recorded(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	var got []string
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		got = append(got, string(b))
	}
	return got
}

// The wrapper is how the package lane SEES a build failure: gremlins discards
// `go test`'s output and keeps only its exit code, so the compiler's own exit
// status, observed under -toolexec, is the one place "this mutant never ran a
// test" is still distinguishable. It must record a failed compile or vet ONCE
// per `go` command (one command is one mutant), ignore every other tool, and
// hand the tool's exit status and stderr through untouched — gremlins' verdict
// must not move.
func TestGremlinsToolexec_RecordsABuildFailureOncePerGoCommand(t *testing.T) {
	dir := t.TempDir()
	code, out := runToolexec(t, dir, "compile", "1", "./cat.go:3:42: invalid operation: a - b")
	if code != 1 || !strings.Contains(out, "invalid operation") {
		t.Errorf("the compiler's exit status and stderr must pass through: exit %d, output %q", code, out)
	}
	// The same `go` command (this test process) compiling a second variant of
	// the same broken package is still ONE mutant.
	runToolexec(t, dir, "compile", "1", "./cat.go:3:42: invalid operation: a - b")
	got := recorded(t, dir)
	if len(got) != 1 || !strings.Contains(got[0], "cat.go:3:42: invalid operation") {
		t.Errorf("want exactly one record naming the compiler error; got %q", got)
	}

	vetDir := t.TempDir()
	runToolexec(t, vetDir, "vet", "1", "./cat.go:9:5: suspect or: s != a || s != b")
	if got := recorded(t, vetDir); len(got) != 1 {
		t.Errorf("a vet failure fails `go test` before any test runs and must be recorded; got %q", got)
	}

	quiet := t.TempDir()
	if code, _ := runToolexec(t, quiet, "compile", "0", ""); code != 0 {
		t.Errorf("a successful compile must exit 0 through the wrapper; got %d", code)
	}
	if code, _ := runToolexec(t, quiet, "link", "1", "link failed"); code != 1 {
		t.Errorf("a failing link must still exit 1 through the wrapper; got %d", code)
	}
	if got := recorded(t, quiet); len(got) != 0 {
		t.Errorf("only a failed compile or vet marks a mutant; got %q", got)
	}
}
