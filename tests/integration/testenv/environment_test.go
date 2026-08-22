package testenv

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestTestEnvironment_NthLastOutput_SeesAnEarlierRunAfterALaterOneOverwritesLast
// pins that TestEnvironment used to keep only a single mutable
// lastOutput/lastError/lastExitCode slot, so any scenario that ran two
// commands lost the first's output the moment the second ran. Five
// acceptance journeys had invented private snapshot fields to work around
// exactly this (j001500State.lastSyncOutput, four j001700State fields, ...). The
// history-based replacement must let a caller reach back to an earlier
// run's output without a private field.
func TestTestEnvironment_NthLastOutput_SeesAnEarlierRunAfterALaterOneOverwritesLast(t *testing.T) {
	e := &TestEnvironment{}
	e.recordRun([]string{"first", "command"}, "first output", nil)
	e.recordRun([]string{"second", "command"}, "second output", nil)

	if got := e.LastOutput(); got != "second output" {
		t.Fatalf("LastOutput() = %q, want %q", got, "second output")
	}
	if got := e.NthLastOutput(0); got != "second output" {
		t.Fatalf("NthLastOutput(0) = %q, want %q (same as LastOutput)", got, "second output")
	}
	if got := e.NthLastOutput(1); got != "first output" {
		t.Fatalf("NthLastOutput(1) = %q, want %q (the FIRST command's output, recoverable after the second overwrote LastOutput)", got, "first output")
	}
	if got := e.NthLastOutput(2); got != "" {
		t.Fatalf("NthLastOutput(2) = %q, want \"\" (no third run exists)", got)
	}
}

// TestTestEnvironment_LastOutput_EmptyBeforeAnyRun pins the zero-value
// behaviour every existing caller relies on: before any command has run,
// LastOutput/LastExitCode/LastError must read as the Go zero values, exactly
// as the old single mutable fields did.
func TestTestEnvironment_LastOutput_EmptyBeforeAnyRun(t *testing.T) {
	e := &TestEnvironment{}
	if got := e.LastOutput(); got != "" {
		t.Fatalf("LastOutput() before any run = %q, want \"\"", got)
	}
	if got := e.LastExitCode(); got != 0 {
		t.Fatalf("LastExitCode() before any run = %d, want 0", got)
	}
	if got := e.LastError(); got != nil {
		t.Fatalf("LastError() before any run = %v, want nil", got)
	}
	if got := e.RunCount(); got != 0 {
		t.Fatalf("RunCount() before any run = %d, want 0", got)
	}
}

// TestTestEnvironment_RecordRun_TracksExitCodeAndError pins the exit-code
// derivation (0 success, -1 non-exec-error, ExitError.ExitCode() otherwise)
// carried over unchanged from the old inline logic in Run/RunWithStdin.
func TestTestEnvironment_RecordRun_TracksExitCodeAndError(t *testing.T) {
	e := &TestEnvironment{}
	wantErr := errors.New("boom")
	e.recordRun([]string{"cmd"}, "some output", wantErr)

	if got := e.LastError(); !errors.Is(got, wantErr) {
		t.Fatalf("LastError() = %v, want %v", got, wantErr)
	}
	if got := e.LastExitCode(); got != -1 {
		t.Fatalf("LastExitCode() for a non-ExitError failure = %d, want -1", got)
	}
	if got := e.RunCount(); got != 1 {
		t.Fatalf("RunCount() = %d, want 1", got)
	}
}

// ── binary freshness ──────────────────────────────────────────────────────
//
// These pin the guard that stops this package's own suites from measuring a
// binary that predates the source under edit. The defect they exist for
// (taskloom pregnant-laurel) was invisible precisely BECAUSE it was green:
// `just test-pkg ./tests/integration/ -tags integration` execed whatever
// ctxloom was lying around, so a change to cmd/ or internal/ could be verified
// green having never been executed. Anything that quietly turns
// checkBinaryFreshness back into "return nil" must be caught here, because by
// construction no integration test can catch it — the whole failure mode is
// that they keep passing.

