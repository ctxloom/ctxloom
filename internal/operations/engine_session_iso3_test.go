package operations

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/isolation"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// ISO3 (v0.7.0 ACP Hub plan, follow-on to D-ISO/ISO2): the RUNTIME axis
// (host process vs container) and the agent-resolution fail-soft were
// previously INVISIBLE to the editor — only the WORKSPACE axis (a real
// worktree) ever announced itself. This file proves buildSessionAnnouncement
// composes the right text for every posture, and that OpenEngineSession
// actually delivers it as the first event on the client-facing channel —
// not just "no error", the PAYLOAD.
//
// The incident this exists to stop being invisible: a Nori config said
// `--agent default`, no such agent existed in that project, and the session
// silently opened unbound (host, no engine binding, no permissions) with the
// only trace a single stderr line editors bury in a log. An agent then burned
// 40 minutes concluding ctxloom itself was broken before anyone found the
// warning. `coder` in THIS project is `engine: claude-sonnet, runtime:
// container`; typo it under the old behavior and you silently get a HOST
// session while believing you are isolated — the same class of bug as a
// `Host{}.Available()` silently reporting available.

// --- buildSessionAnnouncement: pure unit tests, one per posture ------------

func TestBuildSessionAnnouncement_HostAndCwd_AgentResolved(t *testing.T) {
	got := buildSessionAnnouncement(&config.Config{}, "claude-sonnet", "coder", "coder", "claude-sonnet", "", nil)
	want := `ctxloom: agent "coder" (engine claude-sonnet) — HOST process (no container), this project's live working directory (no worktree). Not isolated on either axis.`
	assert.Equal(t, want, got)
}

func TestBuildSessionAnnouncement_HostAndCwd_NoAgentBound(t *testing.T) {
	got := buildSessionAnnouncement(&config.Config{}, "claude-sonnet", "", "", "claude-sonnet", "", nil)
	want := `ctxloom: no agent bound (profile flow) — HOST process (no container), this project's live working directory (no worktree). Not isolated on either axis.`
	assert.Equal(t, want, got)
}

func TestBuildSessionAnnouncement_AgentNotFound(t *testing.T) {
	got := buildSessionAnnouncement(&config.Config{}, "claude-sonnet", "default", "", "", "", nil)
	want := `ctxloom: WARNING — agent "default" was requested but NOT FOUND; this session fell back to the plain profile flow instead of refusing to open. NONE of that agent's engine override, composed profiles, permissions posture, or runtime isolation apply — it is running on the HOST, unisolated, against this project's live working directory. Check the agent name (see ` + "`ctxloom acp agents`" + `) and reconnect.`
	assert.Equal(t, want, got)
}

func TestBuildSessionAnnouncement_WorktreeOnly(t *testing.T) {
	aw := &acpWorkspace{dir: "/tmp/fake-worktree", announce: "irrelevant marker: only emptiness is checked"}
	got := buildSessionAnnouncement(&config.Config{}, "mock", "reviewer", "reviewer", "mock", "", aw)
	want := `ctxloom: agent "reviewer" (engine mock) — RUNTIME: a HOST process (no container). WORKSPACE isolated to its own git worktree — /tmp/fake-worktree. Your editor's view of this project is NOT touched directly: the engine's edits land in that worktree, and this window stays blind to it unless you open the path yourself. Results return through ctxloom's normal delegated-child assemble/merge flow.`
	assert.Equal(t, want, got)
}

func TestBuildSessionAnnouncement_ContainerOnly_ConfiguredImage(t *testing.T) {
	cfg := &config.Config{IsolationImages: map[string]string{"claude-sonnet": "myimg:latest"}}
	got := buildSessionAnnouncement(cfg, "claude-sonnet", "coder", "coder", "claude-sonnet", agent.RuntimeContainer, nil)
	want := `ctxloom: agent "coder" (engine claude-sonnet) — RUNTIME isolated inside a container (image myimg:latest) — this engine process is NOT running directly on your host. WORKSPACE: this project's live working directory (no worktree) — the container mounts it at the same path, so edits still land where your editor can see them.`
	assert.Equal(t, want, got)
}

