package operations

import (
	"context"
	"io"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/isolation"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
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
//
// Both `ctxloom run` and `ctxloom mcp` call this at startup so the sweep runs
// regardless of how the session was started — the same symmetry
// ReportCompanions/WriteAndRecordSyncSummary already keep between the two entry
// points.
func SweepOrphanedWorktrees(ctx context.Context, w io.Writer) {
	result := isolation.ReapOrphanedWorktrees(ctx, nil)
	if result.Reaped > 0 {
		// Best-effort reporting on a fault-tolerant startup path; a failed
		// write is intentionally dropped (captured-but-unchecked via
		// iox.ErrWriter), matching every other startup reporter in this file.
		ew := iox.NewErrWriter(w)
		ew.Printf("ctxloom: reaped %d orphaned per-agent worktree(s) left by crashed run(s)\n", result.Reaped)
	}
}

// ReportCompanions probes the built-in companion binaries (taskloom, ltk) on
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
func ReportCompanions(w io.Writer) {
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

// WriteAndRecordSyncSummary prints a consistent summary of a
// SyncDependenciesResult to w AND records each failed item as a fatal sync
// finding — named for both, for the same reason cli's printAndRecordConfigWarnings
// was: a caller reaching for a summary writer must see that it also arms the
// strict startup gate. Both `ctxloom mcp` startup and `ctxloom run` use this so
// users see the same surface regardless of how sync was triggered.
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
			strictness.Record(strictness.ClassSync, "check network/auth and retry (ctxloom deps pull), or drop the reference from its profile",
				"sync: %s (%s) is neither cached nor fetchable: %s", item.Reference, item.Type, item.Error)
		}
	}
}
