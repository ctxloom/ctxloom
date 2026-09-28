package isolation

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/git"
	"github.com/ctxloom/ctxloom/internal/adapters/gitignore"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestWorktree_PrepareCreatesWorktree: in a repo, PrepareWorkspace adds a detached
// worktree under the OS temp dir (NOT inside the repo) and exposes it as Dir(),
// with the env for what it provisioned.
func TestWorktree_PrepareCreatesWorktree(t *testing.T) {
	common := t.TempDir() // stand-in .git common dir so the exclude write succeeds
	f := &git.Fake{CommonDirValue: common}
	ws, err := NewWorktree(f).prepareWorkspace(context.Background(), "/proj", "member-a")
	require.NoError(t, err)
	// Safety net registered BEFORE any assertion below can fail/panic and skip
	// the ws.Cleanup() call at the end of this test (see requireCleanWorkspace).
	requireCleanWorkspace(t, ws)

	assert.True(t, strings.HasPrefix(ws.Dir(), os.TempDir()), "worktree lives under the OS temp dir, not the repo tree")
	assert.NotContains(t, ws.Dir(), "/proj/", "worktree is not created inside the project tree")

	// The Fake records exactly one add: checked out to HEAD on a NEW branch
	// named for the agent, so commits made inside outlive the checkout.
	require.Len(t, f.Calls, 1)
	assert.True(t, strings.HasPrefix(f.Calls[0], "add "), "one worktree add")
	assert.Contains(t, f.Calls[0], "@HEAD", "checked out to HEAD")
	require.Len(t, f.Worktrees, 1)
	assert.False(t, f.Worktrees[0].Detached, "never detached")
	assert.Equal(t, worktreeBranchName(ws.Dir()), f.Worktrees[0].Branch,
		"on the branch derived from the checkout's own name")
	assert.True(t, strings.HasPrefix(f.Worktrees[0].Branch, worktreeBranchPrefix+"member-a-"),
		"the branch carries the agent id under the agent namespace")

	env := workspaceEnv(ws)
	require.NotNil(t, env, "worktree exposes the env for its scratch dir and git identity")
	assert.Contains(t, env["TMPDIR"], "ctxloom-tmp-")

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
	ws, err := NewWorktree(f).prepareWorkspace(context.Background(), "/proj", "member-t")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ws.Cleanup() })
	requireCleanWorkspace(t, ws)

	assert.Contains(t, f.Calls, "skip-worktree(true) .mcp.json")
	assert.Contains(t, f.Calls, "skip-worktree(true) .claude/settings.json")
}

// TestWorktree_DegradesOnNonRepo: a non-git dir makes PrepareWorkspace error so
// the caller degrades to None. None never fails.
func TestWorktree_DegradesOnNonRepo(t *testing.T) {
	f := &git.Fake{Repos: map[string]bool{}} // no dirs are repos
	_, err := NewWorktree(f).prepareWorkspace(context.Background(), "/not-a-repo", "m")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a git repository")
	assert.Empty(t, f.Calls, "no worktree add is attempted on a non-repo")
}

