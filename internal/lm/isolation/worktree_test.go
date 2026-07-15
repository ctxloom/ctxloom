package isolation

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/git"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWorktree_Axes pins the policy identity: name "worktree", approvals PROMPT
// (config isolation is NOT a security boundary — only container bypasses).
func TestWorktree_Axes(t *testing.T) {
	p := NewWorktree(&git.Fake{}, "")
	assert.Equal(t, "worktree", p.Name())
	assert.Equal(t, ApprovalsPrompt, p.Approvals(), "worktree keeps the engine's in-tool approval prompt")
}

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
	home, _ := Worktree{}.provisionConfigHome("agent-x")
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

// --- Host+worktree credential seeding integration (grave-prize) ------------
//
// These exercise the FULL PrepareWorkspace path (not just hostCredentialSeed in
// auth_test.go), proving the backend threaded through NewWorktree actually
// reaches provisionConfigHome and that the seeded bytes land where Env() points
// CLAUDE_CONFIG_DIR — the assertion that would have caught the original bug
// (provisioning returned no error while shipping an EMPTY config-home).

// TestWorktree_PrepareSeedsClaudeCredentials is the end-to-end PAYLOAD-asserting
// regression test for grave-prize: a "claude-code" worktree, no ANTHROPIC_API_KEY,
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
	assert.NoDirExists(t, env["CLAUDE_CONFIG_DIR"], "nothing is seeded when there is nothing to seed")

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
	assert.NoDirExists(t, env["CLAUDE_CONFIG_DIR"], "no seed dir is created when the key rides the env")

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
}

// --- Per-engine isolation-home (legal-hula / white-dawn / balmy-comic) -----

// TestWorktree_HomeVars_PerBackend is the "descriptor table guard" the
// per-engine-isolation-home plan §9 asks for: each backend's Env() var-set
// size must match the §1 cartography table — claude:1, codex:1, kiro:2,
// antigravity:0 (no lever at all), and "" (no backend context):0 (the
// pre-fix, config-only-isolation default — see TestWorktree_NoBackendSkipsSeedingAndFailLoud).
func TestWorktree_HomeVars_PerBackend(t *testing.T) {
	resetStrictness(t)
	withFakeHome(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk-test") // skip claude's seed attempt — only var COUNT matters here
	t.Setenv("OPENAI_API_KEY", "sk-test")    // skip codex's seed attempt
	t.Setenv("KIRO_API_KEY", "sk-test")      // grant kiro's gated XDG_DATA_HOME

	cases := []struct {
		backend string
		want    int
	}{
		{"claude-code", 1},
		{"codex", 1},
		{"kiro", 2},
		{"antigravity", 0},
		{"", 0},
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

// TestWorktree_KiroTwoAgentsDisjointXDG is legal-hula's headline PAYLOAD test:
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

// TestWorktree_KiroFailLoudWithoutApiKey pins legal-hula's fail-loud floor: no
// KIRO_API_KEY means isolating XDG_DATA_HOME would silently strand the agent
// logged out of its (global, unrelocatable) credential store, so it is
// OMITTED from Env() (falling back to the shared global store) and a
// ClassIsolation finding is recorded — the previously-SILENT non-isolation
// (legal-hula) becomes a loud, degradable error instead. KIRO_HOME (sessions
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