func TestBuildSessionAnnouncement_ContainerOnly_NoConfiguredImage(t *testing.T) {
	got := buildSessionAnnouncement(&config.Config{}, "claude-sonnet", "coder", "coder", "claude-sonnet", agent.RuntimeContainer, nil)
	want := `ctxloom: agent "coder" (engine claude-sonnet) — RUNTIME isolated inside a container (an auto-selected image) — this engine process is NOT running directly on your host. WORKSPACE: this project's live working directory (no worktree) — the container mounts it at the same path, so edits still land where your editor can see them.`
	assert.Equal(t, want, got)
}

func TestBuildSessionAnnouncement_ContainerAndWorktree(t *testing.T) {
	cfg := &config.Config{IsolationImages: map[string]string{"claude-sonnet": "myimg:latest"}}
	aw := &acpWorkspace{dir: "/tmp/fake-worktree", announce: "irrelevant marker: only emptiness is checked"}
	got := buildSessionAnnouncement(cfg, "claude-sonnet", "coder", "coder", "claude-sonnet", agent.RuntimeContainer, aw)
	want := `ctxloom: agent "coder" (engine claude-sonnet) — RUNTIME isolated inside a container (image myimg:latest) — this engine process is NOT running directly on your host. WORKSPACE isolated to its own git worktree — /tmp/fake-worktree. Your editor's view of this project is NOT touched directly: the engine's edits land in that worktree, and this window stays blind to it unless you open the path yourself. Results return through ctxloom's normal delegated-child assemble/merge flow.`
	assert.Equal(t, want, got)
}

// TestBuildSessionAnnouncement_DegradedWorktreeNeverClaimsIsolation proves the
// aw.announce == "" case (worktree requested but silently degraded to none,
// see prepareACPWorkspace's TestPrepareACPWorkspace_NonGitDegradesSilently)
// falls through to the unisolated one-liner, never the worktree paragraph —
// a workspace that never actually isolated must never be announced as if it
// did.
func TestBuildSessionAnnouncement_DegradedWorktreeNeverClaimsIsolation(t *testing.T) {
	aw := &acpWorkspace{dir: "/some/live/dir", announce: ""}
	got := buildSessionAnnouncement(&config.Config{}, "mock", "reviewer", "reviewer", "mock", "", aw)
	want := `ctxloom: agent "reviewer" (engine mock) — HOST process (no container), this project's live working directory (no worktree). Not isolated on either axis.`
	assert.Equal(t, want, got)
}

// --- end-to-end payload proofs: the announcement actually reaches the -----
// --- client-facing Events channel as the FIRST event, for real postures ---

// TestOpenEngineSession_UnisolatedAnnouncesOnce proves the ALWAYS decision:
// even the fully unisolated, no-agent, plain `ctxloom acp` entry gets the
// concise one-line posture statement as the first event — it no longer skips
// straight to the engine's own first event the way D-ISO's worktree-only
// mechanism used to.
func TestOpenEngineSession_UnisolatedAnnouncesOnce(t *testing.T) {
	resetStrictness(t)
	t.Setenv("HOME", t.TempDir())
	repo := writeACPTestProject(t, "worktree")

	prevPrep := prepareIsolation
	prepareIsolation = func(context.Context, isolation.Axes, string, isolation.ImageConfig, string, string, isolation.SessionState) (isolation.Policy, isolation.Workspace) {
		t.Fatal("the plain ctxloom acp entry (no --agent) must never invoke isolation.Prepare")
		return nil, nil
	}
	t.Cleanup(func() { prepareIsolation = prevPrep })

	client := &fakeACPEngineClient{}
	stubACPEngineClient(t, client)

	chat, err := OpenEngineSession(context.Background(), OpenRequest{Cwd: repo}, fakeEngineSessionCoord{},
		"", "", "mock", "")
	require.NoError(t, err)
	require.NotNil(t, chat)

	first := <-chat.Events
	require.NotNil(t, first.Entry, "the FIRST event must be the always-on ISO3 announcement")
	assert.Equal(t, agent.EntryTypeSystem, first.Entry.Type)
	assert.Equal(t, `ctxloom: no agent bound (profile flow) — HOST process (no container), this project's live working directory (no worktree). Not isolated on either axis.`, first.Entry.Content)

	second := <-chat.Events
	require.NotNil(t, second.Entry)
	assert.Equal(t, "engine-marker", second.Entry.Content, "the engine's own event still follows, unaltered")

	chat.Close()
}