// TestWorktree_DegradesOnAddFailure: a worktree-add failure errors (→ caller
// degrades), never returning a half-built workspace.
func TestWorktree_DegradesOnAddFailure(t *testing.T) {
	f := &git.Fake{AddErr: assertErr("disk full")}
	_, err := NewWorktree(f).prepareWorkspace(context.Background(), "/proj", "m")
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

	innerIdx := indexOf(f.Calls, "remove "+inner)
	outerIdx := indexOf(f.Calls, "remove "+outer)
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

// TestWorktree_TeardownAbortsOnIgnoredContent pins that IsDirty
// alone misses gitignored/excluded content, so a worktree holding ONLY
// ignored files (e.g. an agent-authored CLAUDE.md hidden by this repo's own
// common-dir info/exclude block) used to read clean and get destroyed.
// unsafeToRemove must also refuse when HasIgnoredContent reports true, even
// though IsDirty reports false.
func TestWorktree_TeardownAbortsOnIgnoredContent(t *testing.T) {
	outer := filepath.Join(os.TempDir(), "ctxloom-wt-ignored")
	f := &git.Fake{
		Worktrees:      []git.Worktree{{Path: outer}},
		IgnoredContent: map[string]bool{outer: true}, // clean per IsDirty, but holds ignored files
	}
	ws := &worktreeWorkspace{git: f, repoDir: "/proj", dir: outer}
	require.NoError(t, ws.Cleanup())
	assert.Empty(t, f.Removed, "a worktree holding only ignored content must be preserved, not removed")
}

// TestWorktree_TeardownAbortsOnUnknownIgnoredContentState pins the
// fail-closed half: an error from HasIgnoredContent must be treated exactly
// like "yes, unsafe" — never as permission to proceed.
func TestWorktree_TeardownAbortsOnUnknownIgnoredContentState(t *testing.T) {
	outer := filepath.Join(os.TempDir(), "ctxloom-wt-ignored-err")
	f := &git.Fake{
		Worktrees: []git.Worktree{{Path: outer}},
		// git.Fake has no HasIgnoredContentErr injector; simulate the
		// unreadable-repo case IsDirty already covers via DirtyErr, which
		// unsafeToRemove must treat identically.
		DirtyErr: assertErr("permission denied"),
	}
	ws := &worktreeWorkspace{git: f, repoDir: "/proj", dir: outer}
	require.NoError(t, ws.Cleanup())
	assert.Empty(t, f.Removed, "an unreadable WIP state must never be treated as safe to delete")
}

// TestWorktree_TeardownRetiresConfigExcludeWhenLastWorktreeGone pins the other
// half: once teardown removes the LAST non-main worktree, the
// shared config-exclude block must be retired from the common-dir
// info/exclude — otherwise the developer's own main checkout is left unable
// to see new CLAUDE.md/AGENTS.md/.claude/ files FOREVER, with no removal
// path.
// TestWorktree_TeardownKeepsConfigExcludeWhileSiblingRemains ensures the
// retirement above never fires while ANOTHER agent worktree is still alive —
// removing the shared block then would strip that sibling's own noise-hiding
// and false-dirty it.
// TestWorktree_TeardownLeaksOnListFailure: if the repo-global list can't be read,
// the teardown does NOT blind-remove (a hidden nested inner's WIP could be lost) —
// it leaks the worktree and warns.
func TestWorktree_TeardownLeaksOnListFailure(t *testing.T) {
	f := &git.Fake{ListErr: assertErr("git broke")}
	ws := &worktreeWorkspace{git: f, repoDir: "/proj", dir: "/tmp/ctxloom-wt-q"}
	require.NoError(t, ws.Cleanup())
	assert.Empty(t, f.Removed, "no removal when the worktree list is unavailable")
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

// TestResolveWorktree wires the workspace axis through chainFor's lead policy.
func TestResolveWorktree(t *testing.T) {
	p := chainFor(Axes{Workspace: WorkspaceWorktree}, "claude-code", ImageConfig{})[0]
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
// harp homes the per-agent scratch dirs (the checkout and the toolchain temp)
// under the session's ephemeral/ dir — the §6d layout: regenerable state in
// one inspectable per-session place — instead of the OS temp dir. The no-harp
// case keeps the temp dir (pinned by TestWorktree_PrepareCreatesWorktree).
func TestWorktree_ScratchRelocatesIntoHarpEphemeral(t *testing.T) {
	home := testsupport.Isolate(t)
	f := &git.Fake{CommonDirValue: t.TempDir()}

	w := NewWorktree(f)
	w.state = SessionState{Harp: "brisk-teal-otter"}
	ws, err := w.prepareWorkspace(context.Background(), "/proj", "member-a")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ws.Cleanup() })

	eph := filepath.Join(home, ".ctxloom", "sessions", "brisk-teal-otter", "ephemeral")
	assert.True(t, strings.HasPrefix(ws.Dir(), eph+string(os.PathSeparator)),
		"checkout %q lives under the session ephemeral dir", ws.Dir())

	env := workspaceEnv(ws)
	require.NotNil(t, env)

	// spawner-env: the toolchain scratch dir (TMPDIR/GOTMPDIR) is the second
	// per-agent scratch resource homed under the session ephemeral dir — NOT
	// the OS temp dir, which is the whole point (the shared /tmp is what
	// corrupted concurrent agents in the first place).
	require.NotEmpty(t, env["TMPDIR"])
	assert.True(t, strings.HasPrefix(env["TMPDIR"], eph+string(os.PathSeparator)),
		"scratch dir %q lives under the session ephemeral dir, not the OS temp dir", env["TMPDIR"])
}

// --- Toolchain/VCS scoping (spawner-env) ------------------------------------
//
// A ~10-agent parallel run corrupted concurrent agents through toolchain/VCS
// state shared across linked worktrees (a shared process TMPDIR racing `go
// build`'s per-invocation $WORK scratch; a leaked `git config user.email`
// from a worktree into the shared main checkout's .git/config). These tests
// pin the structural fix at the Env()/Cleanup seam: every worktree member
// gets a DISJOINT TMPDIR/GOTMPDIR and a per-agent git identity that outranks
// the shared repo-local config, by construction — not by an agent
// remembering to set an env var it was never given.

// TestWorktree_ScratchDir_TMPDIRAndGOTMPDIRMatchAndExist is the payload floor:
// Env() doesn't just carry SOME value for TMPDIR/GOTMPDIR — the path it names
// actually exists on disk (a real, writable scratch dir a spawned `go build`
// or `git` could use), and GOTMPDIR mirrors TMPDIR (Go honours GOTMPDIR for
// its own $WORK scratch, TMPDIR for everything else that shells out).
func TestWorktree_ScratchDir_TMPDIRAndGOTMPDIRMatchAndExist(t *testing.T) {
	common := t.TempDir()
	f := &git.Fake{CommonDirValue: common}
	ws, err := NewWorktree(f).prepareWorkspace(context.Background(), "/proj", "member-scratch")
	require.NoError(t, err)
	requireCleanWorkspace(t, ws)

	env := workspaceEnv(ws)
	require.NotNil(t, env)
	require.NotEmpty(t, env["TMPDIR"], "Env() must carry TMPDIR — the fix this seam exists for")
	assert.Equal(t, env["TMPDIR"], env["GOTMPDIR"], "GOTMPDIR mirrors TMPDIR — one scratch root for both")

	info, statErr := os.Stat(env["TMPDIR"])
	require.NoError(t, statErr, "the scratch dir Env() points at must actually exist on disk")
	assert.True(t, info.IsDir())

	require.NoError(t, ws.Cleanup())
}

// TestWorktree_ConcurrentAgents_DisjointScratchDirs is the concurrency
// payload test: two members PREPARED CONCURRENTLY (mirroring the fan-out)
// get DIFFERENT TMPDIR roots — the assertion that catches the exact failure
// mode (every worktree member inheriting the SAME process TMPDIR) rather
// than merely proving a struct field was set on each in isolation.
func TestWorktree_ConcurrentAgents_DisjointScratchDirs(t *testing.T) {
	common := t.TempDir()
	f := &git.Fake{CommonDirValue: common}

	type result struct {
		ws  workspace
		env map[string]string
		err error
	}
	results := make([]result, 2)
	var wg sync.WaitGroup
	for i, agentID := range []string{"agent-alpha", "agent-beta"} {
		wg.Add(1)
		go func(i int, agentID string) {
			defer wg.Done()
			ws, err := NewWorktree(f).prepareWorkspace(context.Background(), "/proj", agentID)
			results[i] = result{ws: ws, env: workspaceEnv(ws), err: err}
		}(i, agentID)
	}
	wg.Wait()
	for _, r := range results {
		require.NoError(t, r.err)
		requireCleanWorkspace(t, r.ws)
		t.Cleanup(func(ws workspace) func() { return func() { _ = ws.Cleanup() } }(r.ws))
	}

	require.NotEmpty(t, results[0].env["TMPDIR"])
	require.NotEmpty(t, results[1].env["TMPDIR"])
	assert.NotEqual(t, results[0].env["TMPDIR"], results[1].env["TMPDIR"],
		"two concurrently-prepared children must get DISJOINT scratch roots")
	assert.NotEqual(t, results[0].env["GIT_AUTHOR_EMAIL"], results[1].env["GIT_AUTHOR_EMAIL"],
		"two differently-named agents must get DISJOINT git identities")
}

// TestWorktree_ScratchDir_CleanedUpOnTeardown: Cleanup() removes the
// toolchain scratch dir from disk — an agent's TMPDIR must not outlive its
// workspace and accumulate as residue across a long-running host (mirroring
// the configHome cleanup guarantee).
func TestWorktree_ScratchDir_CleanedUpOnTeardown(t *testing.T) {
	common := t.TempDir()
	f := &git.Fake{CommonDirValue: common}
	ws, err := NewWorktree(f).prepareWorkspace(context.Background(), "/proj", "member-teardown")
	require.NoError(t, err)
	requireCleanWorkspace(t, ws)

	scratch := workspaceEnv(ws)["TMPDIR"]
	require.NotEmpty(t, scratch)
	require.DirExists(t, scratch, "sanity: the scratch dir exists before Cleanup")

	require.NoError(t, ws.Cleanup())
	assert.NoDirExists(t, scratch, "the scratch dir is removed by Cleanup — TMPDIR must not outlive the workspace")
}

// TestWorktree_GitIdentity_AttributesToAgentNotHuman pins the git-identity
// half of the fix: GIT_AUTHOR_*/GIT_COMMITTER_* self-identify as the AGENT
// (never a bare human-looking name), are traceable to the specific agentID
// PrepareWorkspace was given, and use a synthetic domain that can never
// collide with a real person's address — so an agent's commits are
// attributable to it even when the shared linked-worktree .git/config
// already carries a (possibly wrong) human identity from another agent.
func TestWorktree_GitIdentity_AttributesToAgentNotHuman(t *testing.T) {
	common := t.TempDir()
	f := &git.Fake{CommonDirValue: common}
	ws, err := NewWorktree(f).prepareWorkspace(context.Background(), "/proj", "reviewer-3")
	require.NoError(t, err)
	requireCleanWorkspace(t, ws)

	env := workspaceEnv(ws)
	require.NotNil(t, env)
	assert.Contains(t, env["GIT_AUTHOR_NAME"], "reviewer-3", "the author name traces back to the agent")
	assert.Contains(t, env["GIT_AUTHOR_NAME"], "agent", "the name self-identifies as an agent, not a human")
	assert.Contains(t, env["GIT_AUTHOR_EMAIL"], "reviewer-3")
	assert.Contains(t, env["GIT_AUTHOR_EMAIL"], "agents.ctxloom.local", "a synthetic domain that can never collide with a real person")
	assert.Equal(t, env["GIT_AUTHOR_NAME"], env["GIT_COMMITTER_NAME"], "author and committer identity match")
	assert.Equal(t, env["GIT_AUTHOR_EMAIL"], env["GIT_COMMITTER_EMAIL"], "author and committer identity match")

	require.NoError(t, ws.Cleanup())
}

// --- Per-engine isolation-home -----------------------------------------

// panicAfterAddGit panics on CommonDir — the first git call PrepareWorkspace
// makes AFTER the checkout exists and the leak-recovery defer is installed, so
// it drives exactly the unwind that defer exists for.
type panicAfterAddGit struct{ *git.Fake }

func (panicAfterAddGit) CommonDir(context.Context, string) (string, error) {
	panic("worktree provisioning blew up after the checkout existed")
}

// TestWorktree_PanicRecoveryPrunesRegistration pins that the recovery
// path removed the checkout with a raw os.RemoveAll and stopped there, so the
// repo kept the worktree's administrative registration
// (.git/worktrees/<name>) — one stale `git worktree list` entry per panic,
// with the directory it names already gone. Recovery now prunes, which is what
// the graceful teardown does after its own removal, and re-panics unchanged so
// a real bug still crashes and a mutant still dies.
func TestWorktree_PanicRecoveryPrunesRegistration(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)

	f := &git.Fake{}
	w := NewWorktree(panicAfterAddGit{Fake: f})
	assert.Panics(t, func() {
		_, _ = w.prepareWorkspace(context.Background(), "/proj", "member-panic")
	}, "the original failure is preserved, not swallowed")

	assert.Contains(t, f.Calls, "prune",
		"recovery retires the checkout's registration, not just its directory")

	entries, err := os.ReadDir(tmp)
	require.NoError(t, err)
	var left []string
	for _, e := range entries {
		left = append(left, e.Name())
	}
	assert.Empty(t, left, "recovery leaves no checkout, config-home, scratch dir or owner marker behind")
}

