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

// ISO3/ISO4 (v0.7.0 ACP Hub plan, follow-on to D-ISO/ISO2): the RUNTIME axis
// (host process vs container), the agent-resolution fail-soft, and (ISO4)
// the resolved model/profiles/fragments/commands/skills/MCP set were
// previously INVISIBLE to the editor — only the WORKSPACE axis (a real
// worktree) ever announced itself. This file proves buildSessionInitSummary
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

// --- buildSessionInitSummary: pure unit tests, one per posture ------------

func TestBuildSessionInitSummary_HostAndCwd_AgentResolved(t *testing.T) {
	got := buildSessionInitSummary(sessionInitSummaryInputs{
		cfg:            &config.Config{},
		backendName:    "claude-sonnet",
		requestedAgent: "coder",
		currentAgent:   "coder",
		label:          "claude-sonnet",
		model:          "claude-sonnet-4-5-20260929",
		profiles:       []string{"reviewer"},
		workDir:        "/home/user/project",
	})
	want := "ctxloom: session initialization summary\n" +
		"  isolation : HOST process (no container); working directory /home/user/project (no worktree) — NOT isolated on either axis\n" +
		`  agent     : agent "coder" (engine claude-sonnet)` + "\n" +
		"  model     : claude-sonnet-4-5-20260929\n" +
		"  profiles  : [reviewer]\n" +
		"  fragments : none loaded into the lead context\n" +
		"  commands  : none available\n" +
		"  skills    : none installed\n" +
		"  mcp       : none configured (connection status is not observable at connect)\n" +
		"  permissions: every tool call is forwarded to your editor for a real-time approval decision (this session never auto-bypasses)"
	assert.Equal(t, want, got)
	assert.Less(t, len(want), 900, "sanity: still a scannable block, not runaway text")
}

func TestBuildSessionInitSummary_HostAndCwd_NoAgentBound(t *testing.T) {
	got := buildSessionInitSummary(sessionInitSummaryInputs{
		cfg:         &config.Config{},
		backendName: "claude-sonnet",
		label:       "claude-sonnet",
		workDir:     "/home/user/project",
	})
	want := "ctxloom: session initialization summary\n" +
		"  isolation : HOST process (no container); working directory /home/user/project (no worktree) — NOT isolated on either axis\n" +
		"  agent     : no agent bound (profile flow)\n" +
		"  model     : engine default (ctxloom pinned none)\n" +
		"  profiles  : none\n" +
		"  fragments : none loaded into the lead context\n" +
		"  commands  : none available\n" +
		"  skills    : none installed\n" +
		"  mcp       : none configured (connection status is not observable at connect)\n" +
		"  permissions: every tool call is forwarded to your editor for a real-time approval decision (this session never auto-bypasses)"
	assert.Equal(t, want, got)
}

func TestBuildSessionInitSummary_AgentNotFound(t *testing.T) {
	got := buildSessionInitSummary(sessionInitSummaryInputs{
		cfg:            &config.Config{},
		backendName:    "claude-sonnet",
		requestedAgent: "default",
		workDir:        "/home/user/project",
	})
	want := `ctxloom: WARNING — agent "default" was requested but NOT FOUND; this session fell back to the plain profile flow instead of refusing to open. NONE of that agent's engine override, composed profiles, permissions posture, or runtime isolation apply — it is running on the HOST, unisolated, against this project's live working directory, /home/user/project. Check the agent name (see ` + "`ctxloom acp entries`" + `) and reconnect.`
	assert.Equal(t, want, got)
}

func TestBuildSessionInitSummary_WorktreeOnly(t *testing.T) {
	aw := &acpWorkspace{dir: "/tmp/fake-worktree", announce: "irrelevant marker: only emptiness is checked"}
	got := buildSessionInitSummary(sessionInitSummaryInputs{
		cfg:            &config.Config{},
		backendName:    "mock",
		requestedAgent: "reviewer",
		currentAgent:   "reviewer",
		label:          "mock",
		workDir:        "/tmp/fake-worktree",
		aw:             aw,
	})
	want := "ctxloom: session initialization summary\n" +
		"  isolation : RUNTIME: a HOST process (no container); WORKSPACE isolated to its own git worktree — /tmp/fake-worktree. Your editor's view of this project is NOT touched directly: the engine's edits land in that worktree, and this window stays blind to it unless you open the path yourself. Results return through ctxloom's normal delegated-child assemble/merge flow.\n" +
		`  agent     : agent "reviewer" (engine mock)` + "\n" +
		"  model     : engine default (ctxloom pinned none)\n" +
		"  profiles  : none\n" +
		"  fragments : none loaded into the lead context\n" +
		"  commands  : none available\n" +
		"  skills    : none installed\n" +
		"  mcp       : none configured (connection status is not observable at connect)\n" +
		"  permissions: every tool call is forwarded to your editor for a real-time approval decision (this session never auto-bypasses)"
	assert.Equal(t, want, got)
}

