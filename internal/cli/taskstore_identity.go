package cli

import (
	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/projectroot"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// taskStoreWorkDir returns workDir, redirected to a linked git worktree's
// PRIMARY checkout for TASK-STORE purposes only (see
// projectroot.TaskStoreRoot) -- so `ctxloom run`'s CTXLOOM_PROJECT_ID export
// resolves the SAME project identity a `taskloom` call from workDir would
// (internal/taskloom/workdir carries the identical redirect). This is
// deliberately a NEW, separate seam from taskops.ResolveProjectIdentity
// itself, which stays untouched and worktree-distinct for its OTHER caller,
// internal/mcp/coord_host.go: the runtime coordinator's state-dir key (an
// exclusive owner.pid lock) must never merge across worktrees, or two
// concurrent worktree sessions collide and the loser silently degrades to an
// ephemeral state dir (task brown-canal, 2026-07-09/10). "tasks aren't
// context" is the whole rationale for redirecting HERE and nowhere else.
//
// A redirect failure (a stale worktree pointer, or an unreadable .git file)
// warns once and returns workDir UNCHANGED rather than propagating the
// error: this call site is fault-tolerant by design (a bad project-identity
// resolution degrades the task store, it never blocks a session launch). In
// practice a stale pointer will already have raised a FATAL pre-launch
// strictness finding (worktreeSignpost, internal/config) unless the caller
// passed --degraded -- itself an explicit "keep going" choice this mirrors.
func taskStoreWorkDir(workDir string) string {
	redirected, err := projectroot.TaskStoreRoot(afero.NewOsFs(), workDir)
	if err != nil {
		clidiag.Warn("ctxloom", "task-store identity: %v; using %s as-is", err, workDir)
		return workDir
	}
	return redirected
}
