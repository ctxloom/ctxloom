package isolation

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/git"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWorktree_Axes pins the policy identity: name "worktree", approvals PROMPT
// (config isolation is NOT a security boundary — only container bypasses).
func TestWorktree_Axes(t *testing.T) {
	p := NewWorktree(&git.Fake{})
	assert.Equal(t, "worktree", p.Name())
	assert.Equal(t, ApprovalsPrompt, p.Approvals(), "worktree keeps the engine's in-tool approval prompt")
}

// TestWorktree_PrepareCreatesWorktree: in a repo, PrepareWorkspace adds a detached
// worktree under the OS temp dir (NOT inside the repo) and exposes it as Dir(),
// with per-agent config-home envs.
func TestWorktree_PrepareCreatesWorktree(t *testing.T) {
	common := t.TempDir() // stand-in .git common dir so the exclude write succeeds
	f := &git.Fake{CommonDirValue: common}
	ws, err := NewWorktree(f).PrepareWorkspace(context.Background(), "/proj", "member-a")
	require.NoError(t, err)

	assert.True(t, strings.HasPrefix(ws.Dir(), os.TempDir()), "worktree lives under the OS temp dir, not the repo tree")
	assert.NotContains(t, ws.Dir(), "/proj/", "worktree is not created inside the project tree")

	// The Fake records exactly one detached HEAD add.
	require.Len(t, f.Calls, 1)
	assert.True(t, strings.HasPrefix(f.Calls[0], "add "), "one worktree add")
	assert.Contains(t, f.Calls[0], "@HEAD", "checked out to HEAD")

	env := WorkspaceEnv(ws)
	require.NotNil(t, env, "worktree exposes per-agent config-home envs")
	assert.Contains(t, env["CLAUDE_CONFIG_DIR"], "ctxloom-cfg-")
	assert.Contains(t, env["CODEX_HOME"], "ctxloom-cfg-")
	assert.Contains(t, env["KIRO_HOME"], "ctxloom-cfg-")

	// §3.1: the broadened config excludes land in the common-dir info/exclude.
	excl, err := os.ReadFile(filepath.Join(common, "info", "exclude"))
	require.NoError(t, err, "the exclude file is written to the common dir")
	assert.Contains(t, string(excl), ".mcp.json")
	assert.Contains(t, string(excl), ".claude/")

	require.NoError(t, ws.Cleanup())
}

// TestWorktree_SkipsTrackedConfig proves §3.1's TRACKED-config arm: any repo-
// tracked per-agent config file in the new worktree gets the skip-worktree bit, so
// ctxloom's edits to a committed .mcp.json/.claude/ never ride a developer member's
// merge-back (the info/exclude covers only the UNTRACKED case).
func TestWorktree_SkipsTrackedConfig(t *testing.T) {
	common := t.TempDir()
	f := &git.Fake{
		CommonDirValue: common,
		TrackedFiles:   []string{".mcp.json", ".claude/settings.json"},
	}
	ws, err := NewWorktree(f).PrepareWorkspace(context.Background(), "/proj", "member-t")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ws.Cleanup() })

	assert.Contains(t, f.Calls, "skip-worktree(true) .mcp.json")
	assert.Contains(t, f.Calls, "skip-worktree(true) .claude/settings.json")
}

// TestWorktree_DegradesOnNonRepo: a non-git dir makes PrepareWorkspace error so
// the caller degrades to None. None never fails.
func TestWorktree_DegradesOnNonRepo(t *testing.T) {
	f := &git.Fake{Repos: map[string]bool{}} // no dirs are repos
	_, err := NewWorktree(f).PrepareWorkspace(context.Background(), "/not-a-repo", "m")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a git repository")
	assert.Empty(t, f.Calls, "no worktree add is attempted on a non-repo")
}

// TestWorktree_DegradesOnAddFailure: a worktree-add failure errors (→ caller
// degrades), never returning a half-built workspace.
func TestWorktree_DegradesOnAddFailure(t *testing.T) {
	f := &git.Fake{AddErr: assertErr("disk full")}
	_, err := NewWorktree(f).PrepareWorkspace(context.Background(), "/proj", "m")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "worktree add")
}

