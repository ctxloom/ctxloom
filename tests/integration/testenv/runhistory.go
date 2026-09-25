package testenv

import (
	"errors"
	"os/exec"
	"time"
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
	// bound overrides CommandBound when non-zero (SetCommandBound).
	bound time.Duration
	// expired is the first command since the last TakeExpired that was
	// killed for outliving its bound.
	expired *DeadlineError
}

// SetCommandBound replaces CommandBound for this history's later commands.
// A caller whose command is not an ordinary one — an image build, run under
// its own measured bound — or a test needing a bound far below any real
// command's sets it; nothing else should.
func (h *RunHistory) SetCommandBound(d time.Duration) { h.bound = d }

func (h *RunHistory) commandBound() time.Duration {
	if h.bound > 0 {
		return h.bound
	}
	return CommandBound
}

// TakeExpired returns the *DeadlineError of a command killed for outliving
// its bound since the last call, then forgets it — nil if there was none.
//
// It exists because a caller may discard a command's error (the CLI step
// records it and leaves exit status to a later assertion, and a scenario
// EXPECTING failure would then pass on a command that never finished). The
// acceptance suite's after-step hook takes it, which fails the step that ran
// the command whatever that step did with the error.
func (h *RunHistory) TakeExpired() error {
	if h.expired == nil {
		return nil
	}
	de := h.expired
	h.expired = nil
	return de
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
// returning cmd's error — or a *DeadlineError if it outlived its bound
// (CommandBound unless SetCommandBound says otherwise). Args are recorded as
// cmd's own arguments — the binary path is the caller's business, not the
// invocation's.
func (h *RunHistory) Exec(cmd *exec.Cmd) error {
	// syncBuffer, not bytes.Buffer: a run abandoned after its kill (see
	// DeadlineError.Orphaned) can still be written to while it is read here.
	var stdout, stderr syncBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := runBounded(cmd, h.commandBound())
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
//
// A *DeadlineError is appended to the recorded stderr, so a step that reports
// "exited -1; output: ..." names the command and bound that killed it, and it
// is held for TakeExpired.
func (h *RunHistory) RecordSplit(args []string, stdout, stderr string, err error) {
	var de *DeadlineError
	if errors.As(err, &de) {
		stderr += "\n" + de.Error() + "\n"
		if h.expired == nil {
			h.expired = de
		}
	}
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
