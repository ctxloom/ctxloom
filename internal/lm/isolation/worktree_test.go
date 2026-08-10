package isolation

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ctxloom/ctxloom/internal/git"
	"github.com/ctxloom/ctxloom/internal/gitignore"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWorktree_PrepareCreatesWorktree: in a repo, PrepareWorkspace adds a detached
// worktree under the OS temp dir (NOT inside the repo) and exposes it as Dir(),
// with per-agent config-home envs.
func TestWorktree_PrepareCreatesWorktree(t *testing.T) {
	common := t.TempDir() // stand-in .git common dir so the exclude write succeeds
	f := &git.Fake{CommonDirValue: common}
	// A real backend is needed so Env() (now driven by credentialSeedSpecs,
	// per-engine-isolation-home plan §6) has a spec to resolve HomeVars from —
	// see TestWorktree_HomeVars_PerBackend for the per-engine var-count guard.
	ws, err := NewWorktree(f, "claude-code").PrepareWorkspace(context.Background(), "/proj", "member-a")
	require.NoError(t, err)
	// Safety net registered BEFORE any assertion below can fail/panic and skip
	// the ws.Cleanup() call at the end of this test (see requireCleanWorkspace).
	requireCleanWorkspace(t, ws)

	assert.True(t, strings.HasPrefix(ws.Dir(), os.TempDir()), "worktree lives under the OS temp dir, not the repo tree")
	assert.NotContains(t, ws.Dir(), "/proj/", "worktree is not created inside the project tree")

	// The Fake records exactly one detached HEAD add.
	require.Len(t, f.Calls, 1)
	assert.True(t, strings.HasPrefix(f.Calls[0], "add "), "one worktree add")
	assert.Contains(t, f.Calls[0], "@HEAD", "checked out to HEAD")

	env := WorkspaceEnv(ws)
	require.NotNil(t, env, "worktree exposes per-agent config-home envs")
	assert.Contains(t, env["CLAUDE_CONFIG_DIR"], "ctxloom-cfg-")

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
	ws, err := NewWorktree(f, "").PrepareWorkspace(context.Background(), "/proj", "member-t")
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
	_, err := NewWorktree(f, "").PrepareWorkspace(context.Background(), "/not-a-repo", "m")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a git repository")
	assert.Empty(t, f.Calls, "no worktree add is attempted on a non-repo")
}

// TestWorktree_DegradesOnAddFailure: a worktree-add failure errors (→ caller
// degrades), never returning a half-built workspace.
func TestWorktree_DegradesOnAddFailure(t *testing.T) {
	f := &git.Fake{AddErr: assertErr("disk full")}
	_, err := NewWorktree(f, "").PrepareWorkspace(context.Background(), "/proj", "m")
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

// TestProvisionConfigHome_OwnerOnly: the per-agent config-home holds engine
// creds/state (CLAUDE_CONFIG_DIR & co.) in the SHARED OS temp dir — it must be
// owner-only (0700) like every MkdirTemp sibling in this package, never
// world-traversable.
func TestProvisionConfigHome_OwnerOnly(t *testing.T) {
	home, _ := Worktree{}.provisionConfigHome("agent-x")
	require.NotEmpty(t, home)
	t.Cleanup(func() { _ = os.RemoveAll(home) })

	info, err := os.Stat(home)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm(), "engine creds/state dir is owner-only")
}

// TestWorktree_UnregisteredBackendRecordsFinding pins that a backend not
// registered in credentialSeedSpecs (e.g. "acp", a real, user-selectable
// registered backend — registry.go:392) used to fall through
// PrepareWorkspace with zero engine-global isolation and no finding at all —
// the run reports "worktree" isolation while the engine's global config/creds
// stay fully shared with the host. It must now be loud.
func TestWorktree_UnregisteredBackendRecordsFinding(t *testing.T) {
	resetStrictness(t)
	common := t.TempDir()
	f := &git.Fake{CommonDirValue: common}
	ws, err := NewWorktree(f, "acp").PrepareWorkspace(context.Background(), "/proj", "agent-a")
	require.NoError(t, err, "PrepareWorkspace itself still succeeds — the fail-loud gate is the CALLER's job")
	t.Cleanup(func() { _ = ws.Cleanup() })

	findings := strictness.All()
	require.Len(t, findings, 1, "an unregistered backend must record exactly one fatal finding")
	assert.Equal(t, strictness.ClassIsolation, findings[0].Class)
	assert.Contains(t, findings[0].Message, `"acp"`)
	assert.Contains(t, findings[0].Message, "not registered in the credential-seed registry")
}