func TestBuildSessionInitSummary_ContainerOnly_ConfiguredImage(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{IsolationImages: map[string]string{"claude-sonnet": "myimg:latest"}})
	got := buildSessionInitSummary(sessionInitSummaryInputs{
		cfg:            cfg,
		backendName:    "claude-sonnet",
		requestedAgent: "coder",
		currentAgent:   "coder",
		label:          "claude-sonnet",
		runtimeAxis:    agent.RuntimeContainer,
		workDir:        "/home/user/project",
	})
	want := "ctxloom: session initialization summary\n" +
		"  isolation : RUNTIME isolated inside a container (image myimg:latest) — NOT running directly on your host; WORKSPACE mounted identically: host /home/user/project -> container /home/user/project (no worktree)\n" +
		`  agent     : agent "coder" (engine claude-sonnet)` + "\n" +
		"  model     : engine default (ctxloom pinned none)\n" +
		"  profiles  : none\n" +
		"  fragments : none loaded into the lead context\n" +
		"  commands  : none available\n" +
		"  skills    : none installed\n" +
		"  mcp       : none configured (connection status is not observable at connect)\n" +
		"  permissions: every tool call is forwarded to your editor for a real-time approval decision (this session never auto-bypasses)"
	assert.Equal(t, want, got)
}

func TestBuildSessionInitSummary_ContainerOnly_NoConfiguredImage(t *testing.T) {
	got := buildSessionInitSummary(sessionInitSummaryInputs{
		cfg:            &config.Config{},
		backendName:    "claude-sonnet",
		requestedAgent: "coder",
		currentAgent:   "coder",
		label:          "claude-sonnet",
		runtimeAxis:    agent.RuntimeContainer,
		workDir:        "/home/user/project",
	})
	want := "ctxloom: session initialization summary\n" +
		"  isolation : RUNTIME isolated inside a container (an auto-selected image) — NOT running directly on your host; WORKSPACE mounted identically: host /home/user/project -> container /home/user/project (no worktree)\n" +
		`  agent     : agent "coder" (engine claude-sonnet)` + "\n" +
		"  model     : engine default (ctxloom pinned none)\n" +
		"  profiles  : none\n" +
		"  fragments : none loaded into the lead context\n" +
		"  commands  : none available\n" +
		"  skills    : none installed\n" +
		"  mcp       : none configured (connection status is not observable at connect)\n" +
		"  permissions: every tool call is forwarded to your editor for a real-time approval decision (this session never auto-bypasses)"
	assert.Equal(t, want, got)
}

func TestBuildSessionInitSummary_ContainerAndWorktree(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{IsolationImages: map[string]string{"claude-sonnet": "myimg:latest"}})
	aw := &acpWorkspace{dir: "/tmp/fake-worktree", announce: "irrelevant marker: only emptiness is checked"}
	got := buildSessionInitSummary(sessionInitSummaryInputs{
		cfg:            cfg,
		backendName:    "claude-sonnet",
		requestedAgent: "coder",
		currentAgent:   "coder",
		label:          "claude-sonnet",
		runtimeAxis:    agent.RuntimeContainer,
		workDir:        "/tmp/fake-worktree",
		aw:             aw,
	})
	want := "ctxloom: session initialization summary\n" +
		"  isolation : RUNTIME isolated inside a container (image myimg:latest) — NOT running directly on your host; WORKSPACE isolated to its own git worktree — /tmp/fake-worktree. Your editor's view of this project is NOT touched directly: the engine's edits land in that worktree, and this window stays blind to it unless you open the path yourself. Results return through ctxloom's normal delegated-child assemble/merge flow.\n" +
		`  agent     : agent "coder" (engine claude-sonnet)` + "\n" +
		"  model     : engine default (ctxloom pinned none)\n" +
		"  profiles  : none\n" +
		"  fragments : none loaded into the lead context\n" +
		"  commands  : none available\n" +
		"  skills    : none installed\n" +
		"  mcp       : none configured (connection status is not observable at connect)\n" +
		"  permissions: every tool call is forwarded to your editor for a real-time approval decision (this session never auto-bypasses)"
	assert.Equal(t, want, got)
}

