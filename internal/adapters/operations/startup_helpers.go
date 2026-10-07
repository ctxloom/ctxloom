package operations

import (
	"context"
	"io"

	"github.com/ctxloom/ctxloom/internal/adapters/companions"
	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/errwriter"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// SweepOrphanedWorktrees runs the startup per-agent-worktree reaper: a
// crashed/killed run's worktree checkout is never
// reaped by anything else — teardown()'s WIP-safe removal only ever runs on a
// GRACEFUL Cleanup(), so without this sweep every crashed member left an
// orphaned checkout (and a stale `git worktree list` registration in its
// project repo) behind forever, one per crash. Best-effort and silent on the
// all-clear path; only reports when it actually removed something. Never blocks or fails
// startup: isolation.ReapOrphanedWorktrees is itself conservative on every
// candidate it cannot prove is both ownerless AND clean (see its doc) — a
// live agent's worktree, or a dead one still carrying real uncommitted work,
// is left untouched, exactly as the graceful path would leave it.

func SweepOrphanedWorktrees(ctx context.Context, w io.Writer) {
	result := isolation.ReapOrphanedWorktrees(ctx, nil)
	if result.Reaped > 0 {
		// Best-effort reporting on a fault-tolerant startup path; a failed
		// write is intentionally dropped (captured-but-unchecked via
		// errwriter.Writer), matching every other startup reporter in this file.
		ew := errwriter.New(w)
		ew.Printf("ctxloom: reaped %d orphaned per-agent worktree(s) left by crashed run(s)\n", result.Reaped)
	}
}

// ReportCompanions probes each REGISTERED companion (names) on PATH and logs
// each one found with its self-reported version, so a boot transcript shows
// exactly which companion versions the session was wired with. A present
// binary that fails the probe (predates `version --format json`, wedged, not
// actually the companion) gets a warning but stays wired — the version is
// reporting (CLAUDE.md fault tolerance). A registered name that resolves to
// nothing stays silent here: the loadout probe names it with its remedy.
func ReportCompanions(w io.Writer, prober companions.Prober, names []string) {
	// Best-effort reporting on fault-tolerant startup paths; failed writes
	// are intentionally dropped (captured-but-unchecked via errwriter.Writer).
	ew := errwriter.New(w)
	for _, st := range prober.ProbeCompanions(names) {
		switch {
		case st.Path == "":
		case st.Err != nil:
			clidiag.Fwarn(ew, "ctxloom", "companion %s (%s): %v", st.Bin, st.Path, st.Err)
		default:
			ew.Printf("ctxloom: companion %s %s\n", st.Bin, st.Version)
		}
	}
}

// WriteAndRecordSyncSummary prints a consistent summary of a
// SyncDependenciesResult to w AND records each failed item as a fatal sync
// finding — named for both, for the same reason cli's printAndRecordConfigWarnings
// was: a caller reaching for a summary writer must see that it also arms the
// strict startup gate. `ctxloom run`'s startup sync is its caller.
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
func WriteAndRecordSyncSummary(w io.Writer, result *SyncDependenciesResult) {
	if result == nil {
		return
	}
	// Best-effort summary. The startup sync calls this on a fault-tolerant
	// path that must never block, so a failed write to the summary target is
	// intentionally dropped (captured-but-unchecked via errwriter.Writer).
	ew := errwriter.New(w)
	if result.Status != "up_to_date" && result.Installed+result.Reinstalled > 0 {
		ew.Printf("ctxloom: %s\n", result.Message)
	}
	WriteConstraintChanges(ew, result.ConstraintChanges)
	WriteNewPins(ew, result.Changes)
	if result.Errors > 0 {
		clidiag.Fwarn(ew, "ctxloom", "sync completed with %d errors", result.Errors)
		for _, item := range result.Failed {
			ew.Printf("ctxloom:   - %s (%s): %s\n", item.Reference, item.Type, item.Error)
			strictness.Record(report.KindSync, item.Remedy(),
				"sync: %s (%s) is not cached and could not be installed: %s", item.Reference, item.Type, item.Error)
		}
	}
}
