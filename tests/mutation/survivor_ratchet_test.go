// No build tag, deliberately — same reasoning as invalid_mutant_test.go: the
// ratchet is the arithmetic that decides whether a mutation run regressed, and
// a guard that only runs during the hour-long thing it guards is not a guard.
package mutation

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// runRatchet drives the real survivor_ratchet.sh against a baseline and a run
// log written to a temp dir, and returns its exit code, combined output, and
// the baseline's contents afterwards (the update path rewrites it in place).
func runRatchet(t *testing.T, baseline, log string, env ...string) (int, string, string) {
	t.Helper()
	root := repoRootFromTest(t)
	script := filepath.Join(root, "tests", "mutation", "survivor_ratchet.sh")

	dir := t.TempDir()
	basePath := filepath.Join(dir, "baseline.txt")
	logPath := filepath.Join(dir, "run.log")
	if err := os.WriteFile(basePath, []byte(baseline), 0o644); err != nil {
		t.Fatalf("write baseline: %v", err)
	}
	if err := os.WriteFile(logPath, []byte(log), 0o644); err != nil {
		t.Fatalf("write log: %v", err)
	}

	cmd := exec.Command("bash", script, basePath, logPath)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("running ratchet: %v\n%s", err, out)
		}
		code = exitErr.ExitCode()
	}
	after, err := os.ReadFile(basePath)
	if err != nil {
		t.Fatalf("re-reading baseline: %v", err)
	}
	return code, string(out), string(after)
}

// gremlinsLog is what a gremlins-judged target leaves in the run log: the
// marker the harness prints, then gremlins' own two-line tally.
func gremlinsLog(target string, killed, lived, notCovered, timedOut, notViable, skipped int) string {
	return "some earlier output\n" +
		"gremlins-target: " + target + "\n" +
		"      KILLED CONDITIONALS_NEGATION at x.go:1:1\n" +
		"Mutation testing completed in 12 seconds 5 milliseconds\n" +
		"Killed: " + strconv.Itoa(killed) + ", Lived: " + strconv.Itoa(lived) + ", Not covered: " + strconv.Itoa(notCovered) + "\n" +
		"Timed out: " + strconv.Itoa(timedOut) + ", Not viable: " + strconv.Itoa(notViable) + ", Skipped: " + strconv.Itoa(skipped) + "\n" +
		"Test efficacy: 66.67%\n" +
		"Mutator coverage: 90.00%\n"
}

func oozeLog(target string, total, killed, survived int) string {
	return "ooze-target: " + target + "\n" +
		"┃ • Total:     " + strconv.Itoa(total) + " ┃\n" +
		"┃ • Killed:    " + strconv.Itoa(killed) + " ┃\n" +
		"┃ • Survived:  " + strconv.Itoa(survived) + " ┃\n" +
		"┃ • Score:     0.50 ┃\n"
}

const packageRow = "TestPackageMutation/iso"

// A gremlins target whose unverified count matches its row holds, exit 0.
func TestSurvivorRatchet_GremlinsTargetHeld(t *testing.T) {
	code, out, _ := runRatchet(t,
		packageRow+" 100 40 measured\n",
		gremlinsLog(packageRow, 60, 30, 10, 0, 0, 0))
	if code != 0 {
		t.Fatalf("exit %d for a held gremlins target; output:\n%s", code, out)
	}
	if !strings.Contains(out, "HELD  "+packageRow) {
		t.Errorf("a gremlins target at its baseline must report HELD; got:\n%s", out)
	}
}

// LIVED grew past the row: the ratchet fails, exit 1.
func TestSurvivorRatchet_GremlinsTargetRegressedOnLived(t *testing.T) {
	code, out, _ := runRatchet(t,
		packageRow+" 100 40 measured\n",
		gremlinsLog(packageRow, 55, 35, 10, 0, 0, 0))
	if code != 1 {
		t.Fatalf("exit %d, want 1 for 45 unverified against a row of 40; output:\n%s", code, out)
	}
	if !strings.Contains(out, "REGRESSED") {
		t.Errorf("must name the regression; got:\n%s", out)
	}
}

