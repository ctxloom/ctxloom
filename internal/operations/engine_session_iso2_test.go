package operations

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/lm/isolation"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// ISO2 (v0.7.0 ACP Hub plan): tests for the ACP-hosted WORKSPACE axis —
// acpWorkspaceAxis (the posture gate), prepareACPWorkspace (the real
// isolation.Prepare wiring), announceOnFirstEvent (the D-ISO announce-only
// mechanism), and OpenEngineSession end to end via the newACPEngineClient
// seam (a fake pb.Client, so no real subprocess is spawned).

// --- acpWorkspaceAxis: the posture gate, in isolation -----------------------

// TestAcpWorkspaceAxis pins the ISO2 posture rule directly: worktree
// isolation applies ONLY when an EXPLICIT --agent flag resolved successfully
// (currentAgent == flagAgent) — never for the plain `ctxloom acp` entry
// (flagAgent == ""), and never when --agent was given but resolution
// DEGRADED to the bare profile flow (currentAgent == "" even though flagAgent
// was set) or resolved to a DIFFERENT name (defensive; today currentAgent is
// always either "" or exactly resolveAgent).
func TestAcpWorkspaceAxis(t *testing.T) {
	cfgDefaultWorktree := &config.Config{Workspace: "worktree"}
	cfgDefaultNone := &config.Config{Workspace: ""}

	cases := []struct {
		name                    string
		cfg                     *config.Config
		flagAgent, currentAgent string
		flagWorkspace           string
		want                    isolation.WorkspaceAxis
	}{
		{
			name: "plain entry (no --agent) never isolates, even with a project-wide worktree default",
			cfg:  cfgDefaultWorktree, flagAgent: "", currentAgent: "",
			want: "",
		},
		{
			name: "plain entry ignores an explicit --workspace too (no --agent at all)",
			cfg:  cfgDefaultNone, flagAgent: "", currentAgent: "",
			flagWorkspace: "worktree", want: "",
		},
		{
			name: "--agent resolution FAILED (degraded to profile flow) never isolates",
			cfg:  cfgDefaultWorktree, flagAgent: "reviewer", currentAgent: "",
			want: "",
		},
		{
			name: "explicit --agent + project default worktree: isolates",
			cfg:  cfgDefaultWorktree, flagAgent: "reviewer", currentAgent: "reviewer",
			want: isolation.WorkspaceWorktree,
		},
		{
			name: "explicit --agent + project default none: does NOT isolate",
			cfg:  cfgDefaultNone, flagAgent: "reviewer", currentAgent: "reviewer",
			want: "",
		},
		{
			name: "explicit --agent + explicit --workspace worktree wins over a none project default",
			cfg:  cfgDefaultNone, flagAgent: "reviewer", currentAgent: "reviewer",
			flagWorkspace: "worktree", want: isolation.WorkspaceWorktree,
		},
		{
			name: "explicit --agent + explicit --workspace none overrides a worktree project default",
			cfg:  cfgDefaultWorktree, flagAgent: "reviewer", currentAgent: "reviewer",
			flagWorkspace: "none", want: isolation.WorkspaceShared,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := acpWorkspaceAxis(tc.cfg, tc.flagAgent, tc.currentAgent, tc.flagWorkspace)
			assert.Equal(t, tc.want, got)
		})
	}
}

// --- prepareACPWorkspace: the real isolation.Prepare wiring -----------------

// TestPrepareACPWorkspace_NoWorktreeAxis proves the fast path: when the axis
// doesn't ask for a worktree, prepareACPWorkspace returns (nil, nil) WITHOUT
// ever calling isolation.Prepare (the prepareIsolation seam fails the test if
// invoked) — the mechanism-level half of "the plain entry never touches
// isolation".
func TestPrepareACPWorkspace_NoWorktreeAxis(t *testing.T) {
	prev := prepareIsolation
	prepareIsolation = func(context.Context, isolation.Axes, string, isolation.ImageConfig, string, string, isolation.SessionState) (isolation.Policy, isolation.Workspace) {
		t.Fatal("isolation.Prepare must never be called when the axis doesn't ask for a worktree")
		return nil, nil
	}
	t.Cleanup(func() { prepareIsolation = prev })

	aw, err := prepareACPWorkspace(context.Background(), &config.Config{}, isolation.Axes{}, "mock", "", t.TempDir(), nil)
	require.NoError(t, err)
	assert.Nil(t, aw)
}