// TestOpenEngineSession_UnknownAgentAnnouncementIsLoud is the headline ISO3
// regression proof: the exact fail-soft that bit a real user today —
// `--agent <typo>` silently degrading to an unbound session — must now be
// IMPOSSIBLE to miss in the chat transcript itself, not just a stderr line.
func TestOpenEngineSession_UnknownAgentAnnouncementIsLoud(t *testing.T) {
	resetStrictness(t)
	t.Setenv("HOME", t.TempDir())
	repo := writeACPTestProject(t, "worktree")

	client := &fakeACPEngineClient{}
	stubACPEngineClient(t, client)

	chat, err := OpenEngineSession(context.Background(), OpenRequest{Cwd: repo}, fakeEngineSessionCoord{},
		"", "no-such-agent", "mock", "")
	require.NoError(t, err)
	require.NotNil(t, chat)

	first := <-chat.Events
	require.NotNil(t, first.Entry, "the FIRST event must be the agent-not-found announcement")
	assert.Equal(t, agent.EntryTypeSystem, first.Entry.Type)
	assert.Contains(t, first.Entry.Content, `agent "no-such-agent" was requested but NOT FOUND`)
	assert.Contains(t, first.Entry.Content, "running on the HOST, unisolated")

	chat.Close()
}

// TestOpenEngineSession_ContainerRuntimeAnnounces proves the RUNTIME axis
// (previously silent) now announces too: an agent declaring `runtime:
// container` gets a first-turn message naming the container posture, even
// though its workspace stays the shared project dir (no worktree requested).
func TestOpenEngineSession_ContainerRuntimeAnnounces(t *testing.T) {
	resetStrictness(t)
	t.Setenv("HOME", t.TempDir())
	repo := initTestRepo(t)
	appDir := filepath.Join(repo, ".ctxloom")
	require.NoError(t, os.MkdirAll(appDir, 0o755))
	body := "version: 5\n" +
		"agents:\n  builder:\n    engine: mock\n    runtime: container\n"
	require.NoError(t, os.WriteFile(filepath.Join(appDir, "config.yaml"), []byte(body), 0o644))

	client := &fakeACPEngineClient{}
	stubACPEngineClient(t, client)

	chat, err := OpenEngineSession(context.Background(), OpenRequest{Cwd: repo}, fakeEngineSessionCoord{},
		"", "builder", "", "")
	require.NoError(t, err)
	require.NotNil(t, chat)

	require.NotNil(t, client.gotReq)
	assert.Equal(t, agent.RuntimeContainer, client.gotReq.Runtime, "sanity: the runtime axis actually reached the ChatRequest")

	first := <-chat.Events
	require.NotNil(t, first.Entry)
	assert.Equal(t, agent.EntryTypeSystem, first.Entry.Type)
	assert.Contains(t, first.Entry.Content, `agent "builder"`)
	assert.Contains(t, first.Entry.Content, "RUNTIME isolated inside a container")
	assert.Contains(t, first.Entry.Content, "NOT running directly on your host")

	chat.Close()
}