// THE SEMANTIC THIS LANE ADDS: a NOT COVERED mutant is a mechanism no test
// executed, which is exactly the "left unverified" the ratchet refuses to let
// grow. Counting only LIVED would let untested new code through the gate.
func TestSurvivorRatchet_GremlinsNotCoveredCountsAsUnverified(t *testing.T) {
	code, out, _ := runRatchet(t,
		packageRow+" 100 40 measured\n",
		gremlinsLog(packageRow, 59, 30, 11, 0, 0, 0))
	if code != 1 {
		t.Fatalf("exit %d, want 1: LIVED held at 30 but NOT COVERED rose 10 -> 11, so one more mechanism is unverified; output:\n%s", code, out)
	}
	if !strings.Contains(out, "40 survivors -> 41") {
		t.Errorf("the regression must be reported as 40 -> 41 (lived + not covered); got:\n%s", out)
	}
}

// Fewer unverified than the row: reported as IMPROVED, exit 0, row untouched
// (lowering it is a decision that belongs in the diff that earned it).
func TestSurvivorRatchet_GremlinsTargetImprovedIsReportedNotBanked(t *testing.T) {
	baseline := packageRow + " 100 40 measured\n"
	code, out, after := runRatchet(t, baseline, gremlinsLog(packageRow, 65, 30, 5, 0, 0, 0))
	if code != 0 {
		t.Fatalf("exit %d for an improvement; output:\n%s", code, out)
	}
	if !strings.Contains(out, "IMPROVED  "+packageRow+": 40 survivors -> 35") {
		t.Errorf("must report the improvement with both numbers; got:\n%s", out)
	}
	if after != baseline {
		t.Errorf("the row must not be lowered automatically; baseline became:\n%s", after)
	}
}

// An `unknown` row: the run is neither a pass nor a failure on it.
func TestSurvivorRatchet_GremlinsUnknownRowIsUnbaselined(t *testing.T) {
	code, out, _ := runRatchet(t,
		packageRow+" - - unknown\n",
		gremlinsLog(packageRow, 60, 30, 10, 0, 0, 0))
	if code != 0 {
		t.Fatalf("exit %d for an unknown row; output:\n%s", code, out)
	}
	if !strings.Contains(out, "UNBASELINED  "+packageRow+": 40 survivors of 100") {
		t.Errorf("must report the measurement it cannot judge, with lived+not-covered as survivors; got:\n%s", out)
	}
}

// CTXLOOM_MUTATION_BASELINE=update records the measurement: TOTAL is every
// mutant gremlins reported (all six statuses), SURVIVED is lived + not covered.
func TestSurvivorRatchet_GremlinsUpdateRecordsTheRow(t *testing.T) {
	code, out, after := runRatchet(t,
		"# header\n"+packageRow+" - - unknown\n",
		gremlinsLog(packageRow, 60, 30, 10, 2, 1, 0),
		"CTXLOOM_MUTATION_BASELINE=update")
	if code != 0 {
		t.Fatalf("exit %d in update mode; output:\n%s", code, out)
	}
	if !strings.Contains(out, "BASELINE UPDATED") {
		t.Errorf("update mode must announce itself; got:\n%s", out)
	}
	row := ""
	for _, line := range strings.Split(after, "\n") {
		if strings.HasPrefix(line, packageRow) {
			row = line
		}
	}
	fields := strings.Fields(row)
	if len(fields) != 4 || fields[1] != "103" || fields[2] != "40" || fields[3] != "measured" {
		t.Errorf("row after update = %q, want TOTAL 103 (60+30+10+2+1+0), SURVIVED 40 (30 lived + 10 not covered), measured", row)
	}
	if !strings.HasPrefix(after, "# header\n") {
		t.Errorf("the header must survive an in-place update; baseline became:\n%s", after)
	}
}