// fakeSourceTree writes a minimal project root: one compiled source file, one
// test file, and go.mod, each stamped with an explicit mtime so the comparison
// under test is deterministic rather than a race with the filesystem clock.
func fakeSourceTree(t *testing.T, srcMod, testMod time.Time) string {
	t.Helper()
	root := t.TempDir()
	write := func(rel string, mod time.Time) string {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(path, []byte("package x\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
		if err := os.Chtimes(path, mod, mod); err != nil {
			t.Fatalf("chtimes %s: %v", rel, err)
		}
		return path
	}
	write("internal/thing/thing.go", srcMod)
	write("internal/thing/thing_test.go", testMod)
	write("go.mod", srcMod)
	return root
}

// fakeBinary writes an executable stamped with mod.
func fakeBinary(t *testing.T, mod time.Time) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ctxloom")
	if err := os.WriteFile(path, []byte("#!/bin/false\n"), 0o755); err != nil {
		t.Fatalf("write binary: %v", err)
	}
	if err := os.Chtimes(path, mod, mod); err != nil {
		t.Fatalf("chtimes binary: %v", err)
	}
	return path
}

func TestCheckBinaryFreshness_RefusesABinaryOlderThanTheSourceCompiledIntoIt(t *testing.T) {
	built := time.Date(2026, 8, 18, 18, 53, 17, 0, time.UTC)
	edited := built.Add(72 * time.Hour)
	root := fakeSourceTree(t, edited, built)
	bin := fakeBinary(t, built)

	err := checkBinaryFreshness(bin, root)
	if err == nil {
		t.Fatal("checkBinaryFreshness accepted a binary three days older than its source; " +
			"that is the blind gate this guard exists to close")
	}
	msg := err.Error()
	// The refusal has to be actionable cold, at 3am, in a CI log: WHICH binary,
	// WHICH file outran it, and what to do. A bare "stale binary" sends the
	// reader hunting.
	for _, want := range []string{
		bin,
		filepath.Join(root, "internal", "thing", "thing.go"),
		// .Local(): the refusal formats os.FileInfo.ModTime(), which the
		// filesystem hands back in the machine's zone whatever zone the
		// fixture named.
		built.Local().Format(time.RFC3339),
		edited.Local().Format(time.RFC3339),
		"just build",
		allowStaleBinaryEnv,
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal does not mention %q; got:\n%s", want, msg)
		}
	}
}

func TestCheckBinaryFreshness_AcceptsABinaryNewerThanItsSource(t *testing.T) {
	edited := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	root := fakeSourceTree(t, edited, edited)
	bin := fakeBinary(t, edited.Add(time.Second))

	if err := checkBinaryFreshness(bin, root); err != nil {
		t.Fatalf("refused a binary built AFTER its source: %v", err)
	}
}

func TestCheckBinaryFreshness_IgnoresTestFileEditsBecauseTheyNeverEnterTheBinary(t *testing.T) {
	built := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	// Only the _test.go file is newer than the binary. A test file is compiled
	// into the TEST process, never into the subprocess under test, so treating
	// it as staleness would refuse every run where someone edited a test —
	// which is every run.
	root := fakeSourceTree(t, built.Add(-time.Hour), built.Add(time.Hour))
	bin := fakeBinary(t, built)

	if err := checkBinaryFreshness(bin, root); err != nil {
		t.Fatalf("a newer _test.go must not count as staleness: %v", err)
	}
}

func TestCheckBinaryFreshness_WaivedByTheOptOutEnvVar(t *testing.T) {
	built := time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC)
	root := fakeSourceTree(t, built.Add(72*time.Hour), built)
	bin := fakeBinary(t, built)

	if err := checkBinaryFreshness(bin, root); err == nil {
		t.Fatal("precondition: this pair must be judged stale without the waiver")
	}
	t.Setenv(allowStaleBinaryEnv, "1")
	if err := checkBinaryFreshness(bin, root); err != nil {
		t.Fatalf("%s did not waive the refusal: %v", allowStaleBinaryEnv, err)
	}
}

func TestCheckBinaryFreshness_HasNoOpinionOnATreeWithNoCompiledSource(t *testing.T) {
	// A checkout with none of binaryFreshnessDirs (or one this process cannot
	// read) must degrade to silence, not fail suites for a reason unrelated to
	// what they test.
	bin := fakeBinary(t, time.Date(1990, 1, 1, 0, 0, 0, 0, time.UTC))
	if err := checkBinaryFreshness(bin, t.TempDir()); err != nil {
		t.Fatalf("empty source tree must yield no verdict: %v", err)
	}
}

func TestFindAppBinary_AppliesTheFreshnessVerdictToWhateverItResolved(t *testing.T) {
	// End-to-end through the real resolution path against the REAL repo root,
	// so a future refactor that resolves a binary without consulting
	// checkBinaryFreshness fails here. CTXLOOM_BINARY is resolveAppBinary's
	// first branch, which makes the binary under judgement deterministic.
	env := &TestEnvironment{}

	t.Run("stale", func(t *testing.T) {
		t.Setenv("CTXLOOM_BINARY", fakeBinary(t, time.Date(1990, 1, 1, 0, 0, 0, 0, time.UTC)))
		if got, err := env.findAppBinary(); err == nil {
			t.Fatalf("findAppBinary returned %q for a binary from 1990", got)
		}
	})

	t.Run("fresh", func(t *testing.T) {
		bin := fakeBinary(t, time.Now().Add(time.Hour))
		t.Setenv("CTXLOOM_BINARY", bin)
		got, err := env.findAppBinary()
		if err != nil {
			t.Fatalf("findAppBinary refused a binary newer than every source file: %v", err)
		}
		if got != bin {
			t.Fatalf("findAppBinary returned %q, want %q", got, bin)
		}
	})
}