// TestBuildSessionInitSummary_DegradedWorktreeNeverClaimsIsolation proves the
// aw.announce == "" case (worktree requested but silently degraded to none,
// see prepareACPWorkspace's TestPrepareACPWorkspace_NonGitDegradesSilently)
// falls through to the unisolated line, never the worktree paragraph — a
// workspace that never actually isolated must never be announced as if it
// did.
func TestBuildSessionInitSummary_DegradedWorktreeNeverClaimsIsolation(t *testing.T) {
	aw := &acpWorkspace{dir: "/some/live/dir", announce: ""}
	got := buildSessionInitSummary(sessionInitSummaryInputs{
		cfg:            &config.Config{},
		backendName:    "mock",
		requestedAgent: "reviewer",
		currentAgent:   "reviewer",
		label:          "mock",
		workDir:        "/some/live/dir",
		aw:             aw,
	})
	assert.Contains(t, got, "HOST process (no container); working directory /some/live/dir (no worktree) — NOT isolated on either axis")
	assert.NotContains(t, got, "git worktree")
}

// TestBuildSessionInitSummary_ModelProfilesFragmentsNamed proves ISO4's
// three added identity/assembly facts render as REAL values, not the
// generic label: model is the value that would actually reach the engine's
// env (see OpenEngineSession's ChatRequest.Model / ACPConfig.spawnEnv),
// profiles names the composed set, and short fragment/command/skill/mcp
// lists print their names in full (namesOrCount's short-list branch).
func TestBuildSessionInitSummary_ModelProfilesFragmentsNamed(t *testing.T) {
	got := buildSessionInitSummary(sessionInitSummaryInputs{
		cfg:             &config.Config{},
		backendName:     "claude-sonnet",
		requestedAgent:  "coder",
		currentAgent:    "coder",
		label:           "claude-sonnet",
		model:           "claude-opus-4-6-20260609",
		profiles:        []string{"reviewer", "go-house-style"},
		fragmentsLoaded: listed([]string{"builtin:isolation#fragments/isolation-axes", "go-patterns#fragments/errors"}),
		commandNames:    listed([]string{"review", "test"}),
		skillNames:      listed([]string{"code-review"}),
		mcpServerNames:  listed([]string{"ctxloom-context", "taskloom"}),
		workDir:         "/home/user/project",
	})
	assert.Contains(t, got, "model     : claude-opus-4-6-20260609")
	assert.Contains(t, got, "profiles  : [reviewer, go-house-style]")
	assert.Contains(t, got, "fragments : 2 (builtin:isolation#fragments/isolation-axes, go-patterns#fragments/errors) loaded into the lead context")
	assert.Contains(t, got, "commands  : 2 (review, test) available")
	assert.Contains(t, got, "skills    : 1 (code-review) installed")
	assert.Contains(t, got, "mcp       : 2 (ctxloom-context, taskloom) configured (connection status is not observable at connect)")
}

// TestBuildSessionInitSummary_LongListsCollapseToCount proves namesOrCount's
// long-list branch: once a set exceeds nameListCap, the summary falls back
// to a bare count plus the CLI command that lists it in full, rather than
// spelling out a dozen+ names and turning the block into a wall of text.
func TestBuildSessionInitSummary_LongListsCollapseToCount(t *testing.T) {
	many := make([]string, nameListCap+1)
	for i := range many {
		many[i] = "frag" + string(rune('a'+i))
	}
	got := buildSessionInitSummary(sessionInitSummaryInputs{
		cfg:             &config.Config{},
		backendName:     "claude-sonnet",
		requestedAgent:  "coder",
		currentAgent:    "coder",
		label:           "claude-sonnet",
		fragmentsLoaded: listed(many),
		workDir:         "/home/user/project",
	})
	assert.Contains(t, got, "fragments : 9 (see `ctxloom run --dry-run`) loaded into the lead context")
	assert.NotContains(t, got, "fraga")
}

// --- end-to-end payload proofs: the summary actually reaches the client- --
// --- facing Events channel as the FIRST event, for real postures ---------