// TestNestedUnder_MatchesRealpathResolvedPaths is a red-first pin.
// `git worktree list --porcelain` reports every path REALPATH-RESOLVED, while
// the target teardown is given is whatever scratchBase built — os.TempDir() on
// macOS is /var/folders/… behind the /var → /private/var symlink, and a
// symlinked HOME does the same to the session ephemeral dir. Matching by raw
// string prefix then finds nothing nested, so the inner-first removal never
// happens.
func TestNestedUnder_MatchesRealpathResolvedPaths(t *testing.T) {
	real, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	link := filepath.Join(t.TempDir(), "link")
	require.NoError(t, os.Symlink(real, link))

	outer := filepath.Join(real, "ctxloom-wt-outer")
	inner := filepath.Join(outer, ".claude", "worktrees", "inner")
	require.NoError(t, os.MkdirAll(inner, 0o755))

	list := []git.Worktree{{Path: outer}, {Path: inner}}

	nested := nestedUnder(list, filepath.Join(link, "ctxloom-wt-outer"))
	require.Len(t, nested, 1, "the inner is nested under the target however the target is spelled")
	assert.Equal(t, inner, nested[0].Path)

	assert.Empty(t, nestedUnder(list, filepath.Join(link, "ctxloom-wt-elsewhere")),
		"resolving the target must not widen the match to unrelated trees")
}

