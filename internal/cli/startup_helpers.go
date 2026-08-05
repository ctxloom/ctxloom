package cli

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/isolation"
	"github.com/ctxloom/ctxloom/internal/operations"
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
// Today's sole use: `ctxloom remote upgrade` declining to advance a pin onto
// content whose publisher signature does not verify over its bytes. The pin it
// kept is intact and being served; what did not happen is the advance.
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
// (`ctxloom remote update`) per CLAUDE.md fault tolerance.
//
// NEVER call this from a command that WRITES. The fallback is a minimal EMPTY
// config: no profile definitions, no defaults, nothing to enumerate. A reader
// handed it degrades to showing less; a writer handed it computes an empty
// result and persists it over real state. `ctxloom remote upgrade` did exactly
// that — it rebuilt the lockfile from an empty closure, erased every pin, hold
// and retraction, and reported "Everything is up to date." Destructive commands
// call the loader directly and fail on its error (see runRemoteUpgrade).
func loadConfigOrFallback(loader func() (*config.Config, error), w io.Writer) *config.Config {
	cfg, err := loader()
	if err != nil {
		// Best-effort warning. This runs in fault-tolerant startup paths
		// (`ctxloom remote update`, `ctxloom search`) that must proceed
		// regardless, so a failed warning write has nowhere to go and is
		// intentionally dropped (captured-but-unchecked via iox.ErrWriter).
		ew := iox.NewErrWriter(w)
		clidiag.Fwarn(ew, "ctxloom", "failed to load config (%v); using minimal default rooted at .ctxloom", err)
		return config.NewFixture(config.Fixture{AppPaths: []string{".ctxloom"}})
	}
	return cfg
}

// printAndRecordConfigWarnings echoes the warnings config.Load accumulated to w
// (one "ctxloom: warning: ..." line each) AND records each as a fatal finding
// for the strict startup gate. The name says BOTH because the recording is not
// a detail of the printing: callers pick this function up expecting a
// reporter, and it also arms an abort. Load downgrades unreadable, malformed, and
// schema-invalid config files (and lossy migrations) to kind-tagged Warnings
// and returns a nil error, so every startup path that consumes a loaded config
// must surface them — otherwise a corrupted config.yaml silently launches an
// empty-context session. The RECORDING is deduplicated per strictness window
// (RecordOnce): config.Load is memoized and re-consulted from every
// GetConfig-based command, so recording unconditionally turned one broken file
// into N copies of one finding in a single abort block. The finding still
// re-fires in the next window, so an unfixed config refuses the next session
// too. A present-but-broken config is fatal-class
// (fail-loudly): `ctxloom run` / `ctxloom mcp` / `ctxloom acp` abort on the
// recorded findings unless --degraded; management commands never check
// findings, so for them this stays pure warning output. Shared by `ctxloom
// run`, `ctxloom mcp`, and the GetConfig-based command entrypoints.
func printAndRecordConfigWarnings(w io.Writer, warnings []config.Warning) {
	// Best-effort reporting on fault-tolerant startup paths; failed writes
	// are intentionally dropped (captured-but-unchecked via iox.ErrWriter).
	ew := iox.NewErrWriter(w)
	for _, warning := range warnings {
		clidiag.Fwarn(ew, "ctxloom", "%s", warning.Text)
		strictness.RecordOnce(configWarningClass(warning.Kind), configWarningFixIt(warning.Kind), "%s", warning.Text)
	}
}

// configWarningClass maps a config warning kind to its strictness class. The
// table lives beside the kinds themselves (config.WarningKind) so the CLI and
// the ACP session opener classify the same degradation identically.
func configWarningClass(kind config.WarningKind) strictness.Class { return kind.StrictnessClass() }

// configWarningFixIt names the fix for a config warning kind (the finding's
// message already carries the path and error detail).
func configWarningFixIt(kind config.WarningKind) string { return kind.FixIt() }

// failOnFindings is the strict startup gate for process-owning entry points
// (`ctxloom run`, `ctxloom mcp`): when strict mode collected fatal findings
// since mark, it prints ALL of them — never just the first — each with its
// fix-it, plus the --degraded escape hatch, and returns an ExitError carrying
// the distinct findings exit code (3). Returns nil in degraded mode or when
// nothing was collected.
func failOnFindings(w io.Writer, mark strictness.Mark) error {
	msg := formatFindings(strictness.Since(mark))
	if msg == "" {
		return nil
	}
	fmt.Fprintln(w, msg)
	return &ExitError{Code: exitCodeFatalFindings}
}

// formatFindings renders the collected findings block: a header naming the
// count and the escape hatch, then one "[class] message" + "fix:" pair per
// finding. Empty when there is nothing to report or the process is degraded.
func formatFindings(findings []strictness.Finding) string {
	if len(findings) == 0 || strictness.Degraded() {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "ctxloom: aborting startup: %d fatal finding(s); fix them, or rerun with --degraded (env CTXLOOM_DEGRADED=1) to launch anyway:", len(findings))
	for _, f := range findings {
		fmt.Fprintf(&b, "\n  - [%s] %s", f.Class, f.Message)
		if f.FixIt != "" {
			fmt.Fprintf(&b, "\n    fix: %s", f.FixIt)
		}
	}
	return b.String()
}

