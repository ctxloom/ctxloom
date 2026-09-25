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
	root := repoRootFromTest(t)
	script := filepath.Join(root, "tests", "mutation", "score_correction.sh")

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
	root := repoRootFromTest(t)
	script := filepath.Join(root, "tests", "mutation", "score_correction.sh")

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