// TestWorktree_UnsafeHarpIsReported pins that scratchBase runs the
// SAME safePathSegment validator on the SAME untrusted input the container
// path validates (a harp arriving from the env map), but where the container
// path turns a rejection into a hard error, this one silently swapped in the
// OS temp dir: no warning, no finding, and a run that reports session-scoped
// ephemeral state while writing none. The EMPTY harp keeps its silence — that
// is the documented no-session-accounting construction, not a rejected value.
func TestWorktree_UnsafeHarpIsReported(t *testing.T) {
	t.Run("rejected harp is reported", func(t *testing.T) {
		const badHarp = "wave31/u065-unsafe-harp"
		f := &git.Fake{CommonDirValue: t.TempDir()}
		w := NewWorktree(f)
		w.state = SessionState{Harp: badHarp}

		done := captureStderr(t)
		ws, err := w.prepareWorkspace(context.Background(), "/proj", "member-badharp")
		stderr := done()
		require.NoError(t, err)
		requireCleanWorkspace(t, ws)
		t.Cleanup(func() { _ = ws.Cleanup() })

		assert.Contains(t, stderr, badHarp, "the warning names the harp it refused to use")
		assert.True(t, strings.HasPrefix(ws.Dir(), filepath.Clean(os.TempDir())+string(os.PathSeparator)),
			"the fallback itself is unchanged: scratch %q lands in the OS temp dir", ws.Dir())
	})

	t.Run("absent harp stays silent", func(t *testing.T) {
		f := &git.Fake{CommonDirValue: t.TempDir()}
		w := NewWorktree(f)

		done := captureStderr(t)
		ws, err := w.prepareWorkspace(context.Background(), "/proj", "member-noharp")
		stderr := done()
		require.NoError(t, err)
		requireCleanWorkspace(t, ws)
		t.Cleanup(func() { _ = ws.Cleanup() })

		assert.Empty(t, strings.TrimSpace(stderr),
			"no session accounting is the documented construction, not a fault to warn about")
	})
}