// A gremlins marker followed by an OOZE box (or vice versa) is a score that
// cannot be attributed to the target that announced it. Refuse it.
func TestSurvivorRatchet_MismatchedToolUnderAMarkerIsUnattributable(t *testing.T) {
	code, out, _ := runRatchet(t,
		packageRow+" 100 40 measured\n",
		"gremlins-target: "+packageRow+"\n"+
			"┃ • Total:     100 ┃\n┃ • Killed:    60 ┃\n┃ • Survived:  40 ┃\n")
	if code != 1 {
		t.Fatalf("exit %d, want 1: an ooze box under a gremlins marker belongs to no announced target; output:\n%s", code, out)
	}
	if !strings.Contains(out, "belong to no announced target") {
		t.Errorf("must say the box could not be attributed; got:\n%s", out)
	}

	// And the other way round: a gremlins tally under an ooze marker.
	code, out, _ = runRatchet(t,
		"TestAcceptanceMutation/x 43 31 recorded\n",
		"ooze-target: TestAcceptanceMutation/x\n"+
			"Killed: 60, Lived: 30, Not covered: 10\n"+
			"Timed out: 0, Not viable: 0, Skipped: 0\n")
	if code != 1 {
		t.Fatalf("exit %d, want 1: a gremlins tally under an ooze marker belongs to no announced target; output:\n%s", code, out)
	}
	if !strings.Contains(out, "belong to no announced target") {
		t.Errorf("must say the tally could not be attributed; got:\n%s", out)
	}
}

// The ooze path is untouched by the gremlins addition.
func TestSurvivorRatchet_OozeTargetStillJudged(t *testing.T) {
	code, out, _ := runRatchet(t,
		"TestAcceptanceMutation/x 43 31 recorded\n",
		oozeLog("TestAcceptanceMutation/x", 43, 12, 31))
	if code != 0 {
		t.Fatalf("exit %d for a held ooze target; output:\n%s", code, out)
	}
	if !strings.Contains(out, "HELD  TestAcceptanceMutation/x: 31 survivors of 43") {
		t.Errorf("ooze box must still be attributed to its marker; got:\n%s", out)
	}

	code, out, _ = runRatchet(t,
		"TestAcceptanceMutation/x 43 31 recorded\n",
		oozeLog("TestAcceptanceMutation/x", 43, 11, 32))
	if code != 1 || !strings.Contains(out, "REGRESSED") {
		t.Errorf("an ooze regression must still fail: exit %d, output:\n%s", code, out)
	}
}

// A gremlins run over an empty mutant set says nothing, and must not pass.
func TestSurvivorRatchet_GremlinsZeroMutantsIsAFailure(t *testing.T) {
	code, out, _ := runRatchet(t,
		packageRow+" 100 40 measured\n",
		gremlinsLog(packageRow, 0, 0, 0, 0, 0, 0))
	if code != 1 {
		t.Fatalf("exit %d, want 1 for a run that produced ZERO mutants; output:\n%s", code, out)
	}
	if !strings.Contains(out, "ZERO mutants") {
		t.Errorf("must say the mutant set was empty; got:\n%s", out)
	}
}

// oozeLogWithInvalid is oozeLog with n of the target's mutants marked by the
// runner as having failed to compile, the way they appear in a real run:
// between the target's marker and its box.
func oozeLogWithInvalid(target string, total, killed, survived, invalid int) string {
	marker := "ooze-target: " + target + "\n"
	body := strings.TrimPrefix(oozeLog(target, total, killed, survived), marker)
	return marker + strings.Repeat("ooze-invalid-mutant: ./cmd/ctxloom did not compile\n", invalid) + body
}