// TestOpenEngineSession_UnisolatedAnnouncesOnce proves the ALWAYS decision:
// even the fully unisolated, no-agent, plain `ctxloom acp` entry gets the
// initialization summary — set unconditionally on EngineChat.InitSummary,
// independent of the engine's own Events, which carry ONLY the engine's
// real output now (no synthetic entry spliced in).
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

	assert.Contains(t, chat.InitSummary, "ctxloom: session initialization summary")
	assert.Contains(t, chat.InitSummary, "agent     : no agent bound (profile flow)")
	assert.Contains(t, chat.InitSummary, "working directory "+repo)
	assert.Contains(t, chat.InitSummary, "NOT isolated on either axis")

	first := <-chat.Events
	require.NotNil(t, first.Entry)
	assert.Equal(t, "engine-marker", first.Entry.Content, "Events carries only the engine's own output — the summary rides InitSummary instead")

	chat.Close()
}

// TestOpenEngineSession_UnknownAgentAnnouncementIsLoud is the headline ISO3
// regression proof, now covering the AUTO-BOUND default agent.
//
// An EXPLICIT `--agent <typo>` no longer degrades at all — it refuses the
// session (sandy-boxer; see engine_session_iso2_test.go). But a project whose
// cfg.DefaultAgent names a missing agent still degrades, because the editor
// never asked for that binding and hard-breaking every plain session over a
// stale config setting would punish a choice the user did not make here. That
// surviving degrade is the one this proves is IMPOSSIBLE to miss in the chat
// transcript itself, not just a stderr line editors bury.
func TestOpenEngineSession_UnknownAgentAnnouncementIsLoud(t *testing.T) {
	resetStrictness(t)
	t.Setenv("HOME", t.TempDir())
	repo := initTestRepo(t)
	appDir := filepath.Join(repo, ".ctxloom")
	require.NoError(t, os.MkdirAll(appDir, 0o755))
	body := "version: 5\ndefault_agent: no-such-agent\n"
	require.NoError(t, os.WriteFile(filepath.Join(appDir, "config.yaml"), []byte(body), 0o644))

	client := &fakeACPEngineClient{}
	stubACPEngineClient(t, client)

	chat, err := OpenEngineSession(context.Background(), OpenRequest{Cwd: repo}, fakeEngineSessionCoord{},
		"", "", "mock", "")
	require.NoError(t, err, "an auto-bound default agent still degrades rather than refusing")
	require.NotNil(t, chat)

	assert.Contains(t, chat.InitSummary, `agent "no-such-agent" was requested but NOT FOUND`)
	assert.Contains(t, chat.InitSummary, "running on the HOST, unisolated")
	assert.Contains(t, chat.InitSummary, repo, "the WARNING names the real working directory too")

	first := <-chat.Events
	require.NotNil(t, first.Entry, "Events still carries the engine's own event; the agent-not-found warning rides InitSummary instead")
	assert.Equal(t, "engine-marker", first.Entry.Content)

	chat.Close()
}

// TestOpenEngineSession_ContainerRuntimeAnnounces proves the RUNTIME axis
// (previously silent) now announces too, with the host->container mount
// shown on both sides: an agent declaring `runtime: container` gets a
// first-turn message naming the container posture, even though its
// workspace stays the shared project dir (no worktree requested).
func TestOpenEngineSession_ContainerRuntimeAnnounces(t *testing.T) {
	resetStrictness(t)
	t.Setenv("HOME", t.TempDir())
	repo := initTestRepo(t)
	appDir := filepath.Join(repo, ".ctxloom")
	require.NoError(t, os.MkdirAll(appDir, 0o755))
	// agents.*.runtime is ScopeShared (internal/config/layerscope) — a
	// deliberate divergence from the design doc's literal ScopeMachine, made
	// because ScopeMachine here interacts with agentBindingMergeFunc's
	// atomic-replace rule (D) to make it IMPOSSIBLE for any project-defined
	// agent to ever carry a non-host runtime through a persisted file (home's
	// contribution is replaced wholesale the instant project also names the
	// same agent, which it must to give the agent a real `engine` —
	// agents.*.llm stays Shared, home-disallowed). See the report's
	// divergence section. So this fixture is exactly what it looks like: an
	// ordinary project-declared agent, engine and runtime both in the same
	// (committed) file, same as before this change.
	body := "version: 5\n" +
		"agents:\n  builder:\n    llm: mock\n    runtime: container\n"
	require.NoError(t, os.WriteFile(filepath.Join(appDir, "config.yaml"), []byte(body), 0o644))

	client := &fakeACPEngineClient{}
	stubACPEngineClient(t, client)

	chat, err := OpenEngineSession(context.Background(), OpenRequest{Cwd: repo}, fakeEngineSessionCoord{},
		"", "builder", "", "")
	require.NoError(t, err)
	require.NotNil(t, chat)

	require.NotNil(t, client.gotReq)
	assert.Equal(t, agent.RuntimeContainer, client.gotReq.Runtime, "sanity: the runtime axis actually reached the ChatRequest")

	assert.Contains(t, chat.InitSummary, `agent "builder"`)
	assert.Contains(t, chat.InitSummary, "RUNTIME isolated inside a container")
	assert.Contains(t, chat.InitSummary, "NOT running directly on your host")
	assert.Contains(t, chat.InitSummary, "host "+repo+" -> container "+repo, "the same-path mount is shown on BOTH sides, not asserted in prose")

	chat.Close()
}