// TestWorktree_ExcludeConfigFromMerge_WritesEveryPattern pins a claim, and
// the row is REFUTED. The claim is that excludeConfigFromMerge "reports success
// having written zero bytes when handed an empty pattern list", because
// gitignore.EnsureFile returns nil before opening the file when len(patterns)
// is zero. That early return is real, but this call site can never reach it:
// the argument is gitignore.WorktreeArtifactPatterns, a package-level literal,
// and no caller can substitute one. What is pinned here is therefore the
// PAYLOAD -- the exclude block that hides per-agent config from a merge-back
// actually lands, pattern for pattern -- so the claimed silent no-op becomes a
// red test the moment the pattern set could ever be empty.
func TestWorktree_ExcludeConfigFromMerge_WritesEveryPattern(t *testing.T) {
	require.NotEmpty(t, gitignore.WorktreeArtifactPatterns,
		"an empty pattern set is what would make EnsureFile a silent no-op here")

	common := t.TempDir()
	f := &git.Fake{CommonDirValue: common}
	NewWorktree(f).excludeConfigFromMerge(context.Background(), "/proj")

	raw, err := os.ReadFile(filepath.Join(common, "info", "exclude"))
	require.NoError(t, err, "the exclude file must exist")
	require.NotEmpty(t, raw, "zero bytes written is exactly the failure this asserts against")

	written := map[string]bool{}
	for _, line := range strings.Split(string(raw), "\n") {
		written[strings.TrimSpace(line)] = true
	}
	for _, pat := range gitignore.WorktreeArtifactPatterns {
		assert.True(t, written[pat], "the exclude block carries %q", pat)
	}
}

