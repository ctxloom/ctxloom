//go:build acceptance

// Shared fixture for ctxloom's per-agent scratch worktrees — the debris a
// crashed run leaves behind, and the only input the startup worktree reaper
// (isolation.ReapOrphanedWorktrees) acts on.
//
// It lives apart from any one journey because more than one feature needs the
// same real artifact: a genuine linked `git worktree add` checkout at the
// exact path isolation.findEphemeralWorktrees scans, carrying the sibling
// owner-pid marker isolation.readWorktreeOwner reads. A second hand-rolled
// copy of that layout would drift from the first, and the reaper would then be
// asserted against a shape it never sees.
package acceptance

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	// deadOwnerPid names no live process on any Linux configuration: it
	// exceeds even a kernel.pid_max=4194304 configuration by a wide margin,
	// so "this worktree's owner is confirmed dead" is deterministic with no
	// fork/kill/race. Copied deliberately rather than imported: the isolation
	// package's constant is unexported, and an acceptance fixture that
	// silently changed meaning when that package was refactored would be
	// worse than a duplicated literal with this comment attached.
	deadOwnerPid = 999999999

	// harpSessionsRel is the harp store, relative to the isolated HOME.
	harpSessionsRel = ".ctxloom/sessions"

	// scratchWorktreePrefix is the directory-name prefix the reaper's
	// candidate finder matches (isolation.worktreeCandidatePrefix, built from
	// worktreeScratchPrefix). A checkout without it is invisible to the sweep.
	scratchWorktreePrefix = "ctxloom-wt-"

	// scratchWorktreeOwnerSuffix names the SIBLING marker file recording which
	// process owned a checkout (isolation.worktreeOwnerSuffix). It sits next
	// to the checkout, never inside it, so it can never dirty the tree it
	// describes.
	scratchWorktreeOwnerSuffix = ".owner.pid"
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

// seedScratchWorktree creates a REAL linked git worktree of the project repo at
// wtDir on a fresh branch, plus the sibling owner marker naming ownerPid. An
// ownerPid of 0 writes no marker at all — the "can't prove who owned this"
// case, which the reaper must treat exactly like a live owner.
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
func seedScratchWorktree(w *World, wtDir, branch string, ownerPid int) error {
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
	if ownerPid == 0 {
		return nil
	}
	marker := wtDir + scratchWorktreeOwnerSuffix
	if err := os.WriteFile(marker, fmt.Appendf(nil, "%d\n", ownerPid), 0o600); err != nil {
		return fmt.Errorf("write owner marker for %s: %w", wtDir, err)
	}
	return nil
}