// TestPrepareACPWorkspace_RealWorktree proves the payload against a REAL git
// repo and the REAL isolation.Prepare (no stubbing): a fresh, genuinely
// separate worktree checkout is created on disk, its announcement text names
// that real path, and Cleanup performs the real WIP-safe teardown (the
// directory is gone afterward).
func TestPrepareACPWorkspace_RealWorktree(t *testing.T) {
	requireGit(t)
	t.Setenv("HOME", t.TempDir()) // isolate paths.HarpEphemeralDir's fallback (OS temp dir) from the real home
	repo := initTestRepo(t)

	axes := isolation.Axes{Workspace: isolation.WorkspaceWorktree}
	aw, err := prepareACPWorkspace(context.Background(), &config.Config{}, axes, "mock", "reviewer", repo, nil)
	require.NoError(t, err)
	require.NotNil(t, aw)
	t.Cleanup(func() { _ = os.RemoveAll(aw.dir) }) // safety net if an assertion below fails first

	assert.NotEqual(t, repo, aw.dir, "the worktree must be a SEPARATE directory from the live project dir")
	info, statErr := os.Stat(aw.dir)
	require.NoError(t, statErr, "the worktree directory must exist on disk")
	assert.True(t, info.IsDir())
	assert.FileExists(t, filepath.Join(aw.dir, "README.md"), "the worktree is a real checkout of the repo")
	require.NotEmpty(t, aw.announce, "a REAL worktree (not a none-degrade) must carry the D-ISO announcement")
	assert.Contains(t, aw.announce, aw.dir, "the announcement must name the actual worktree path")

	aw.cleanup()
	assert.NoDirExists(t, aw.dir, "Cleanup must remove the worktree from disk (the session-end lifecycle)")
}

// TestPrepareACPWorkspace_NonGitDegradesSilently: PrepareWorkspace degrading
// worktree→none (not a git repo) must NOT announce — isolation.Isolated(none)
// is false, so the honesty message never fires over a workspace that is, in
// fact, just the live project dir.
func TestPrepareACPWorkspace_NonGitDegradesSilently(t *testing.T) {
	dir := t.TempDir() // deliberately NOT a git repo
	axes := isolation.Axes{Workspace: isolation.WorkspaceWorktree}
	aw, err := prepareACPWorkspace(context.Background(), &config.Config{}, axes, "mock", "reviewer", dir, nil)
	require.NoError(t, err)
	require.NotNil(t, aw)
	assert.Equal(t, dir, aw.dir, "a degrade to none keeps the live project dir")
	assert.Empty(t, aw.announce, "no announcement over a workspace that never actually isolated")
}

// --- announceOnFirstEvent: the D-ISO delivery mechanism ---------------------

// TestAnnounceOnFirstEvent proves the announcement reaches the wrapped
// channel as the FIRST event, verbatim, ahead of — and without altering —
// whatever the engine itself emits afterward.
func TestAnnounceOnFirstEvent(t *testing.T) {
	src := make(chan agent.ChatEvent, 2)
	src <- agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeAssistant, Content: "engine says hi"}}
	src <- agent.ChatEvent{Complete: &agent.TurnMeta{StopReason: "end_turn"}}
	close(src)

	out := announceOnFirstEvent(context.Background(), src, "ctxloom: isolated to /fake/worktree")

	first := <-out
	require.NotNil(t, first.Entry, "the first event must be a synthetic entry, not the engine's own")
	assert.Equal(t, agent.EntryTypeSystem, first.Entry.Type)
	assert.Equal(t, "ctxloom: isolated to /fake/worktree", first.Entry.Content)

	second := <-out
	require.NotNil(t, second.Entry)
	assert.Equal(t, "engine says hi", second.Entry.Content, "the engine's own event forwards unchanged")

	third := <-out
	require.NotNil(t, third.Complete)
	assert.Equal(t, "end_turn", third.Complete.StopReason)

	_, ok := <-out
	assert.False(t, ok, "the wrapped channel closes once the source closes")
}