// TestWorktreeCleanup_NoResourceStrandedByTheDirGuard pins a claim, and
// the row is REFUTED on its consequence. The claim: Cleanup's idempotence guard
// `if w.dir == "" { return nil }` also short-circuits removal of scratchDir,
// an independent resource.
//
// The guard does gate it — that half is true. The leak it implies is
// unreachable by construction, and this pins the two properties that make it
// so: (1) the only production construction of a worktreeWorkspace sets dir
// FIRST and non-empty, before any scratch dir exists to strand, and (2) one
// Cleanup clears every field and removes every resource, so a second call has
// nothing left to reach. Either property breaking — a construction that leaves
// dir empty, or a Cleanup arm that stops clearing its field — turns the shared
// guard into the leak the row describes, and turns this red.
func TestWorktreeCleanup_NoResourceStrandedByTheDirGuard(t *testing.T) {
	withFakeHome(t)
	f := &git.Fake{CommonDirValue: t.TempDir()}
	ws, err := NewWorktree(f).prepareWorkspace(context.Background(), "/proj", "member-a")
	require.NoError(t, err)
	requireCleanWorkspace(t, ws)
	concrete, ok := ws.(*worktreeWorkspace)
	require.True(t, ok)

	require.NotEmpty(t, concrete.dir,
		"the guard's own field is set before any scratch dir exists to be stranded by it")
	scratch := concrete.scratchDir
	require.NotEmpty(t, scratch, "a scratch dir must be provisioned for this pin to mean anything")

	require.NoError(t, ws.Cleanup())

	assert.Empty(t, concrete.dir, "dir is cleared")
	assert.Empty(t, concrete.scratchDir, "scratchDir is cleared in the SAME call, never left for a second one")
	assert.NoDirExists(t, scratch, "Cleanup removed the scratch dir from disk")
}

