package testenv

import (
	"errors"
	"os/exec"
	"testing"
)

// TestRunHistory_NthLastOutput_SeesAnEarlierRunAfterALaterOneOverwritesLast
// pins the reason RunHistory exists: a single mutable lastOutput slot loses
// the first command's output the moment a second command runs, and every
// actor that ran two commands then invented a private snapshot field to work
// around it. The history must let a caller reach back to an earlier run's
// output without one.
func TestRunHistory_NthLastOutput_SeesAnEarlierRunAfterALaterOneOverwritesLast(t *testing.T) {
	var h RunHistory
	h.Record([]string{"first", "command"}, "first output", nil)
	h.Record([]string{"second", "command"}, "second output", nil)

	if got := h.LastOutput(); got != "second output" {
		t.Fatalf("LastOutput() = %q, want %q", got, "second output")
	}
	if got := h.NthLastOutput(0); got != "second output" {
		t.Fatalf("NthLastOutput(0) = %q, want %q (same as LastOutput)", got, "second output")
	}
	if got := h.NthLastOutput(1); got != "first output" {
		t.Fatalf("NthLastOutput(1) = %q, want %q (the FIRST command's output, recoverable after the second overwrote LastOutput)", got, "first output")
	}
	if got := h.NthLastOutput(2); got != "" {
		t.Fatalf("NthLastOutput(2) = %q, want \"\" (no third run exists)", got)
	}
}

// TestRunHistory_LastOutput_EmptyBeforeAnyRun pins the zero-value behaviour
// every caller relies on: before any command has run, the accessors read as
// the Go zero values.
func TestRunHistory_LastOutput_EmptyBeforeAnyRun(t *testing.T) {
	var h RunHistory
	if got := h.LastOutput(); got != "" {
		t.Fatalf("LastOutput() before any run = %q, want \"\"", got)
	}
	if got := h.LastExitCode(); got != 0 {
		t.Fatalf("LastExitCode() before any run = %d, want 0", got)
	}
	if got := h.LastError(); got != nil {
		t.Fatalf("LastError() before any run = %v, want nil", got)
	}
	if got := h.RunCount(); got != 0 {
		t.Fatalf("RunCount() before any run = %d, want 0", got)
	}
}

// TestRunHistory_Record_TracksExitCodeAndError pins the exit-code derivation
// (0 success, -1 non-exec-error, ExitError.ExitCode() otherwise).
func TestRunHistory_Record_TracksExitCodeAndError(t *testing.T) {
	var h RunHistory
	wantErr := errors.New("boom")
	h.Record([]string{"cmd"}, "some output", wantErr)

	if got := h.LastError(); !errors.Is(got, wantErr) {
		t.Fatalf("LastError() = %v, want %v", got, wantErr)
	}
	if got := h.LastExitCode(); got != -1 {
		t.Fatalf("LastExitCode() for a non-ExitError failure = %d, want -1", got)
	}
	if got := h.RunCount(); got != 1 {
		t.Fatalf("RunCount() = %d, want 1", got)
	}
}

// TestRunHistory_Exec_RecordsEveryRunSoTheFirstSurvivesTheSecond pins the
// path a per-actor history is driven through by a step that builds its own
// *exec.Cmd (a teammate's checkout, a different working directory): Exec
// must record EVERY process it ran — argv, both streams, exit code — so an
// assertion about the first command still reads the first command after a
// second one has run. Real processes, not synthetic records: the defect this
// guards against lived in the exec path, not the bookkeeping.
func TestRunHistory_Exec_RecordsEveryRunSoTheFirstSurvivesTheSecond(t *testing.T) {
	var h RunHistory
	if err := h.Exec(exec.Command("sh", "-c", "echo first-out; echo first-err >&2")); err != nil {
		t.Fatalf("first Exec: %v", err)
	}
	if err := h.Exec(exec.Command("sh", "-c", "echo second-out; exit 3")); err == nil {
		t.Fatal("second Exec: want the non-zero exit surfaced as an error, got nil")
	}

	if got := h.RunCount(); got != 2 {
		t.Fatalf("RunCount() = %d, want 2 (every run recorded)", got)
	}
	if got := h.NthLastOutput(1); got != "first-out\nfirst-err\n" {
		t.Fatalf("NthLastOutput(1) = %q, want the FIRST command's combined streams", got)
	}
	if got := h.LastOutput(); got != "second-out\n" {
		t.Fatalf("LastOutput() = %q, want the SECOND command's output", got)
	}
	if got := h.LastStdout(); got != "second-out\n" {
		t.Fatalf("LastStdout() = %q, want the second command's stdout alone", got)
	}
	if got := h.LastExitCode(); got != 3 {
		t.Fatalf("LastExitCode() = %d, want 3 (the process's real exit status)", got)
	}
	// Args are the command's OWN arguments: the binary path is the actor's
	// business, not the invocation's.
	if got := h.LastArgs(); len(got) != 2 || got[0] != "-c" {
		t.Fatalf("LastArgs() = %q, want the argv after the binary", got)
	}
}