// --- OpenEngineSession end to end (fake pb.Client, no real subprocess) -----

// fakeACPEngineClient is a minimal pb.Client that records the ChatRequest it
// received and emits one recognizable marker event, so a test can assert
// WHERE the engine was told to run (WorkDir) and that no announcement was
// spliced in ahead of the engine's own first event.
type fakeACPEngineClient struct {
	gotReq *agent.ChatRequest
}

func (c *fakeACPEngineClient) Chat(_ context.Context, req agent.ChatRequest) (chan<- agent.ChatMessage, <-chan agent.ChatEvent, <-chan error, error) {
	reqCopy := req
	c.gotReq = &reqCopy
	in := make(chan agent.ChatMessage, 1)
	events := make(chan agent.ChatEvent, 1)
	events <- agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeAssistant, Content: "engine-marker"}}
	close(events)
	errs := make(chan error, 1)
	close(errs)
	return in, events, errs, nil
}
func (c *fakeACPEngineClient) Info(context.Context) (*pb.LLMInfo, error) { return &pb.LLMInfo{}, nil }
func (c *fakeACPEngineClient) Run(context.Context, *pb.RunStart, io.Reader, io.Writer, io.Writer, <-chan *pb.WindowSize) (int32, error) {
	return 0, nil
}
func (c *fakeACPEngineClient) RunWithModelInfo(context.Context, *pb.RunStart, io.Reader, io.Writer, io.Writer, <-chan *pb.WindowSize) (*pb.RunResult, error) {
	return &pb.RunResult{}, nil
}
func (c *fakeACPEngineClient) GetSession(context.Context, string) (*agent.Session, error) {
	return nil, nil
}
func (c *fakeACPEngineClient) WatchSession(context.Context, string) (<-chan *pb.WatchEvent, <-chan error, error) {
	return nil, nil, nil
}
func (c *fakeACPEngineClient) ListSessions(context.Context) ([]agent.SessionMeta, error) {
	return nil, nil
}
func (c *fakeACPEngineClient) GetPlans(context.Context, string) ([]agent.PlanFile, error) {
	return nil, nil
}
func (c *fakeACPEngineClient) Kill() {}

// fakeEngineSessionCoord is a minimal EngineSessionCoordinator: no delegation
// reach-back (SessionEnv → nil trio) and no coordinator standup (WatchChildren
// → nil) — the documented degrade OpenEngineSession already handles.
type fakeEngineSessionCoord struct{}

func (fakeEngineSessionCoord) SessionEnv(*config.Config, string, string) map[string]string {
	return nil
}
func (fakeEngineSessionCoord) WatchChildren() func(ctx context.Context) (<-chan ChildUpdate, func()) {
	return nil
}

// stubACPEngineClient swaps OpenEngineSession's newACPEngineClient seam for
// one that always hands back client, restoring the production seam on
// cleanup — mirrors stubPrepareIsolation's style (oneshot_isolation_gate_test.go).
func stubACPEngineClient(t *testing.T, client pb.Client) {
	t.Helper()
	prev := newACPEngineClient
	newACPEngineClient = func(string, string, int, map[string]string) (pb.Client, error) {
		return client, nil
	}
	t.Cleanup(func() { newACPEngineClient = prev })
}

// writeACPTestProject writes a real git repo (one commit) with a `.ctxloom`
// config declaring the given project-wide workspace default and a "reviewer"
// agent bound to the "mock" backend, and returns the repo root (== the config's
// AppDir's parent == req.Cwd).
func writeACPTestProject(t *testing.T, workspaceDefault string) string {
	t.Helper()
	repo := initTestRepo(t)
	appDir := filepath.Join(repo, ".ctxloom")
	require.NoError(t, os.MkdirAll(appDir, 0o755))
	body := "version: 5\n"
	if workspaceDefault != "" {
		body += "workspace: " + workspaceDefault + "\n"
	}
	body += "agents:\n  reviewer:\n    engine: mock\n"
	require.NoError(t, os.WriteFile(filepath.Join(appDir, "config.yaml"), []byte(body), 0o644))
	return repo
}