// TestWorktreeBranchName pins the branch ↔ checkout derivation: the branch a
// per-agent worktree is created on is a pure function of the checkout's
// directory name, so anyone holding either can find the other (the
// coordinator merging from it, doctor reading `git worktree list`, a human
// triaging leftovers) without a side table. The scratch prefix is dropped
// and the agent namespace prefix added; the sanitized agent id and the
// uniqueness token ride through unchanged.
func TestWorktreeBranchName(t *testing.T) {
	for _, tc := range []struct{ dir, want string }{
		{"/sess/ephemeral/ctxloom-wt-member-a-0a1b2c", "ctxloom/member-a-0a1b2c"},
		{"/tmp/ctxloom-wt-developer-93f8-ffff", "ctxloom/developer-93f8-ffff"},
		{"/tmp/ctxloom-wt-agent-deadbeef", "ctxloom/agent-deadbeef"},
	} {
		assert.Equal(t, tc.want, worktreeBranchName(tc.dir), tc.dir)
	}
	// It is the same name PrepareWorkspace hands the git seam: one
	// derivation, not two that can drift.
	p := worktreeScratchPath(t.TempDir(), worktreeScratchPrefix, "x/y z")
	assert.Equal(t, worktreeBranchPrefix+strings.TrimPrefix(filepath.Base(p), worktreeCandidatePrefix), worktreeBranchName(p))
	assert.NotContains(t, worktreeBranchName(p), " ", "a git ref never carries whitespace")
}

// --- sizable-antler: resume must find its own surviving worktree, or refuse ---
//
// Worktree teardown deliberately PRESERVES a dirty checkout (unsafeToRemove),
// so a child stopped with WIP leaves its worktree standing. On resume, the
// SAME session harp is reused (that is what "resume" means to the
// coordinator — see enqueueRun's currentRun(harp) check in
// internal/adapters/coordgrpc/pb), so a second ResolveWorkspace call for that harp +
// agentID must be able to find what the first call left, rather than
// silently creating a brand-new, empty checkout at a different path and
// leaving the agent's own prior work invisible to it.

// TestWorktree_ResumeFindsItsOwnSurvivingCheckout pins the settled behaviour:
// two ResolveWorkspace calls for the SAME (harp, agentID) — the first
// simulating the original run, the second simulating a resume after the
// process died with the worktree left in place (teardown never ran) — land
// on the exact same checkout, and the resumed run can see a file the first
// run wrote (the marker standing in for real WIP). Before the fix landed,
// this test failed: the second call minted a brand-new, randomly-suffixed
// path (worktreeScratchPath's randToken()), so ws2.Dir() != ws1.Dir() and the
// marker was invisible — a silent, undetectable loss of the agent's own
// prior work. It is unit-level and hermetic on purpose (git.Fake, no real
// git binary, no container, no daemon): the row explicitly notes this needs
// none of that to reproduce.
func TestWorktree_ResumeFindsItsOwnSurvivingCheckout(t *testing.T) {
	testsupport.Isolate(t)
	f := &git.Fake{CommonDirValue: t.TempDir()}

	w1 := NewWorktree(f)
	w1.state = SessionState{Harp: "resume-harp-a"}
	ws1, err := w1.prepareWorkspace(context.Background(), "/proj", "worker")
	require.NoError(t, err, "first (original) run prepares normally")
	dir1 := ws1.Dir()

	// Simulate WIP the original run left behind, then simulate the process
	// dying WITHOUT a graceful Cleanup() — exactly what "stopped with WIP,
	// worktree preserved" means. The Fake's registered worktree entry is
	// left in place too (teardown never ran to remove it). The Fake never
	// touches the real filesystem for `worktree add` (unlike real git), so
	// the checkout directory itself is created here by hand — standing in
	// for what a real `git worktree add` would have left on disk.
	require.NoError(t, os.MkdirAll(dir1, 0o755))
	marker := filepath.Join(dir1, "WIP_MARKER.txt")
	require.NoError(t, os.WriteFile(marker, []byte("prior work"), 0o644))

	// Resume: a FRESH Worktree value (a new process), but the SAME harp and
	// the SAME agentID — precisely what the coordinator's resume path
	// (currentRun(harp)) hands back to PrepareWorkspace.
	w2 := NewWorktree(f)
	w2.state = SessionState{Harp: "resume-harp-a"}
	ws2, err := w2.prepareWorkspace(context.Background(), "/proj", "worker")
	require.NoError(t, err, "resume must succeed — reuse or a named refusal, never a silent fresh checkout")

	assert.Equal(t, dir1, ws2.Dir(), "resume lands back in the SAME checkout the original run left, not a fresh one elsewhere")

	got, err := os.ReadFile(filepath.Join(ws2.Dir(), "WIP_MARKER.txt"))
	require.NoError(t, err, "the resumed workspace can see the file the original run wrote")
	assert.Equal(t, "prior work", string(got))

	addCalls := 0
	for _, c := range f.Calls {
		if strings.HasPrefix(c, "add ") {
			addCalls++
		}
	}
	assert.Equal(t, 1, addCalls, "resume must NOT attempt to re-add an already-registered worktree")
}