// TestWorktree_TeardownNestedFirst is the core WIP-safety ordering test: an inner
// worktree nested under ours (as claude's EnterWorktree creates) must be removed
// BEFORE the outer, so removing the outer never silently destroys the inner.
func TestWorktree_TeardownNestedFirst(t *testing.T) {
	outer := filepath.Join(os.TempDir(), "ctxloom-wt-x")
	inner := filepath.Join(outer, ".claude", "worktrees", "inner")

	f := &git.Fake{Worktrees: []git.Worktree{
		{Path: "/proj"},               // the main repo
		{Path: outer},                 // ours
		{Path: inner, Detached: true}, // claude's nested inner (clean)
	}}
	ws := &worktreeWorkspace{git: f, repoDir: "/proj", dir: outer}
	require.NoError(t, ws.Cleanup())

	// Ordering: inner removed, THEN outer, THEN prune.
	assert.Equal(t, []string{inner, outer}, f.Removed, "inner removed before outer")
	require.NotEmpty(t, f.Calls)
	assert.Equal(t, "prune", f.Calls[len(f.Calls)-1], "prune runs last")

	innerIdx := indexOf(f.Calls, "remove(force=false) "+inner)
	outerIdx := indexOf(f.Calls, "remove(force=false) "+outer)
	require.GreaterOrEqual(t, innerIdx, 0)
	require.GreaterOrEqual(t, outerIdx, 0)
	assert.Less(t, innerIdx, outerIdx, "the inner remove is ordered before the outer remove")
}

// TestWorktree_TeardownAbortsOnInnerWIP is the WIP-abort test: a DIRTY nested
// inner must abort the whole teardown — neither the inner nor the outer is
// removed, so uncommitted work is never destroyed. This is the exact footgun that
// lost work before ([[worktree-wip-safety]]).
func TestWorktree_TeardownAbortsOnInnerWIP(t *testing.T) {
	outer := filepath.Join(os.TempDir(), "ctxloom-wt-y")
	inner := filepath.Join(outer, ".claude", "worktrees", "inner")

	f := &git.Fake{
		Worktrees: []git.Worktree{{Path: outer}, {Path: inner, Detached: true}},
		Dirty:     map[string]bool{inner: true}, // the inner carries WIP
	}
	ws := &worktreeWorkspace{git: f, repoDir: "/proj", dir: outer}
	require.NoError(t, ws.Cleanup(), "cleanup never returns an error (fault tolerant)")

	assert.Empty(t, f.Removed, "NOTHING is removed when a nested worktree has WIP")
	assert.NotContains(t, f.Calls, "prune", "no prune after an aborted teardown")
}

// TestWorktree_TeardownAbortsOnOuterWIP: the outer's own uncommitted work leaves
// it in place (force=false; git would refuse anyway). WIP is sacred.
func TestWorktree_TeardownAbortsOnOuterWIP(t *testing.T) {
	outer := filepath.Join(os.TempDir(), "ctxloom-wt-z")
	f := &git.Fake{
		Worktrees: []git.Worktree{{Path: outer}},
		Dirty:     map[string]bool{outer: true},
	}
	ws := &worktreeWorkspace{git: f, repoDir: "/proj", dir: outer}
	require.NoError(t, ws.Cleanup())
	assert.Empty(t, f.Removed, "a dirty outer worktree is preserved, not removed")
}

// TestWorktree_TeardownLeaksOnListFailure: if the repo-global list can't be read,
// the teardown does NOT blind-remove (a hidden nested inner's WIP could be lost) —
// it leaks the worktree and warns.
func TestWorktree_TeardownLeaksOnListFailure(t *testing.T) {
	f := &git.Fake{ListErr: assertErr("git broke")}
	ws := &worktreeWorkspace{git: f, repoDir: "/proj", dir: "/tmp/ctxloom-wt-q"}
	require.NoError(t, ws.Cleanup())
	assert.Empty(t, f.Removed, "no removal when the worktree list is unavailable")
}