// TestOpenEngineSession_ExplicitAgentWorktree is the headline ISO2 proof: an
// ACP session bound to an EXPLICIT --agent, with the project's `workspace:`
// default set to "worktree", actually runs the engine in a fresh git
// worktree — proven by inspecting the REAL ChatRequest.WorkDir the fake
// engine client received (a real directory, distinct from the repo, that is
// a genuine git checkout) — and the announcement reaches the client as the
// FIRST event on the session's Events channel, naming that exact path. On
// Close, the worktree is removed from disk (WIP-safe teardown ran).
func TestOpenEngineSession_ExplicitAgentWorktree(t *testing.T) {
	requireGit(t)
	resetStrictness(t)
	t.Setenv("HOME", t.TempDir())
	repo := writeACPTestProject(t, "worktree")

	client := &fakeACPEngineClient{}
	stubACPEngineClient(t, client)

	chat, err := OpenEngineSession(context.Background(), OpenRequest{Cwd: repo}, fakeEngineSessionCoord{},
		"", "reviewer", "", "")
	require.NoError(t, err)
	require.NotNil(t, chat)

	require.NotNil(t, client.gotReq, "the engine client must have been dialed")
	assert.NotEqual(t, repo, client.gotReq.WorkDir, "the engine's cwd must be the isolated worktree, not the live project dir")
	assert.DirExists(t, client.gotReq.WorkDir)
	assert.FileExists(t, filepath.Join(client.gotReq.WorkDir, "README.md"), "the worktree is a real checkout")
	wtDir := client.gotReq.WorkDir

	first := <-chat.Events
	require.NotNil(t, first.Entry, "the FIRST event on the client-facing channel must be the announcement")
	assert.Equal(t, agent.EntryTypeSystem, first.Entry.Type)
	assert.Contains(t, first.Entry.Content, wtDir, "the announcement must name the real worktree path")

	second := <-chat.Events
	require.NotNil(t, second.Entry)
	assert.Equal(t, "engine-marker", second.Entry.Content, "the engine's own event still follows, unaltered")

	chat.Close()
	assert.NoDirExists(t, wtDir, "closing the session tears the worktree down (WIP-safe)")
}

// TestOpenEngineSession_PlainEntryNeverWorktrees is the negative-space proof
// the memo demands: the plain `ctxloom acp` entry (no --agent) must NEVER get
// a worktree, even though this same project's `workspace:` default is
// "worktree" (the exact config the previous test proves DOES isolate an
// explicit --agent session). isolation.Prepare is stubbed to fail the test if
// called at all — proving the plain entry doesn't even ATTEMPT isolation —
// and the engine's real WorkDir is asserted to be the untouched repo root.
//
// ISO3: the plain entry now ALSO gets the always-on one-line posture
// announcement ahead of the engine's own first event — see
// engine_session_iso3_test.go's TestOpenEngineSession_UnisolatedAnnouncesOnce
// for the payload assertion on that text; this test stays focused on its
// original WorkDir/isolation.Prepare proof and only adjusts the event-order
// assertion to skip past the (now expected) announcement.
func TestOpenEngineSession_PlainEntryNeverWorktrees(t *testing.T) {
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

	// No --agent (flagAgent == ""); llmOverride picks the mock backend so the
	// bare profile flow resolves cleanly with no LM config needed.
	chat, err := OpenEngineSession(context.Background(), OpenRequest{Cwd: repo}, fakeEngineSessionCoord{},
		"", "", "mock", "")
	require.NoError(t, err)
	require.NotNil(t, chat)

	require.NotNil(t, client.gotReq)
	assert.Equal(t, repo, client.gotReq.WorkDir, "the plain entry's engine cwd must stay the live project dir")

	first := <-chat.Events
	require.NotNil(t, first.Entry, "ISO3: the always-on posture announcement is now the first event")
	assert.Equal(t, agent.EntryTypeSystem, first.Entry.Type)

	second := <-chat.Events
	require.NotNil(t, second.Entry)
	assert.Equal(t, "engine-marker", second.Entry.Content, "the engine's own event still follows the announcement, unaltered")

	chat.Close()
}