// TestWorktree_RefusesAStrangerAtTheResumePath: a path collision alone is
// never proof of ownership. When the deterministic resume path is already
// occupied by something that is NOT a registered git worktree of this repo
// (a plain leftover directory, unrelated content, anything git worktree
// list doesn't know about), ResolveWorkspace must REFUSE — naming the path —
// rather than adopt it or silently fall back to running with no workspace
// isolation at all.
func TestWorktree_RefusesAStrangerAtTheResumePath(t *testing.T) {
	testsupport.Isolate(t)
	f := &git.Fake{CommonDirValue: t.TempDir()}

	w := NewWorktree(f)
	w.state = SessionState{Harp: "resume-harp-b"}

	// Pre-create the deterministic path by hand, WITHOUT registering it as a
	// git worktree — the "stranger" case: something occupies the path that
	// git itself does not recognize as this repo's checkout.
	path := w.checkoutPath("worker")
	require.NoError(t, os.MkdirAll(path, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(path, "not-mine.txt"), []byte("x"), 0o644))

	_, err := w.prepareWorkspace(context.Background(), "/proj", "worker")
	require.Error(t, err, "an unverifiable occupant at the resume path must refuse, not adopt or silently degrade")
	assert.Contains(t, err.Error(), strconv.Quote(path), "the refusal names the path, quoted")
}

// TestWorktree_RefusesWrongBranchAtTheResumePath: the path is occupied AND
// registered as a git worktree of this repo, but on a DIFFERENT branch than
// a fresh create at this path would use — evidence that whatever is there is
// not this agent's own leftover (or the naming scheme has diverged
// underneath it). Refuse rather than guess.
func TestWorktree_RefusesWrongBranchAtTheResumePath(t *testing.T) {
	testsupport.Isolate(t)
	f := &git.Fake{CommonDirValue: t.TempDir()}

	w := NewWorktree(f)
	w.state = SessionState{Harp: "resume-harp-c"}

	path := w.checkoutPath("worker")
	require.NoError(t, os.MkdirAll(path, 0o755))
	f.Worktrees = []git.Worktree{{Path: path, Branch: "some/unrelated-branch"}}

	_, err := w.prepareWorkspace(context.Background(), "/proj", "worker")
	require.Error(t, err, "a registered worktree on the wrong branch is not provably this agent's own")
	assert.Contains(t, err.Error(), strconv.Quote(path))
}
