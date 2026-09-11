//go:build acceptance

// Shared fixture for ctxloom's per-agent scratch worktrees — the debris a
// crashed run leaves behind, and the only input the startup worktree reaper
// (isolation.ReapOrphanedWorktrees) acts on.
//
// It lives apart from any one journey because more than one feature needs the
// same real artifact: a genuine linked `git worktree add` checkout at the
// exact path isolation.findEphemeralWorktrees scans, beside the SESSION
// liveness lock (internal/shared/sessionlock) the reaper actually probes. A
// second hand-rolled copy of that layout would drift from the first, and the
// reaper would then be asserted against a shape it never sees.
package acceptance

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/gofrs/flock"
)

const (
	// deadOwnerPid is the pid written as a dead session's lock-file CONTENT.
	// Nothing decides liveness from it and nothing may: the lock being FREE
	// is the whole signal (see internal/shared/sessionlock). It is there so a
	// human — and a listing that renders an owner column — has something to
	// read.
	deadOwnerPid = 999999999

	// harpSessionsRel is the harp store, relative to the isolated HOME.
	harpSessionsRel = ".ctxloom/sessions"

	// scratchWorktreePrefix is the directory-name prefix the reaper's
	// candidate finder matches (isolation.worktreeCandidatePrefix, built from
	// worktreeScratchPrefix). A checkout without it is invisible to the sweep.
	scratchWorktreePrefix = "ctxloom-wt-"

	// sessionLockSuffix names the SESSION liveness lock, which sits BESIDE
	// the harp directory (paths.HarpLockPath) rather than inside it — so a
	// sweep can hold the lock while it removes the very directory it
	// describes.
	sessionLockSuffix = ".lock"
)

// isolatedGit runs a git command in dir under the harness's isolated
// environment, so git reads the scenario's fake HOME rather than the
// developer's.
func isolatedGit(w *World, dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = w.env.Command(nil, "version").Env // the isolated env every helper trusts
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("git %s in %s: %s: %w", strings.Join(args, " "), dir, out, err)
	}
	return string(out), nil
}

// harpDirIn returns the absolute path of a harp's directory in the isolated
// home — ~/.ctxloom/sessions/<harp>, the layout paths.HarpDir builds.
func harpDirIn(w *World, harp string) string {
	return filepath.Join(w.env.HomeDir, filepath.FromSlash(harpSessionsRel), harp)
}

// scratchWorktreeDir is where a harp's scratch checkout named name lives:
// <harp>/ephemeral/ctxloom-wt-<name>, the path the candidate finder scans for.
func scratchWorktreeDir(w *World, harp, name string) string {
	return filepath.Join(harpDirIn(w, harp), "ephemeral", scratchWorktreePrefix+name)
}

// seedScratchWorktree creates a REAL linked git worktree of the project repo
// at wtDir on a fresh branch. It says NOTHING about liveness: that belongs to
// the owning SESSION and is seeded once per harp by seedDeadSession /
// seedLiveSession, or left unseeded for the unprovable case.
//
// Requires the project repo to have a commit already: `git worktree add -b`
// needs a valid HEAD to branch from.
//
// THE GUARD IS THE POINT. This function's whole output is an input to a
// DESTRUCTIVE sweep — everything the reaper accepts, it deletes, and it
// resolves its scan root from HOME. So a fixture that planted itself in a real
// home would aim a real `git worktree remove` at a developer's live work. It
// therefore refuses to seed anywhere outside this scenario's own temporary
// root, which fails loudly here rather than silently at removal time.
func seedScratchWorktree(w *World, wtDir, branch string) error {
	root := w.env.Root
	if root == "" || !strings.HasPrefix(wtDir, root+string(os.PathSeparator)) {
		return fmt.Errorf("refusing to seed a reaper fixture at %q: it is not inside this scenario's isolated root %q, so the startup reaper would be pointed at a real home", wtDir, root)
	}
	if err := os.MkdirAll(filepath.Dir(wtDir), 0o755); err != nil {
		return fmt.Errorf("create ephemeral dir for %s: %w", wtDir, err)
	}
	if _, err := isolatedGit(w, w.env.ProjectDir, "worktree", "add", "-q", "-b", branch, wtDir); err != nil {
		return err
	}
	return nil
}

// harpLockPathIn is the SESSION liveness lock for harp inside the scenario's
// isolated home — <home>/.ctxloom/sessions/<harp>.lock, beside the harp dir.
func harpLockPathIn(w *World, harp string) string {
	return harpDirIn(w, harp) + sessionLockSuffix
}

// seedDeadSession makes harp read sessionlock.Dead: its lock file EXISTS and
// nothing holds it, which is exactly what the kernel leaves behind once the
// owning process has ended — gracefully or by SIGKILL, the lock drops either
// way. This is the only state that permits reclaiming.
func seedDeadSession(w *World, harp string) error {
	path := harpLockPathIn(w, harp)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create sessions root for %s: %w", harp, err)
	}
	if err := os.WriteFile(path, fmt.Appendf(nil, "%d\n", deadOwnerPid), 0o600); err != nil {
		return fmt.Errorf("write dead session lock for %s: %w", harp, err)
	}
	return nil
}

// seedLiveSession makes harp read sessionlock.Alive: THIS test process takes a
// genuine exclusive flock on the harp's lock file and holds it for the rest of
// the scenario, so the ctxloom subprocess probing it meets a real held lock.
//
// It locks the path DIRECTLY rather than calling sessionlock.Hold, and that is
// not a shortcut: sessionlock resolves its path from the calling process's own
// home, so Hold here would write into the developer's REAL ~/.ctxloom instead
// of the scenario's isolated one — a fixture reaching into live session state.
func seedLiveSession(w *World, harp string) error {
	path := harpLockPathIn(w, harp)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create sessions root for %s: %w", harp, err)
	}
	if err := os.WriteFile(path, fmt.Appendf(nil, "%d\n", os.Getpid()), 0o600); err != nil {
		return fmt.Errorf("write live session lock for %s: %w", harp, err)
	}
	fl := flock.New(path)
	locked, err := fl.TryLock()
	if err != nil {
		return fmt.Errorf("take %s's session lock: %w", harp, err)
	}
	if !locked {
		return fmt.Errorf("could not take %s's session lock, so this scenario cannot make it read as live", harp)
	}
	w.heldSessionLocks = append(w.heldSessionLocks, fl)
	return nil
}