// TestOpenEngineSession_ExplicitAgentWithoutWorktreeRequest: an --agent
// session where NEITHER --workspace NOR the project default asks for a
// worktree stays on the live project dir — the hard constraint that
// isolation is additive, never assumed just because an agent bound.
func TestOpenEngineSession_ExplicitAgentWithoutWorktreeRequest(t *testing.T) {
	resetStrictness(t)
	t.Setenv("HOME", t.TempDir())
	repo := writeACPTestProject(t, "") // no project-wide workspace default

	client := &fakeACPEngineClient{}
	stubACPEngineClient(t, client)

	chat, err := OpenEngineSession(context.Background(), OpenRequest{Cwd: repo}, fakeEngineSessionCoord{},
		"", "reviewer", "", "")
	require.NoError(t, err)
	require.NotNil(t, chat)
	require.NotNil(t, client.gotReq)
	assert.Equal(t, repo, client.gotReq.WorkDir, "an --agent session that never asked for a worktree stays on the host cwd")

	first := <-chat.Events
	require.NotNil(t, first.Entry, "ISO3: the always-on posture announcement is still the first event")
	assert.Equal(t, `ctxloom: agent "reviewer" (engine mock) — HOST process (no container), this project's live working directory (no worktree). Not isolated on either axis.`, first.Entry.Content)
	<-chat.Events // drain the engine's own marker event too

	chat.Close()
}

// TestOpenEngineSession_UnknownAgentNeverWorktrees: an --agent naming a
// binding that doesn't exist degrades to the bare profile flow (currentAgent
// stays "") — acpWorkspaceAxis's currentAgent==flagAgent check must still
// refuse to isolate, exactly like the no-agent case.
func TestOpenEngineSession_UnknownAgentNeverWorktrees(t *testing.T) {
	resetStrictness(t)
	t.Setenv("HOME", t.TempDir())
	repo := writeACPTestProject(t, "worktree")

	prevPrep := prepareIsolation
	prepareIsolation = func(context.Context, isolation.Axes, string, isolation.ImageConfig, string, string, isolation.SessionState) (isolation.Policy, isolation.Workspace) {
		t.Fatal("an unresolvable --agent must degrade to the profile flow, never isolation.Prepare")
		return nil, nil
	}
	t.Cleanup(func() { prepareIsolation = prevPrep })

	client := &fakeACPEngineClient{}
	stubACPEngineClient(t, client)

	chat, err := OpenEngineSession(context.Background(), OpenRequest{Cwd: repo}, fakeEngineSessionCoord{},
		"", "no-such-agent", "mock", "")
	require.NoError(t, err)
	require.NotNil(t, chat)
	require.NotNil(t, client.gotReq)
	assert.Equal(t, repo, client.gotReq.WorkDir)

	// ISO3: the fail-soft this test drives (an unresolvable --agent) is
	// exactly the case that must be LOUD in the chat — see
	// engine_session_iso3_test.go's TestOpenEngineSession_UnknownAgentAnnouncementIsLoud
	// for the full payload assertion; drain both events here so this test
	// doesn't leak the announce goroutine.
	<-chat.Events
	<-chat.Events

	chat.Close()
}

// --- shared real-git fixtures (mirrors isolation package's own convention) -

// requireGit skips the test cleanly when git is unavailable, so the normal
// suite stays green on a git-less host.
func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH; skipping a real-git ISO2 test")
	}
}

// initTestRepo creates a temp git repo with one commit and returns its path.
// Mirrors internal/lm/isolation/worktree_integration_test.go's initRealRepo —
// duplicated rather than imported (that helper is unexported in a different
// package) so this package's tests stay self-contained.
func initTestRepo(t *testing.T) string {
	t.Helper()
	requireGit(t)
	dir := t.TempDir()
	iso2GitRun(t, dir, "init", "-b", "main")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("seed"), 0o644))
	iso2GitRun(t, dir, "add", "README.md")
	iso2GitRun(t, dir, "commit", "-m", "seed")
	return dir
}

func iso2GitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=ctxloom", "GIT_AUTHOR_EMAIL=ctxloom@example.com",
		"GIT_COMMITTER_NAME=ctxloom", "GIT_COMMITTER_EMAIL=ctxloom@example.com",
		// Isolate from the developer's/CI's GLOBAL and SYSTEM git config —
		// see worktree_integration_test.go's gitCmd for why.
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}