// TestProvisionConfigHome_OwnerOnly: the per-agent config-home holds engine
// creds/state (CLAUDE_CONFIG_DIR & co.) in the SHARED OS temp dir — it must be
// owner-only (0700) like every MkdirTemp sibling in this package, never
// world-traversable.
func TestProvisionConfigHome_OwnerOnly(t *testing.T) {
	home := Worktree{}.provisionConfigHome("agent-x")
	require.NotEmpty(t, home)
	t.Cleanup(func() { _ = os.RemoveAll(home) })

	info, err := os.Stat(home)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm(), "engine creds/state dir is owner-only")
}

// TestWorktreeCleanup_SurfacesConfigHomeResidue: the config-home removal was a
// bare `_ = os.RemoveAll` — an unremovable tree (e.g. wrongly-owned files)
// must stream a warning naming the path, never vanish silently.
func TestWorktreeCleanup_SurfacesConfigHomeResidue(t *testing.T) {
	home := brokenScratch(t)
	target := filepath.Join(os.TempDir(), "ctxloom-wt-res")
	f := &git.Fake{Worktrees: []git.Worktree{{Path: target}}}
	ws := &worktreeWorkspace{git: f, repoDir: "/proj", dir: target, configHome: home}

	done := captureStderr(t)
	require.NoError(t, ws.Cleanup())
	stderr := done()

	assert.Contains(t, stderr, home, "the warning names the residue path")
	assert.Contains(t, stderr, "sudo rm", "…and the manual fix")
}

// TestWorktree_CleanupIdempotent: a second Cleanup is a noop (no double-teardown).
func TestWorktree_CleanupIdempotent(t *testing.T) {
	outer := filepath.Join(os.TempDir(), "ctxloom-wt-i")
	f := &git.Fake{Worktrees: []git.Worktree{{Path: outer}}}
	ws := &worktreeWorkspace{git: f, repoDir: "/proj", dir: outer}
	require.NoError(t, ws.Cleanup())
	before := len(f.Calls)
	require.NoError(t, ws.Cleanup())
	assert.Equal(t, before, len(f.Calls), "second cleanup makes no further git calls")
}

// TestResolveWorktree wires the workspace axis through Resolve.
func TestResolveWorktree(t *testing.T) {
	p := Resolve(Axes{Workspace: WorkspaceWorktree}, "claude-code", ImageConfig{})
	assert.Equal(t, "worktree", p.Name())
	assert.IsType(t, Worktree{}, p)
}

func indexOf(ss []string, want string) int {
	for i, s := range ss {
		if s == want {
			return i
		}
	}
	return -1
}

type assertErr string

func (e assertErr) Error() string { return string(e) }

// TestWorktree_ScratchRelocatesIntoHarpEphemeral: a run that carries a session
// harp homes BOTH per-agent scratch dirs (the checkout and the config-home)
// under the session's ephemeral/ dir — the §6d layout: regenerable state in
// one inspectable per-session place — instead of the OS temp dir. The no-harp
// case keeps the temp dir (pinned by TestWorktree_PrepareCreatesWorktree).
func TestWorktree_ScratchRelocatesIntoHarpEphemeral(t *testing.T) {
	home := testsupport.Isolate(t)
	f := &git.Fake{CommonDirValue: t.TempDir()}

	w := NewWorktree(f)
	w.state = SessionState{Harp: "brisk-teal-otter"}
	ws, err := w.PrepareWorkspace(context.Background(), "/proj", "member-a")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ws.Cleanup() })

	eph := filepath.Join(home, ".ctxloom", "sessions", "brisk-teal-otter", "ephemeral")
	assert.True(t, strings.HasPrefix(ws.Dir(), eph+string(os.PathSeparator)),
		"checkout %q lives under the session ephemeral dir", ws.Dir())

	env := WorkspaceEnv(ws)
	require.NotNil(t, env)
	assert.True(t, strings.HasPrefix(env["CLAUDE_CONFIG_DIR"], eph+string(os.PathSeparator)),
		"config-home %q lives under the session ephemeral dir", env["CLAUDE_CONFIG_DIR"])
}