// A run in which NO mutant compiled measured nothing: ooze scored every build
// failure as a kill, so its box reads 0 survivors and a perfect score. The
// ratchet must fail it — and above all must not call it an improvement, which
// is what it did when the laboratory lost its embeds.
func TestSurvivorRatchet_OozeRunWhereNoMutantCompiledMeasuredNothing(t *testing.T) {
	code, out, _ := runRatchet(t,
		"TestAcceptanceMutation/x 51 51 measured\n",
		oozeLogWithInvalid("TestAcceptanceMutation/x", 50, 50, 0, 50))
	if code != 1 {
		t.Fatalf("exit %d, want 1 for a run whose every mutant failed to compile; output:\n%s", code, out)
	}
	if strings.Contains(out, "IMPROVED") {
		t.Errorf("a run that measured nothing was reported as an improvement:\n%s", out)
	}
	if !strings.Contains(out, "measured NOTHING") {
		t.Errorf("must say the run measured nothing; got:\n%s", out)
	}

	// Nor may it be RECORDED: banking 0 survivors from it would turn the next
	// honest run into a regression.
	code, out, after := runRatchet(t,
		"TestAcceptanceMutation/x 51 51 measured\n",
		oozeLogWithInvalid("TestAcceptanceMutation/x", 50, 50, 0, 50),
		"CTXLOOM_MUTATION_BASELINE=update")
	if code != 1 || !strings.Contains(after, "TestAcceptanceMutation/x 51 51 measured") {
		t.Errorf("update mode recorded a run that measured nothing: exit %d\noutput:\n%s\nbaseline after:\n%s", code, out, after)
	}
}

// Some invalid mutants are normal — a mutation can orphan a reference. The
// survivors among the rest are real, so the target is judged as usual.
func TestSurvivorRatchet_OozeRunWithSomeInvalidMutantsIsStillJudged(t *testing.T) {
	code, out, _ := runRatchet(t,
		"TestAcceptanceMutation/x 43 31 recorded\n",
		oozeLogWithInvalid("TestAcceptanceMutation/x", 43, 12, 31, 5))
	if code != 0 || !strings.Contains(out, "HELD  TestAcceptanceMutation/x") {
		t.Errorf("a target with valid mutants must still be judged: exit %d, output:\n%s", code, out)
	}
}

// runRatchetWithoutBaseline drives the ratchet as the unratcheted unit lane
// does: its per-target "measured nothing" refusals, and no baseline judgement.
func runRatchetWithoutBaseline(t *testing.T, log string) (int, string) {
	t.Helper()
	root := repoRootFromTest(t)
	logPath := filepath.Join(t.TempDir(), "run.log")
	if err := os.WriteFile(logPath, []byte(log), 0o644); err != nil {
		t.Fatalf("write log: %v", err)
	}
	cmd := exec.Command("bash", filepath.Join(root, "tests", "mutation", "survivor_ratchet.sh"), "--no-baseline", logPath)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return 0, string(out)
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("running ratchet: %v\n%s", err, out)
	}
	return exitErr.ExitCode(), string(out)
}

// The unit lane has no baseline, but a target none of whose mutants compiled
// measured nothing there too — and beside a healthy target it used to pass,
// because the only check the lane ran compared the whole run's invalid count
// against the whole run's total.
func TestSurvivorRatchet_WithoutABaselineStillRefusesATargetWhereNoMutantCompiled(t *testing.T) {
	healthy := oozeLogWithInvalid("TestUnitMutation/healthy", 10, 6, 4, 0)
	broken := oozeLogWithInvalid("TestUnitMutation/broken", 3, 3, 0, 3)

	if code, out := runRatchetWithoutBaseline(t, healthy); code != 0 {
		t.Fatalf("exit %d for a healthy unbaselined target; the unit lane has no rows and must not need any.\noutput:\n%s", code, out)
	}

	code, out := runRatchetWithoutBaseline(t, broken+healthy)
	if code != 1 {
		t.Fatalf("exit %d, want 1: TestUnitMutation/broken measured nothing, and a healthy target beside it must not hide that.\noutput:\n%s", code, out)
	}
	if !strings.Contains(out, "TestUnitMutation/broken: all 3 mutants DID NOT COMPILE") {
		t.Errorf("the refusal must name the target that measured nothing; got:\n%s", out)
	}
}
