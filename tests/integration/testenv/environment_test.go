package testenv

import (
	"errors"
	"testing"
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