// TestWorktree_MockBackendExemptFromUnregisteredFinding is the negative
// space the fix above must not break: the built-in "mock" test backend is a
// NAMED, independently-verified exemption (a bare echo with no on-disk
// global state — see j002200_isolation.feature's own hermeticity note), not an
// unregistered real backend, so it must never fire the new finding.
func TestWorktree_MockBackendExemptFromUnregisteredFinding(t *testing.T) {
	resetStrictness(t)
	common := t.TempDir()
	f := &git.Fake{CommonDirValue: common}
	ws, err := NewWorktree(f, "mock").PrepareWorkspace(context.Background(), "/proj", "agent-a")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ws.Cleanup() })
	assert.Empty(t, strictness.All(), "the built-in mock backend has no global state to isolate — it must stay exempt")
}

// TestWorktree_ConfigHomeMkdirFailureRecordsFinding pins that a total
// provisionConfigHome MkdirAll failure — which costs ALL engine-global
// isolation for claude/codex/kiro/opencode — used to be a plain clidiag.Warn,
// a QUIETER severity than the LESSER (partial: creds present but unseedable)
// failure in seedCredentials, which is a strictness.Fail. The ordering was
// inverted; both must now be fatal-unless-degraded.
func TestWorktree_ConfigHomeMkdirFailureRecordsFinding(t *testing.T) {
	resetStrictness(t)
	notADir := filepath.Join(t.TempDir(), "not-a-dir")
	require.NoError(t, os.WriteFile(notADir, []byte("x"), 0o644))
	t.Setenv("TMPDIR", notADir)

	w := NewWorktree(nil, "claude-code")
	home, denied := w.provisionConfigHome("agent-a")
	assert.Empty(t, home)
	assert.Nil(t, denied)

	findings := strictness.All()
	require.Len(t, findings, 1, "a total config-home provisioning failure must now be fatal, matching seedNoSource's severity")
	assert.Equal(t, strictness.ClassIsolation, findings[0].Class)
	assert.Contains(t, findings[0].Message, "config-home unavailable")
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

// --- Host+worktree credential seeding integration ---------------------------
//
// These exercise the FULL PrepareWorkspace path (not just hostCredentialSeed in
// auth_test.go), proving the backend threaded through NewWorktree actually
// reaches provisionConfigHome and that the seeded bytes land where Env() points
// CLAUDE_CONFIG_DIR — the assertion that would have caught the original bug
// (provisioning returned no error while shipping an EMPTY config-home).

// TestWorktree_PrepareSeedsClaudeCredentials is the end-to-end PAYLOAD-asserting
// regression test: a "claude-code" worktree, no ANTHROPIC_API_KEY,
// real host creds available (via the hostHomeDir seam) → Env()'s
// CLAUDE_CONFIG_DIR points at a directory that ACTUALLY CONTAINS the seeded
// credential bytes, not an empty dir claude would find "Not logged in" against.
func TestWorktree_PrepareSeedsClaudeCredentials(t *testing.T) {
	resetStrictness(t)
	home := withFakeHome(t)
	t.Setenv("ANTHROPIC_API_KEY", "")
	writeCreds(t, home, true)

	common := t.TempDir()
	f := &git.Fake{CommonDirValue: common}
	ws, err := NewWorktree(f, "claude-code").PrepareWorkspace(context.Background(), "/proj", "member-seed")
	require.NoError(t, err)
	requireCleanWorkspace(t, ws)

	env := WorkspaceEnv(ws)
	require.NotNil(t, env)
	configDir := env["CLAUDE_CONFIG_DIR"]
	require.NotEmpty(t, configDir)

	wantCreds, err := os.ReadFile(filepath.Join(home, ".claude", ".credentials.json"))
	require.NoError(t, err)
	gotCreds, err := os.ReadFile(filepath.Join(configDir, ".credentials.json"))
	require.NoError(t, err, "CLAUDE_CONFIG_DIR must actually contain the seeded credential, not be empty")
	assert.Equal(t, wantCreds, gotCreds, "seeded bytes must be byte-identical to the host source")

	assert.Empty(t, strictness.All(), "a successfully-seeded worktree records no ClassIsolation finding")
	require.NoError(t, ws.Cleanup())
}

// TestWorktree_PrepareFailsLoudWhenNoCredsAndNoKey pins the OTHER half of the
// fix: a "claude-code" worktree with NO ANTHROPIC_API_KEY and NO host creds must
// NOT silently ship an empty (logged-out) config-home — it records a fatal
// ClassIsolation finding the choke owner (isolationGateErr in
// internal/operations) aborts on in strict mode. PrepareWorkspace itself still
// succeeds (the finding is recorded, not returned as an error here) — the abort
// decision belongs to the caller's checkpoint/gate, exactly as the container
// degrade path works.
func TestWorktree_PrepareFailsLoudWhenNoCredsAndNoKey(t *testing.T) {
	resetStrictness(t)
	withFakeHome(t) // empty fake home — nothing to seed
	t.Setenv("ANTHROPIC_API_KEY", "")

	common := t.TempDir()
	f := &git.Fake{CommonDirValue: common}
	ws, err := NewWorktree(f, "claude-code").PrepareWorkspace(context.Background(), "/proj", "member-nokey")
	require.NoError(t, err, "PrepareWorkspace itself still succeeds — the fail-loud gate is the CALLER's job")
	requireCleanWorkspace(t, ws)

	findings := strictness.All()
	require.Len(t, findings, 1, "no creds + no key must record exactly one fatal finding")
	assert.Equal(t, strictness.ClassIsolation, findings[0].Class)
	assert.Contains(t, findings[0].Message, "member-nokey")
	assert.Contains(t, findings[0].Message, "logged out")
	assert.NotEmpty(t, findings[0].FixIt)

	// And the config-home is NOT silently populated with a half-seeded state —
	// no claude subdirectory at all.
	env := WorkspaceEnv(ws)
	requireNothingSeeded(t, env["CLAUDE_CONFIG_DIR"])

	require.NoError(t, ws.Cleanup())
}

// TestWorktree_PrepareSkipsSeedingWithApiKeyNoFailLoud: ANTHROPIC_API_KEY set
// (even with no host creds at all) rides the env exactly as the container path
// prefers env passthrough — no seed attempt, and critically NO fail-loud finding
// (this is the "unaffected by construction" guarantee, §5.4 of the plan).
func TestWorktree_PrepareSkipsSeedingWithApiKeyNoFailLoud(t *testing.T) {
	resetStrictness(t)
	withFakeHome(t) // no host creds
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")

	common := t.TempDir()
	f := &git.Fake{CommonDirValue: common}
	ws, err := NewWorktree(f, "claude-code").PrepareWorkspace(context.Background(), "/proj", "member-key")
	require.NoError(t, err)
	requireCleanWorkspace(t, ws)

	assert.Empty(t, strictness.All(), "ANTHROPIC_API_KEY covers auth — no finding, seeding skipped")
	env := WorkspaceEnv(ws)
	requireNothingSeeded(t, env["CLAUDE_CONFIG_DIR"])

	require.NoError(t, ws.Cleanup())
}

// TestWorktree_NoBackendSkipsSeedingAndFailLoud: a Worktree built with NO
// backend (the pre-fix construction, or a caller with no backend context, e.g.
// the container-worktree base) makes NO seeding attempt and records NO finding
// — config-only isolation exactly as before this fix, never a NEW fail-loud
// surprise for a caller that never opted into credential seeding.
func TestWorktree_NoBackendSkipsSeedingAndFailLoud(t *testing.T) {
	resetStrictness(t)
	withFakeHome(t)
	t.Setenv("ANTHROPIC_API_KEY", "")

	common := t.TempDir()
	f := &git.Fake{CommonDirValue: common}
	ws, err := NewWorktree(f, "").PrepareWorkspace(context.Background(), "/proj", "member-nobackend")
	require.NoError(t, err)
	requireCleanWorkspace(t, ws)

	assert.Empty(t, strictness.All(), "no backend context → no seed attempt → no finding")
	require.NoError(t, ws.Cleanup())
}

// TestWorktree_AsymmetryWithNone guards the None-vs-Worktree asymmetry
// directly (plan §6): None sets NO CLAUDE_CONFIG_DIR at all (the engine reads
// the real ~/.claude and just authenticates), while a seeded Worktree sets one
// whose target ACTUALLY CONTAINS credentials — proving the fix closes the gap
// between the two without regressing None's already-working behavior.
func TestWorktree_AsymmetryWithNone(t *testing.T) {
	resetStrictness(t)
	home := withFakeHome(t)
	t.Setenv("ANTHROPIC_API_KEY", "")
	writeCreds(t, home, true)

	noneWS, err := None{}.PrepareWorkspace(context.Background(), "/proj", "member-none")
	require.NoError(t, err)
	noneEnv := WorkspaceEnv(noneWS)
	assert.Nil(t, noneEnv, "None sets no config-home env at all — it uses the real ~/.claude directly")

	common := t.TempDir()
	f := &git.Fake{CommonDirValue: common}
	ws, err := NewWorktree(f, "claude-code").PrepareWorkspace(context.Background(), "/proj", "member-asym")
	require.NoError(t, err)
	requireCleanWorkspace(t, ws)

	env := WorkspaceEnv(ws)
	require.NotEmpty(t, env["CLAUDE_CONFIG_DIR"], "worktree DOES set CLAUDE_CONFIG_DIR")
	assert.FileExists(t, filepath.Join(env["CLAUDE_CONFIG_DIR"], ".credentials.json"),
		"...and unlike before the fix, that directory is NOT empty — it carries the seeded creds")

	require.NoError(t, ws.Cleanup())
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
// harp homes BOTH per-agent scratch dirs (the checkout and the config-home)
// under the session's ephemeral/ dir — the §6d layout: regenerable state in
// one inspectable per-session place — instead of the OS temp dir. The no-harp
// case keeps the temp dir (pinned by TestWorktree_PrepareCreatesWorktree).
func TestWorktree_ScratchRelocatesIntoHarpEphemeral(t *testing.T) {
	home := testsupport.Isolate(t)
	f := &git.Fake{CommonDirValue: t.TempDir()}

	w := NewWorktree(f, "claude-code")
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

	// spawner-env: the toolchain scratch dir (TMPDIR/GOTMPDIR) is the THIRD
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
	ws, err := NewWorktree(f, "").PrepareWorkspace(context.Background(), "/proj", "member-scratch")
	require.NoError(t, err)
	requireCleanWorkspace(t, ws)

	env := WorkspaceEnv(ws)
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
		ws  Workspace
		env map[string]string
		err error
	}
	results := make([]result, 2)
	var wg sync.WaitGroup
	for i, agentID := range []string{"agent-alpha", "agent-beta"} {
		wg.Add(1)
		go func(i int, agentID string) {
			defer wg.Done()
			ws, err := NewWorktree(f, "").PrepareWorkspace(context.Background(), "/proj", agentID)
			results[i] = result{ws: ws, env: WorkspaceEnv(ws), err: err}
		}(i, agentID)
	}
	wg.Wait()
	for _, r := range results {
		require.NoError(t, r.err)
		requireCleanWorkspace(t, r.ws)
		t.Cleanup(func(ws Workspace) func() { return func() { _ = ws.Cleanup() } }(r.ws))
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
	ws, err := NewWorktree(f, "").PrepareWorkspace(context.Background(), "/proj", "member-teardown")
	require.NoError(t, err)
	requireCleanWorkspace(t, ws)

	scratch := WorkspaceEnv(ws)["TMPDIR"]
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
	ws, err := NewWorktree(f, "").PrepareWorkspace(context.Background(), "/proj", "reviewer-3")
	require.NoError(t, err)
	requireCleanWorkspace(t, ws)

	env := WorkspaceEnv(ws)
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

// TestWorktree_HomeVars_PerBackend is the "descriptor table guard" the
// per-engine-isolation-home plan §9 asks for: each backend's Env() var-set
// size must match the cartography table — claude:1, codex:1, kiro:2, and ""
// (no backend context):0 config-home vars (the pre-fix, config-only-isolation
// default — see
// TestWorktree_NoBackendSkipsSeedingAndFailLoud) — PLUS the 6 toolchain vars
// (spawner-env: TMPDIR, GOTMPDIR, GIT_AUTHOR_{NAME,EMAIL},
// GIT_COMMITTER_{NAME,EMAIL}) every backend gets UNCONDITIONALLY, including
// "" (no backend context still gets a scratch dir; the git identity is
// omitted for "" only because the test cases below pass a real agentID —
// see gitIdentity's empty-agentID no-op for when it wouldn't).
func TestWorktree_HomeVars_PerBackend(t *testing.T) {
	resetStrictness(t)
	withFakeHome(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")  // skip claude's seed attempt — only var COUNT matters here
	t.Setenv("OPENAI_API_KEY", "sk-test")     // skip codex's seed attempt
	t.Setenv("KIRO_API_KEY", "sk-test")       // grant kiro's gated XDG_DATA_HOME
	t.Setenv("OPENROUTER_API_KEY", "sk-test") // skip opencode's seed attempt

	const toolchainVars = 6 // TMPDIR, GOTMPDIR, GIT_AUTHOR_{NAME,EMAIL}, GIT_COMMITTER_{NAME,EMAIL}
	cases := []struct {
		backend string
		want    int
	}{
		{"claude-code", 1 + toolchainVars},
		{"codex", 1 + toolchainVars},
		{"kiro", 2 + toolchainVars},
		{"opencode", 2 + toolchainVars},
		{"", 0 + toolchainVars},
	}
	for _, c := range cases {
		t.Run(c.backend, func(t *testing.T) {
			common := t.TempDir()
			f := &git.Fake{CommonDirValue: common}
			ws, err := NewWorktree(f, c.backend).PrepareWorkspace(context.Background(), "/proj", "member-"+c.backend)
			require.NoError(t, err)
			t.Cleanup(func() { _ = ws.Cleanup() })

			env := WorkspaceEnv(ws)
			assert.Len(t, env, c.want, "backend %q home-var count", c.backend)
		})
	}
}

// TestWorktree_KiroTwoAgentsDisjointXDG is the headline PAYLOAD test:
// two concurrent kiro worktree agents (KIRO_API_KEY set, so XDG isolation is
// granted) get DISJOINT XDG_DATA_HOME roots — the assertion that would have
// caught the original bug (both "isolated" agents sharing one global
// $XDG_DATA_HOME/kiro-cli/data.sqlite3, silently reading each other's
// conversations). Simulates a marker write into agent A's would-be sqlite
// path and asserts agent B's XDG root does not contain it.
func TestWorktree_KiroTwoAgentsDisjointXDG(t *testing.T) {
	resetStrictness(t)
	t.Setenv("KIRO_API_KEY", "sk-test")
	common := t.TempDir()
	f := &git.Fake{CommonDirValue: common}

	wsA, err := NewWorktree(f, "kiro").PrepareWorkspace(context.Background(), "/proj", "agent-a")
	require.NoError(t, err)
	t.Cleanup(func() { _ = wsA.Cleanup() })
	wsB, err := NewWorktree(f, "kiro").PrepareWorkspace(context.Background(), "/proj", "agent-b")
	require.NoError(t, err)
	t.Cleanup(func() { _ = wsB.Cleanup() })

	envA, envB := WorkspaceEnv(wsA), WorkspaceEnv(wsB)
	require.NotEmpty(t, envA["XDG_DATA_HOME"])
	require.NotEmpty(t, envB["XDG_DATA_HOME"])
	assert.NotEqual(t, envA["XDG_DATA_HOME"], envB["XDG_DATA_HOME"], "two agents must resolve to DISJOINT XDG roots")

	// Payload: a marker "conversation" written under A's kiro-cli data dir
	// must not be visible under B's.
	markerRel := filepath.Join("kiro-cli", "data.sqlite3")
	markerPath := filepath.Join(envA["XDG_DATA_HOME"], markerRel)
	require.NoError(t, os.MkdirAll(filepath.Dir(markerPath), 0o700))
	require.NoError(t, os.WriteFile(markerPath, []byte("agent-a-conversation"), 0o600))

	assert.NoFileExists(t, filepath.Join(envB["XDG_DATA_HOME"], markerRel),
		"agent B's XDG root must not contain agent A's conversation store")
	assert.Empty(t, strictness.All(), "KIRO_API_KEY present — both agents isolate cleanly, no finding")
}

// TestWorktree_KiroFailLoudWithoutApiKey pins the fail-loud floor: no
// KIRO_API_KEY means isolating XDG_DATA_HOME would silently strand the agent
// logged out of its (global, unrelocatable) credential store, so it is
// OMITTED from Env() (falling back to the shared global store) and a
// ClassIsolation finding is recorded — the previously-SILENT non-isolation
// becomes a loud, degradable error instead. KIRO_HOME (sessions
// only, no creds) still isolates unconditionally.
func TestWorktree_KiroFailLoudWithoutApiKey(t *testing.T) {
	resetStrictness(t)
	t.Setenv("KIRO_API_KEY", "")
	common := t.TempDir()
	f := &git.Fake{CommonDirValue: common}

	ws, err := NewWorktree(f, "kiro").PrepareWorkspace(context.Background(), "/proj", "member-nokey")
	require.NoError(t, err, "PrepareWorkspace itself still succeeds — the fail-loud gate is the CALLER's job")
	t.Cleanup(func() { _ = ws.Cleanup() })

	env := WorkspaceEnv(ws)
	assert.NotEmpty(t, env["KIRO_HOME"], "KIRO_HOME isolates unconditionally (no creds live there)")
	assert.Empty(t, env["XDG_DATA_HOME"], "XDG_DATA_HOME is DENIED — isolating it would silently log the agent out")

	findings := strictness.All()
	require.Len(t, findings, 1)
	assert.Equal(t, strictness.ClassIsolation, findings[0].Class)
	assert.Contains(t, findings[0].Message, "XDG_DATA_HOME")
	assert.Contains(t, findings[0].Message, "member-nokey")
	assert.NotEmpty(t, findings[0].FixIt)
}

// TestWorktree_KiroIsolatesXDGWithApiKey is the positive half of the gate:
// KIRO_API_KEY set → XDG_DATA_HOME isolates (present in Env()) and no
// ClassIsolation finding is recorded.
func TestWorktree_KiroIsolatesXDGWithApiKey(t *testing.T) {
	resetStrictness(t)
	t.Setenv("KIRO_API_KEY", "sk-test")
	common := t.TempDir()
	f := &git.Fake{CommonDirValue: common}

	ws, err := NewWorktree(f, "kiro").PrepareWorkspace(context.Background(), "/proj", "member-key")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ws.Cleanup() })

	env := WorkspaceEnv(ws)
	assert.NotEmpty(t, env["XDG_DATA_HOME"], "KIRO_API_KEY present — XDG_DATA_HOME isolates")
	assert.Empty(t, strictness.All(), "no finding when the gate is satisfied")
}

// --- opencode host+worktree credential seeding ------------------------------

// TestWorktree_PrepareSeedsOpencodeCredentials is the end-to-end
// PAYLOAD-asserting regression test: a "opencode" worktree, no
// OPENROUTER_API_KEY, real host auth.json available (via the hostHomeDir
// seam) → Env()'s XDG_DATA_HOME points at a directory that ACTUALLY
// CONTAINS the seeded auth.json at the opencode/ subpath opencode itself
// resolves — not an empty dir `opencode auth list` would report "0
// credentials" against, exactly as the task's live experiment showed.
func TestWorktree_PrepareSeedsOpencodeCredentials(t *testing.T) {
	resetStrictness(t)
	home := withFakeHome(t)
	t.Setenv("OPENROUTER_API_KEY", "")
	writeOpencodeAuth(t, home, false)

	common := t.TempDir()
	f := &git.Fake{CommonDirValue: common}
	ws, err := NewWorktree(f, "opencode").PrepareWorkspace(context.Background(), "/proj", "member-oc-seed")
	require.NoError(t, err)
	requireCleanWorkspace(t, ws)

	env := WorkspaceEnv(ws)
	require.NotNil(t, env)
	require.NotEmpty(t, env["XDG_CONFIG_HOME"], "opencode's config lever isolates too")
	xdgData := env["XDG_DATA_HOME"]
	require.NotEmpty(t, xdgData)

	wantAuth, err := os.ReadFile(filepath.Join(home, ".local", "share", "opencode", "auth.json"))
	require.NoError(t, err)
	gotAuth, err := os.ReadFile(filepath.Join(xdgData, "opencode", "auth.json"))
	require.NoError(t, err, "XDG_DATA_HOME must actually contain the seeded auth.json under opencode/, not be empty")
	assert.Equal(t, wantAuth, gotAuth, "seeded bytes must be byte-identical to the host source")

	assert.Empty(t, strictness.All(), "a successfully-seeded opencode worktree records no ClassIsolation finding")
	require.NoError(t, ws.Cleanup())
}

// TestWorktree_PrepareFailsLoudForOpencodeWhenNoCredsAndNoKey pins the
// closed silent no-op: before this fix, an "opencode" worktree with no
// OPENROUTER_API_KEY and no host auth.json made NO finding at all
// (credentialSeedSpecs had no "opencode" entry, so seedCredentials
// short-circuited silently) — strictly worse than kiro's loud
// "nothing seedable" handling. Now it records the same fatal ClassIsolation
// finding claude/codex/kiro already get.
func TestWorktree_PrepareFailsLoudForOpencodeWhenNoCredsAndNoKey(t *testing.T) {
	resetStrictness(t)
	withFakeHome(t) // empty fake home — nothing to seed
	t.Setenv("OPENROUTER_API_KEY", "")

	common := t.TempDir()
	f := &git.Fake{CommonDirValue: common}
	ws, err := NewWorktree(f, "opencode").PrepareWorkspace(context.Background(), "/proj", "member-oc-nokey")
	require.NoError(t, err, "PrepareWorkspace itself still succeeds — the fail-loud gate is the CALLER's job")
	requireCleanWorkspace(t, ws)

	findings := strictness.All()
	require.Len(t, findings, 1, "no creds + no key must record exactly one fatal finding (previously: silently NONE)")
	assert.Equal(t, strictness.ClassIsolation, findings[0].Class)
	assert.Contains(t, findings[0].Message, "member-oc-nokey")
	assert.Contains(t, findings[0].Message, "logged out")
	assert.NotEmpty(t, findings[0].FixIt)

	env := WorkspaceEnv(ws)
	assert.NoDirExists(t, filepath.Join(env["XDG_DATA_HOME"], "opencode"), "nothing is seeded when there is nothing to seed")

	require.NoError(t, ws.Cleanup())
}

// TestWorktree_PrepareSkipsOpencodeSeedingWithOpenrouterKeyNoFailLoud:
// OPENROUTER_API_KEY set (even with no host auth.json at all) rides the env
// exactly as the container path prefers env passthrough — no seed attempt,
// and critically NO fail-loud finding.
func TestWorktree_PrepareSkipsOpencodeSeedingWithOpenrouterKeyNoFailLoud(t *testing.T) {
	resetStrictness(t)
	withFakeHome(t) // no host auth.json
	t.Setenv("OPENROUTER_API_KEY", "sk-or-test")

	common := t.TempDir()
	f := &git.Fake{CommonDirValue: common}
	ws, err := NewWorktree(f, "opencode").PrepareWorkspace(context.Background(), "/proj", "member-oc-key")
	require.NoError(t, err)
	requireCleanWorkspace(t, ws)

	assert.Empty(t, strictness.All(), "OPENROUTER_API_KEY covers auth — no finding, seeding skipped")
	env := WorkspaceEnv(ws)
	assert.NoDirExists(t, filepath.Join(env["XDG_DATA_HOME"], "opencode"), "no seed dir is created when the key rides the env")

	require.NoError(t, ws.Cleanup())
}

// TestWorktree_HomeVarDirsExist pins that every directory Env() names
// for a backend's HomeVars must EXIST, owner-only, by the time the workspace is
// handed to a caller. Only hostCredentialSeed ever created a config-home
// subdirectory, it created spec.destSubdir alone, and only on the path where
// there was something to seed — so an engine authenticating from the
// environment (ANTHROPIC_API_KEY below) or one with no seedable files at all
// (kiro) was handed a scoped var naming a directory nothing had created. The
// MODE is half the assertion: these hold engine config/state and must be 0700
// like every sibling scratch dir, not whatever umask the engine would have
// mkdir'd them with itself.
func TestWorktree_HomeVarDirsExist(t *testing.T) {
	resetStrictness(t)
	withFakeHome(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk-test") // claude authenticates from the env: nothing to seed
	t.Setenv("KIRO_API_KEY", "sk-test")      // grant kiro's gated XDG_DATA_HOME

	for _, backend := range []string{"claude-code", "kiro"} {
		t.Run(backend, func(t *testing.T) {
			common := t.TempDir()
			f := &git.Fake{CommonDirValue: common}
			ws, err := NewWorktree(f, backend).PrepareWorkspace(context.Background(), "/proj", "member-"+backend)
			require.NoError(t, err)
			requireCleanWorkspace(t, ws)
			t.Cleanup(func() { _ = ws.Cleanup() })

			spec := credentialSeedSpecs[backend]
			require.NotEmpty(t, spec.HomeVars, "%q must have HomeVars for this pin to mean anything", backend)
			env := WorkspaceEnv(ws)
			for _, hv := range spec.HomeVars {
				dir := env[hv.EnvVar]
				require.NotEmpty(t, dir, "%s must be exported", hv.EnvVar)
				info, statErr := os.Stat(dir)
				require.NoError(t, statErr, "%s names a directory that must exist on disk", hv.EnvVar)
				assert.True(t, info.IsDir(), "%s names a directory", hv.EnvVar)
				assert.Equal(t, os.FileMode(0o700), info.Mode().Perm(),
					"%s holds engine config/state and must be owner-only", hv.EnvVar)
			}
		})
	}
}

// requireNothingSeeded asserts a per-agent config-home subdirectory carries no
// seeded credential material. It states the invariant a NoDirExists check used
// only to approximate: the directory Env() names is created unconditionally, so
// "no seed happened" is EMPTINESS of that directory, not its absence.
func requireNothingSeeded(t *testing.T, dir string) {
	t.Helper()
	require.NotEmpty(t, dir, "the scoped config-home var must be exported")
	entries, err := os.ReadDir(dir)
	require.NoError(t, err, "the config-home subdirectory exists even when nothing was seeded")
	assert.Empty(t, entries, "nothing is seeded when there is nothing to seed")
}

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
	w := NewWorktree(panicAfterAddGit{Fake: f}, "")
	assert.Panics(t, func() {
		_, _ = w.PrepareWorkspace(context.Background(), "/proj", "member-panic")
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
		w := NewWorktree(f, "")
		w.state = SessionState{Harp: badHarp}

		done := captureStderr(t)
		ws, err := w.PrepareWorkspace(context.Background(), "/proj", "member-badharp")
		stderr := done()
		require.NoError(t, err)
		requireCleanWorkspace(t, ws)
		t.Cleanup(func() { _ = ws.Cleanup() })

		assert.Contains(t, stderr, badHarp, "the warning names the harp it refused to use")
		assert.True(t, strings.HasPrefix(ws.Dir(), os.TempDir()+string(os.PathSeparator)),
			"the fallback itself is unchanged: scratch %q lands in the OS temp dir", ws.Dir())
	})

	t.Run("absent harp stays silent", func(t *testing.T) {
		f := &git.Fake{CommonDirValue: t.TempDir()}
		w := NewWorktree(f, "")

		done := captureStderr(t)
		ws, err := w.PrepareWorkspace(context.Background(), "/proj", "member-noharp")
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
	NewWorktree(f, "").excludeConfigFromMerge(context.Background(), "/proj")

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
// `if w.dir == "" { return nil }` also short-circuits removal of configHome
// and scratchDir, which are independent resources.
//
// The guard does gate both — that half is true. The leak it implies is
// unreachable by construction, and this pins the two properties that make it
// so: (1) the only production construction of a worktreeWorkspace sets dir
// FIRST and non-empty, before any scratch home exists to strand, and (2) one
// Cleanup clears every field and removes every resource, so a second call has
// nothing left to reach. Either property breaking — a construction that leaves
// dir empty, or a Cleanup arm that stops clearing its field — turns the shared
// guard into the leak the row describes, and turns this red.
func TestWorktreeCleanup_NoResourceStrandedByTheDirGuard(t *testing.T) {
	withFakeHome(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")

	for _, backend := range []string{"claude-code"} {
		t.Run(backend, func(t *testing.T) {
			resetStrictness(t)
			f := &git.Fake{CommonDirValue: t.TempDir()}
			ws, err := NewWorktree(f, backend).PrepareWorkspace(context.Background(), "/proj", "member-"+backend)
			require.NoError(t, err)
			requireCleanWorkspace(t, ws)
			concrete, ok := ws.(*worktreeWorkspace)
			require.True(t, ok)

			require.NotEmpty(t, concrete.dir,
				"the guard's own field is set before any scratch home exists to be stranded by it")
			var live []string
			for _, r := range []string{concrete.configHome, concrete.scratchDir} {
				if r != "" {
					live = append(live, r)
				}
			}
			require.NotEmpty(t, live, "%q must provision a scratch home for this pin to mean anything", backend)

			require.NoError(t, ws.Cleanup())

			assert.Empty(t, concrete.dir, "dir is cleared")
			assert.Empty(t, concrete.configHome, "configHome is cleared in the SAME call, never left for a second one")
			assert.Empty(t, concrete.scratchDir, "scratchDir is cleared in the SAME call")
			for _, r := range live {
				assert.NoDirExists(t, r, "Cleanup removed %q from disk", r)
			}
		})
	}
}