// sweepOrphanedWorktrees runs the startup per-agent-worktree reaper
// (bony-carry bug #2): a crashed/killed run's worktree checkout is never
// reaped by anything else — teardown()'s WIP-safe removal only ever runs on a
// GRACEFUL Cleanup(), so without this sweep every crashed member left an
// orphaned checkout (and a stale `git worktree list` registration in its
// project repo) behind forever, one per crash. Best-effort and silent on the
// all-clear path; only reports when it actually removed something, mirroring
// mcp_server.go's purgeLegacyBundles reporting shape. Never blocks or fails
// startup: isolation.ReapOrphanedWorktrees is itself conservative on every
// candidate it cannot prove is both ownerless AND clean (see its doc) — a
// live agent's worktree, or a dead one still carrying real uncommitted work,
// is left untouched, exactly as the graceful path would leave it.
//
// Both `ctxloom run` and `ctxloom mcp` call this at startup so the sweep runs
// regardless of how the session was started — the same symmetry
// reportCompanions/writeAndRecordSyncSummary already keep between the two entry
// points.
func sweepOrphanedWorktrees(ctx context.Context, w io.Writer) {
	result := isolation.ReapOrphanedWorktrees(ctx, nil)
	if result.Reaped > 0 {
		// Best-effort reporting on a fault-tolerant startup path; a failed
		// write is intentionally dropped (captured-but-unchecked via
		// iox.ErrWriter), matching every other startup reporter in this file.
		ew := iox.NewErrWriter(w)
		ew.Printf("ctxloom: reaped %d orphaned per-agent worktree(s) left by crashed run(s)\n", result.Reaped)
	}
}

// reportCompanions probes the built-in companion binaries (taskloom, ltk) on
// PATH and logs each one found with its self-reported version, so a boot
// transcript shows exactly which companion versions the session was wired
// with. A present binary that fails the probe (predates `version --format json`,
// wedged, not actually the companion) gets a warning but stays wired — PATH
// presence is the gating signal, the version is reporting (CLAUDE.md fault
// tolerance). Missing binaries stay silent here: the bundle resolvers emit
// the one-shot install hint when they skip those entries.
//
// Both `ctxloom mcp` startup and `ctxloom run` call this so the surface is
// the same regardless of how the session was started.
func reportCompanions(w io.Writer) {
	// Best-effort reporting on fault-tolerant startup paths; failed writes
	// are intentionally dropped (captured-but-unchecked via iox.ErrWriter).
	ew := iox.NewErrWriter(w)
	for _, st := range config.ProbeCompanions() {
		switch {
		case st.Path == "":
		case !st.Executed():
			// Present on PATH but NOT run — refused, unconfirmed, or blocked by
			// a consent-record fault. AdmitCompanions already warned with the
			// specific reason and the way to fix it; what this line adds is the
			// version slot NOT silently coming back blank, which would read as
			// "probed, said nothing" instead of "never ran".
			ew.Printf("ctxloom: companion %s not run (%s)\n", st.Bin, st.Admission)
		case st.Err != nil:
			clidiag.Fwarn(ew, "ctxloom", "companion %s (%s): %v", st.Bin, st.Path, st.Err)
		default:
			ew.Printf("ctxloom: companion %s %s\n", st.Bin, st.Version)
		}
	}
}

// writeAndRecordSyncSummary prints a consistent summary of a
// SyncDependenciesResult to w AND records each failed item as a fatal sync
// finding — named for both, for the same reason as
// printAndRecordConfigWarnings above: a caller reaching for a summary writer
// must see that it also arms the strict startup gate. Both `ctxloom mcp` startup and `ctxloom run` use this so users see
// the same surface regardless of how sync was triggered.
//
//   - Successful syncs that installed or updated something get a one-line
//     status message.
//   - Failures get a "completed with N errors" warning plus a per-item
//     breakdown so the user can diagnose which bundle/profile failed and
//     why (important for hard-fail clone errors that used to silently
//     degrade to API).
//
// Each failed item is also recorded as a fatal sync finding for the strict
// startup gate: on the startup path a Failed entry is exactly the fatal class
// — a pinned/configured item that was missing (not satisfiable from cache;
// installed items are "skipped") AND whose fetch failed. A refresh failure
// with a complete cache never lands here and stays a plain warning in both
// modes.
//
// A nil result is a no-op so callers don't have to nil-check.
func writeAndRecordSyncSummary(w io.Writer, result *operations.SyncDependenciesResult) {
	if result == nil {
		return
	}
	// Best-effort summary. Both `ctxloom mcp` startup and `ctxloom run`
	// call this on a fault-tolerant path that must never block, so a
	// failed write to the summary target is intentionally dropped
	// (captured-but-unchecked via iox.ErrWriter).
	ew := iox.NewErrWriter(w)
	if result.Status != "up_to_date" && result.Installed+result.Updated > 0 {
		ew.Printf("ctxloom: %s\n", result.Message)
	}
	if result.Errors > 0 {
		clidiag.Fwarn(ew, "ctxloom", "sync completed with %d errors", result.Errors)
		for _, item := range result.Failed {
			ew.Printf("ctxloom:   - %s (%s): %s\n", item.Reference, item.Type, item.Error)
			strictness.Record(strictness.ClassSync, "check network/auth and retry (ctxloom remote pull), or drop the reference from its profile",
				"sync: %s (%s) is neither cached nor fetchable: %s", item.Reference, item.Type, item.Error)
		}
	}
}
