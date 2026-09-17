package testenv

import (
	"bytes"
	"os/exec"
)

// RunHistory is the ordered record of every CLI invocation one ACTOR made,
// oldest first. It replaces a single mutable lastOutput/lastError/lastExitCode
// slot: a single slot forces any scenario that runs two commands to lose the
// first's output the moment the second runs, and every actor with such a slot
// grew private snapshot fields to work around exactly that. LastOutput/
// LastExitCode/LastError read the newest entry; NthLastOutput(n) reaches back.
//
// One history per actor, not one per process: a journey that interleaves two
// developers' commands (Carol syncs, Bob syncs, THEN Carol is asserted about)
// asserts per actor, and folding both into one stream would make Carol's
// NthLastOutput(1) read Bob's run — passing for the wrong reason whenever
// their outputs happen to agree. TestEnvironment embeds one for its own
// ProjectDir; a step driving a different checkout keeps its own and feeds it
// through Exec.
//
// The zero value is ready to use.
type RunHistory struct {
	runs []RunRecord
}

// RunRecord captures everything observed about one CLI invocation: the argv,
// its combined stdout+stderr, exit code, and any error.
type RunRecord struct {
	Args   []string
	Output string // combined stdout+stderr
	// Stdout is the MACHINE stream on its own. A `--format json` assertion
	// that parses Output cannot work: any stderr line the command also
	// emitted (a companion advisory, a withheld-content notice) is
	// concatenated onto the JSON and the parse fails, which pushed those
	// scenarios back onto substring matching — the exact weakness that let a
	// json-flagged command pass while rendering human text.
	Stdout   string
	ExitCode int
	Err      error
}

// Exec runs cmd with both streams captured and records the invocation,
// returning cmd.Run's error. Args are recorded as cmd's own arguments — the
// binary path is the caller's business, not the invocation's.
func (h *RunHistory) Exec(cmd *exec.Cmd) error {
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	h.RecordSplit(cmd.Args[1:], stdout.String(), stderr.String(), err)
	return err
}

// Record derives a RunRecord's ExitCode from err (0 on success, -1 for a
// non-ExitError failure, else the process's real exit code) and appends it.
func (h *RunHistory) Record(args []string, output string, err error) {
	rec := RunRecord{
		Args:   append([]string(nil), args...),
		Output: output,
		Err:    err,
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		rec.ExitCode = exitErr.ExitCode()
	} else if err != nil {
		rec.ExitCode = -1
	} else {
		rec.ExitCode = 0
	}
	h.runs = append(h.runs, rec)
}

// RecordSplit records a run whose stdout and stderr were captured separately,
// keeping both the concatenated view every existing assertion reads and the
// machine stream on its own (see RunRecord.Stdout). Callers reach it only
// AFTER cmd.Run()/cmd.Wait() has returned — i.e. after the process has
// genuinely been started.
func (h *RunHistory) RecordSplit(args []string, stdout, stderr string, err error) {
	h.Record(args, stdout+stderr, err)
	h.runs[len(h.runs)-1].Stdout = stdout
}

// LastOutput returns the combined stdout/stderr from the last command.
func (h *RunHistory) LastOutput() string {
	return h.NthLastOutput(0)
}

// LastStdout returns ONLY the last command's stdout — the stream a
// `--format json` assertion has to parse (see RunRecord.Stdout).
func (h *RunHistory) LastStdout() string {
	if len(h.runs) == 0 {
		return ""
	}
	return h.runs[len(h.runs)-1].Stdout
}

// LastArgs returns the argv the last command was invoked with, which is where
// a format-aware assertion reads the encoding that was ASKED for. Reading it
// off the invocation rather than sniffing the payload is what lets a scenario
// tell "the command honoured --format text" apart from "the command emitted
// something that happens to parse that way".
func (h *RunHistory) LastArgs() []string {
	if len(h.runs) == 0 {
		return nil
	}
	return h.runs[len(h.runs)-1].Args
}

// NthLastOutput returns the combined stdout/stderr of the n-th most recent
// command (n=0 is the same value LastOutput returns, n=1 the command before
// that, and so on), or "" if fewer than n+1 commands have run yet. Lets a
// scenario recover an EARLIER command's output after a later command has
// run and LastOutput now reflects that one instead.
func (h *RunHistory) NthLastOutput(n int) string {
	idx := len(h.runs) - 1 - n
	if idx < 0 || idx >= len(h.runs) {
		return ""
	}
	return h.runs[idx].Output
}

// LastExitCode returns the exit code from the last command.
func (h *RunHistory) LastExitCode() int {
	if len(h.runs) == 0 {
		return 0
	}
	return h.runs[len(h.runs)-1].ExitCode
}

// LastError returns the error from the last command.
func (h *RunHistory) LastError() error {
	if len(h.runs) == 0 {
		return nil
	}
	return h.runs[len(h.runs)-1].Err
}

// RunCount returns the monotonic number of CLI invocations recorded so far.
// A change between two observations means a command actually ran in the
// interim — the line a string-equality guard on the output cannot draw when
// two consecutive commands print the same thing.
func (h *RunHistory) RunCount() int { return len(h.runs) }