// TestOpenEngineSession_InitSummaryModelMatchesDelivered is ISO4's strongest
// proof: the model NAMED in the init summary is not just some string ctxloom
// printed — it is BYTE-IDENTICAL to req.Model, the value OpenEngineSession
// actually placed on the ChatRequest the engine client received (and which
// internal/acp's spawnEnv, session.go, stamps under the backend's own env
// var — ANTHROPIC_MODEL for claude — only when non-empty). A test asserting
// a hardcoded model string would prove nothing about correctness; this one
// reads the SAME resolved value the real delivery path reads and compares
// the summary against it. It also proves profiles/commands/mcp name real,
// resolved facts rather than placeholders — the "coder" agent's declared
// profile appears, and ctxloom's own auto-registered MCP server ("ctxloom",
// wire.MCPConfig.ShouldAutoRegisterCtxloom's default-true) appears in the mcp
// line, since no config here disables it.
func TestOpenEngineSession_InitSummaryModelMatchesDelivered(t *testing.T) {
	resetStrictness(t)
	t.Setenv("HOME", t.TempDir())
	repo := initTestRepo(t)
	appDir := filepath.Join(repo, ".ctxloom")
	require.NoError(t, os.MkdirAll(appDir, 0o755))
	body := "version: 5\n" +
		"llm:\n  configs:\n    fast-model:\n      type: mock\n      model: mock-model-v1\n" +
		"agents:\n  coder:\n    llm: fast-model\n"
	require.NoError(t, os.WriteFile(filepath.Join(appDir, "config.yaml"), []byte(body), 0o644))

	client := &fakeACPEngineClient{}
	stubACPEngineClient(t, client)

	chat, err := OpenEngineSession(context.Background(), OpenRequest{Cwd: repo}, fakeEngineSessionCoord{},
		"", "coder", "", "")
	require.NoError(t, err)
	require.NotNil(t, chat)

	require.NotNil(t, client.gotReq)
	require.NotEmpty(t, client.gotReq.Model, "sanity: this config actually resolved a model")
	assert.Contains(t, chat.InitSummary, "model     : "+client.gotReq.Model,
		"the summary's model line must be the SAME value delivered to the engine, not a re-derived or hardcoded string")
	assert.Equal(t, "mock-model-v1", client.gotReq.Model, "sanity: the configured model actually reached the ChatRequest")

	assert.Contains(t, chat.InitSummary, `agent     : agent "coder" (engine fast-model)`)
	assert.Contains(t, chat.InitSummary, "mcp       : 1 (ctxloom) configured", "ctxloom's own MCP server auto-registers by default")

	<-chat.Events
	chat.Close()
}

// TestOpenEngineSession_InitSummaryModelUnconfiguredIsHonest proves the other
// half of the model-honesty requirement: when nothing pins a model, the
// summary says so plainly rather than guessing or inventing one — and this
// matches req.Model actually being empty (so the engine truly does fall back
// to its own default; see internal/acp's spawnEnv, which skips setting the
// backend's model env var entirely when req.Model == "").
func TestOpenEngineSession_InitSummaryModelUnconfiguredIsHonest(t *testing.T) {
	resetStrictness(t)
	t.Setenv("HOME", t.TempDir())
	repo := writeACPTestProject(t, "") // "reviewer" agent: engine mock, no model configured

	client := &fakeACPEngineClient{}
	stubACPEngineClient(t, client)

	chat, err := OpenEngineSession(context.Background(), OpenRequest{Cwd: repo}, fakeEngineSessionCoord{},
		"", "reviewer", "", "")
	require.NoError(t, err)
	require.NotNil(t, chat)

	require.NotNil(t, client.gotReq)
	assert.Empty(t, client.gotReq.Model, "sanity: no model was configured for this label")
	assert.Contains(t, chat.InitSummary, "model     : engine default (ctxloom pinned none)")

	<-chat.Events
	chat.Close()
}
