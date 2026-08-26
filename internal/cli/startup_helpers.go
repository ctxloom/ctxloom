package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// THE EXIT-CODE LADDER IS CATEGORICAL, NOT ORDERED BY SEVERITY. Read it as a
// set of distinct answers to "what happened?", never as a scale — 3 (a startup
// abort over fatal findings) is a far more serious outcome than 1 (a mistyped
// flag), and code that treats a higher number as a worse outcome is wrong about
// this vocabulary. The whole ladder is documented in docs/cli-ux-principles.md
// §7, which is the contract; these constants are its implementation.
//
//	0  success, effect delivered
//	1  ctxloom's own error (root.go's fallback for any unclassified error)
//	2  exitCodeRefused — completed, and deliberately did not do something asked
//	3  exitCodeFatalFindings — startup aborted over collected fatal findings
//
// A wrapped engine's own exit code passes through unchanged (ExitError), so
// these values are ctxloom's answers only when ctxloom itself is the one
// answering.

// exitCodeRefused is the exit code for a command that RAN TO COMPLETION and
// DELIBERATELY DID NOT DO something it was asked to do. Nothing failed: the
// tool did the right thing and the user's environment is in a good state.
//
// It is not 1, because a refusal is not an error — reporting it as one sends a
// user looking for a fault on their machine that is not there. It is not 0
// either, because an unattended sync that refuses and exits 0 is
// indistinguishable, to the script that ran it, from one that had nothing to
// do; a run that succeeded at doing none of what was asked is not a success
// (docs/cli-ux-principles.md §7).
//
// Today's uses: `ctxloom deps upgrade` declining to advance a pin onto content
// whose publisher signature does not verify over its bytes (the pin it kept is
// intact and being served; what did not happen is the advance), and the distill
// frontends declining to substitute ctxloom's built-in prompt for a project
// `distill` prompt the trust gate withheld (cli.refuseWithheldDistillPrompt —
// the content is intact and undistilled; what did not happen is the
// distillation).
//
// Chosen after checking the closest external analogues, both of which land on
// 2 for "ran fine, something outstanding": terraform's `plan
// -detailed-exitcode` (0 no changes / 1 error / 2 changes pending) and
// ansible's 2 (one or more hosts failed, the run itself fine). diff/grep's
// inverse mapping — 1 for "found something", 2 for "trouble" — is deliberately
// NOT followed: its 1 would collide with ctxloom's generic error.
const exitCodeRefused = 2

// exitCodeFatalFindings is the exit code for a strict-mode startup abort:
// distinct from 1 (ordinary command errors) and from the wrapped LLM's own
// exit codes on the happy path, so callers/scripts can tell "ctxloom refused
// to launch over collected findings" apart from everything else.
const exitCodeFatalFindings = 3

// loadConfigOrFallback loads ctxloom config via loader, falling back to a
// minimal config rooted at .ctxloom when loading fails. The fallback path
// emits a stderr-style warning to w so users in unconfigured directories
// can still diagnose why their effective config is incomplete.
//
// Used by tools that must keep working without a fully-loaded config
// (`ctxloom deps check`) per CLAUDE.md fault tolerance.
//
// NEVER call this from a command that WRITES. The fallback is a minimal EMPTY
// config: no profile definitions, no defaults, nothing to enumerate. A reader
// handed it degrades to showing less; a writer handed it computes an empty
// result and persists it over real state. `ctxloom deps upgrade` did exactly
// that — it rebuilt the lockfile from an empty closure, erased every pin, hold
// and retraction, and reported "Everything is up to date." Destructive commands
// call the loader directly and fail on its error (see runRemoteUpgrade).
func loadConfigOrFallback(loader func() (*config.Config, error), w io.Writer) *config.Config {
	cfg, err := loader()
	if err != nil {
		// Best-effort warning. This runs in fault-tolerant startup paths
		// (`ctxloom deps check`, `ctxloom search`) that must proceed
		// regardless, so a failed warning write has nowhere to go and is
		// intentionally dropped (captured-but-unchecked via iox.ErrWriter).
		ew := iox.NewErrWriter(w)
		clidiag.Fwarn(ew, "ctxloom", "failed to load config (%v); using minimal default rooted at .ctxloom", err)
		return config.NewFixture(config.Fixture{AppPaths: []string{".ctxloom"}})
	}
	return cfg
}

// Phase names a window of a process's lifecycle that findings are gated
// against. It appears in the abort header, so a caller reading "aborting
// container start" is told WHICH phase refused rather than being sent to
// startup for a fault that happened long after it.
type Phase string

const (
	PhaseStartup        Phase = "startup"
	PhaseWorkspace      Phase = "workspace"
	PhaseTransportStart Phase = "transport start"
)

// phaseGates tiles a process's fatality windows. close() reports the phase's
// findings, returns the abort error if any survived, and OPENS THE NEXT WINDOW
// in the same call — so a finding recorded anywhere between two gates is
// caught, and no caller can forget to re-open.
//
// That last property is the whole reason this type exists. run previously
// achieved the same tiling by convention: gate 1 captured the next mark by
// hand immediately after passing, and two comments explained that the windows
// must abut. Nothing enforced it, and a third window added the same way would
// have been a fourth copy of a rule with no owner. Here the re-open is not
// something a caller does; it is what closing IS.
//
// Single-window entry points use it too, calling close() exactly once. They
// cannot violate the tiling property, but routing them through the same type
// means there is ONE gate mechanism in the product rather than a tiling one
// and a single-shot one that must be kept agreeing.
type phaseGates struct {
	w    io.Writer
	mark strictness.Mark
}

// newPhaseGates opens the first window. Everything recorded from here until
// the first close() belongs to that phase.
func newPhaseGates(w io.Writer) *phaseGates {
	return &phaseGates{w: w, mark: strictness.Checkpoint()}
}

// close ends the current window, reporting and aborting on anything actionable
// it collected, then opens the next. The next window is opened even when this
// one aborts: the caller may ignore the error (a degraded run does), and the
// following phase must still be gated rather than silently unwatched.
func (g *phaseGates) close(p Phase) error {
	found := strictness.Since(g.mark)
	g.mark = strictness.Checkpoint()
	msg := formatFindings(p, found)
	if msg == "" {
		return nil
	}
	fmt.Fprintln(g.w, msg)
	return &ExitError{Code: exitCodeFatalFindings}
}

// formatFindings renders the collected findings block: a header naming the
// PHASE, the count and the escape hatch, then one "[class] message" + "fix:"
// pair per finding. Empty when nothing is actionable in the current mode —
// which under --degraded means everything except a NonDegradable finding.
//
// The header keeps the stable "ctxloom: aborting " prefix ahead of the phase
// so a test asking "did this abort at all" can match the prefix and catch
// every phase. Pinning such a check to one phase's wording is how a negative
// assertion silently stops catching the phases added after it.
func formatFindings(p Phase, findings []strictness.Finding) string {
	findings = strictness.Actionable(findings)
	if len(findings) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "ctxloom: aborting %s: %d fatal finding(s); fix them, or rerun with --degraded (env CTXLOOM_DEGRADED=1) to launch anyway:", p, len(findings))
	for _, f := range findings {
		fmt.Fprintf(&b, "\n  - [%s] %s", f.Class, f.Message)
		if f.FixIt != "" {
			fmt.Fprintf(&b, "\n    fix: %s", f.FixIt)
		}
	}
	return b.String()
}
