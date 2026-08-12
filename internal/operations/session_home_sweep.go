package operations

import (
	"io"
	"path/filepath"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/projectroot"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
)

// SweepOrphanedSessionHomes runs the startup per-session engine-home reaper for
// THIS project, the sibling of SweepOrphanedWorktrees and wired into
// the same two entry points (`ctxloom run` and `ctxloom mcp`) for the same
// reason: the sweep must run however the session was started.
//
// It exists because instance teardown (EndSession) only fires on a
// GRACEFUL session end, and every instance holds a credential copied out of the
// user's real host home — so without a backstop a crashed run leaves credential
// bytes inside the project tree, one directory per crash. See
// ReapOrphanedSessionHomes for what it will and will not touch.
//
// Best-effort and silent on the all-clear path, mirroring the worktree sweep's
// reporting shape: it reports only when it actually removed something, and a
// failure warns rather than blocking startup.
func SweepOrphanedSessionHomes(w io.Writer) {
	appPath := filepath.Join(projectroot.WorkDir(), paths.AppDirName)
	result, err := ReapOrphanedSessionHomes(appPath)
	if err != nil {
		clidiag.Warn("ctxloom", "session home reap: %v", err)
		return
	}
	if result.Reaped > 0 {
		// Best-effort reporting on a fault-tolerant startup path; a failed
		// write is intentionally dropped (captured-but-unchecked via
		// iox.ErrWriter), matching every other startup reporter.
		ew := iox.NewErrWriter(w)
		ew.Printf("ctxloom: reaped %d orphaned engine-home instance(s) left by ended or crashed session(s)\n", result.Reaped)
	}
}
