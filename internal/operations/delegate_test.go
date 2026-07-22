package operations

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/git"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/lm/isolation"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// TestPrepareAgentChat_RuntimeAxisChosen pins that a delegated child's
// isolation chain is selected from the AGENT's resolved runtime axis crossed
// with the project's session workspace default — the same axes semantics the
// fan applies.
func TestPrepareAgentChat_RuntimeAxisChosen(t *testing.T) {
	resetStrictness(t)
	var gotAxes isolation.Axes
	prev := prepareIsolation
	prepareIsolation = func(_ context.Context, axes isolation.Axes, _ string, _ isolation.ImageConfig, projectDir, _ string, _ isolation.SessionState) (isolation.Policy, isolation.Workspace) {
		gotAxes = axes
		return stubPolicy{mk: func() pb.Client { return &stubClient{} }}, stubWorkspace{dir: projectDir}
	}
	t.Cleanup(func() { prepareIsolation = prev })

	cfg := config.NewFixture(config.Fixture{Workspace: "worktree"})
	p, err := PrepareAgentChat(context.Background(), cfg, AgentChatRequest{
		Resolved: &ResolvedAgent{Name: "builder", Backend: "mock", Label: "fast", Runtime: "container"},
		WorkDir:  t.TempDir(),
	})
	require.NoError(t, err)
	defer p.Abort()

	assert.Equal(t, isolation.RuntimeAxis("container"), gotAxes.Runtime, "the agent's runtime axis drives the chain")
	assert.Equal(t, isolation.WorkspaceAxis("worktree"), gotAxes.Workspace, "the project workspace default is the session trait")
}

// TestPrepareAgentChat_WorkspaceOverridesProjectDefault is GAP 2's final
// hop: agent_run's per-call req.Workspace ("worktree") OVERRIDES the
// project's cfg.GetWorkspace() default ("none" — shared checkout) on the SAME
// axes Resolve/chainFor uses everywhere else (isolation_test.go's
// TestResolve_DefaultsAndDegrades already pins that {worktree, host} always
// selects the Worktree policy — a REAL, isolated git worktree distinct from
// the shared project dir, never re-derived here).
func TestPrepareAgentChat_WorkspaceOverridesProjectDefault(t *testing.T) {
	resetStrictness(t)
	var gotAxes isolation.Axes
	prev := prepareIsolation
	prepareIsolation = func(_ context.Context, axes isolation.Axes, _ string, _ isolation.ImageConfig, projectDir, _ string, _ isolation.SessionState) (isolation.Policy, isolation.Workspace) {
		gotAxes = axes
		return stubPolicy{mk: func() pb.Client { return &stubClient{} }}, stubWorkspace{dir: projectDir}
	}
	t.Cleanup(func() { prepareIsolation = prev })

	cfg := config.NewFixture(config.Fixture{Workspace: "none"}) // project default: the shared live checkout
	p, err := PrepareAgentChat(context.Background(), cfg, AgentChatRequest{
		Resolved:  &ResolvedAgent{Name: "coder", Backend: "mock", Label: "fast", Runtime: "host"},
		WorkDir:   t.TempDir(),
		Workspace: "worktree", // the agent_run caller's per-call override
	})
	require.NoError(t, err)
	defer p.Abort()

	assert.Equal(t, isolation.WorkspaceAxis("worktree"), gotAxes.Workspace, "the caller's per-call workspace wins over cfg.GetWorkspace()")
}

// TestPrepareAgentChat_EmptyWorkspaceFallsBackToProjectDefault pins the
// other half: an agent_run call that never sets workspace changes nothing —
// cfg.GetWorkspace() still drives the axes exactly like before GAP 2.
func TestPrepareAgentChat_EmptyWorkspaceFallsBackToProjectDefault(t *testing.T) {
	resetStrictness(t)
	var gotAxes isolation.Axes
	prev := prepareIsolation
	prepareIsolation = func(_ context.Context, axes isolation.Axes, _ string, _ isolation.ImageConfig, projectDir, _ string, _ isolation.SessionState) (isolation.Policy, isolation.Workspace) {
		gotAxes = axes
		return stubPolicy{mk: func() pb.Client { return &stubClient{} }}, stubWorkspace{dir: projectDir}
	}
	t.Cleanup(func() { prepareIsolation = prev })

	cfg := config.NewFixture(config.Fixture{Workspace: "worktree"})
	p, err := PrepareAgentChat(context.Background(), cfg, AgentChatRequest{
		Resolved: &ResolvedAgent{Name: "coder", Backend: "mock", Label: "fast", Runtime: "host"},
		WorkDir:  t.TempDir(),
		// Workspace left empty: no per-call override supplied.
	})
	require.NoError(t, err)
	defer p.Abort()

	assert.Equal(t, isolation.WorkspaceAxis("worktree"), gotAxes.Workspace, "an empty override changes nothing — cfg.GetWorkspace() still decides")
}

// TestPrepareAgentChat_DelegatedDefaultsToWorktree pins the DELEGATED-CHILD
// default flip: when NEITHER the agent_run caller (req.Workspace) NOR the
// project config (cfg.GetWorkspace()) says anything explicit, a delegated
// child now gets its OWN git worktree rather than inheriting the
// none/shared-checkout default `ctxloom run` still uses at the top level
// (internal/cli/run.go's runAxes, an entirely separate call site that never
// goes through PrepareAgentChat). This is a WORKSPACE-axis (file-level)
// change only — it says nothing about the engine's global config/credential
// store, which worktree isolation never touches (see EnvWorkspace's doc and
// the antigravity fail-loud gate in isolation.go for the load-bearing
// caveat: some engines honour a config-home env override and some do not).
func TestPrepareAgentChat_DelegatedDefaultsToWorktree(t *testing.T) {
	resetStrictness(t)
	var gotAxes isolation.Axes
	prev := prepareIsolation
	prepareIsolation = func(_ context.Context, axes isolation.Axes, _ string, _ isolation.ImageConfig, projectDir, _ string, _ isolation.SessionState) (isolation.Policy, isolation.Workspace) {
		gotAxes = axes
		return stubPolicy{mk: func() pb.Client { return &stubClient{} }}, stubWorkspace{dir: projectDir}
	}
	t.Cleanup(func() { prepareIsolation = prev })

	cfg := config.NewFixture(config.Fixture{}) // no project workspace: default config
	p, err := PrepareAgentChat(context.Background(), cfg, AgentChatRequest{
		Resolved: &ResolvedAgent{Name: "coder", Backend: "mock", Label: "fast", Runtime: "host"},
		WorkDir:  t.TempDir(),
		// Workspace left empty: no per-call override supplied.
	})
	require.NoError(t, err)
	defer p.Abort()

	assert.Equal(t, isolation.WorkspaceAxis("worktree"), gotAxes.Workspace,
		"a delegated child with no explicit workspace anywhere defaults to worktree, not the shared checkout")
}

// TestPrepareAgentChat_ExplicitNoneStillNone_ProjectConfig pins the opt-out:
// a project config that EXPLICITLY says `workspace: none` must still be
// honored for a delegated child — the new default only fills in when NOTHING
// explicit was said, never overrides an explicit choice.
func TestPrepareAgentChat_ExplicitNoneStillNone_ProjectConfig(t *testing.T) {
	resetStrictness(t)
	var gotAxes isolation.Axes
	prev := prepareIsolation
	prepareIsolation = func(_ context.Context, axes isolation.Axes, _ string, _ isolation.ImageConfig, projectDir, _ string, _ isolation.SessionState) (isolation.Policy, isolation.Workspace) {
		gotAxes = axes
		return stubPolicy{mk: func() pb.Client { return &stubClient{} }}, stubWorkspace{dir: projectDir}
	}
	t.Cleanup(func() { prepareIsolation = prev })

	cfg := config.NewFixture(config.Fixture{Workspace: "none"}) // explicit project opt-out
	p, err := PrepareAgentChat(context.Background(), cfg, AgentChatRequest{
		Resolved: &ResolvedAgent{Name: "coder", Backend: "mock", Label: "fast", Runtime: "host"},
		WorkDir:  t.TempDir(),
	})
	require.NoError(t, err)
	defer p.Abort()

	assert.Equal(t, isolation.WorkspaceAxis("none"), gotAxes.Workspace,
		"an explicit project-level `workspace: none` must still be honored for a delegated child")
}

// TestPrepareAgentChat_ExplicitNoneStillNone_CallerOverride pins the other
// opt-out lever: the agent_run caller passing Workspace: "none" explicitly
// must win, even when the project config has no opinion (would otherwise
// default to worktree per the flip above).
func TestPrepareAgentChat_ExplicitNoneStillNone_CallerOverride(t *testing.T) {
	resetStrictness(t)
	var gotAxes isolation.Axes
	prev := prepareIsolation
	prepareIsolation = func(_ context.Context, axes isolation.Axes, _ string, _ isolation.ImageConfig, projectDir, _ string, _ isolation.SessionState) (isolation.Policy, isolation.Workspace) {
		gotAxes = axes
		return stubPolicy{mk: func() pb.Client { return &stubClient{} }}, stubWorkspace{dir: projectDir}
	}
	t.Cleanup(func() { prepareIsolation = prev })

	cfg := config.NewFixture(config.Fixture{}) // no project default
	p, err := PrepareAgentChat(context.Background(), cfg, AgentChatRequest{
		Resolved:  &ResolvedAgent{Name: "coder", Backend: "mock", Label: "fast", Runtime: "host"},
		WorkDir:   t.TempDir(),
		Workspace: "none", // the agent_run caller's explicit opt-out
	})
	require.NoError(t, err)
	defer p.Abort()

	assert.Equal(t, isolation.WorkspaceAxis("none"), gotAxes.Workspace,
		"an explicit per-call Workspace: \"none\" must still be honored for a delegated child")
}

// TestPrepareAgentChat_ContainerDegradeGate pins fail-loud parity with the
// fan's member gate: an explicitly-requested container that can't start
// (ClassIsolation finding during Prepare) refuses the child in strict mode —
// never a silent host degrade — and proceeds on the degraded workspace only
// under degraded mode.
func TestPrepareAgentChat_ContainerDegradeGate(t *testing.T) {
	rs := &ResolvedAgent{Name: "builder", Backend: "mock", Label: "fast", Runtime: "container"}

	t.Run("strict: the child is refused with the finding text", func(t *testing.T) {
		resetStrictness(t)
		stubPrepareIsolation(t, map[string]bool{"builder": true}, func() pb.Client { return &stubClient{} })
		_, err := PrepareAgentChat(context.Background(), &config.Config{}, AgentChatRequest{
			Resolved: rs,
			WorkDir:  t.TempDir(),
		})
		require.Error(t, err)
		// "NOT sandboxed" comes from the FINDING's own message (prepareChain,
		// isolation.go), not the gate's wrapper text — isolationGateErr's
		// wrapper is deliberately neutral so it never misdescribes a
		// non-container ClassIsolation finding (e.g. grave-prize's worktree
		// credential-seed gate) using container-specific vocabulary.
		assert.Contains(t, err.Error(), "NOT sandboxed")
		assert.Contains(t, err.Error(), "container isolation was requested but could not start")
	})

	t.Run("degraded: the child proceeds on the degraded workspace", func(t *testing.T) {
		resetStrictness(t)
		strictness.SetDegraded(true)
		stubPrepareIsolation(t, map[string]bool{"builder": true}, func() pb.Client { return &stubClient{} })
		p, err := PrepareAgentChat(context.Background(), &config.Config{}, AgentChatRequest{
			Resolved: rs,
			WorkDir:  t.TempDir(),
		})
		require.NoError(t, err)
		p.Abort()
	})
}

// TestPrepareAgentChat_OneshotFallback pins the no-structured-chat path
// ("direct Execute for backends without ACP"): each turn runs as an
// independent oneshot through the fan's launch tail, with the agent's
// composed context as the lead fragment and the turn text as the prompt, and
// the output surfaces as an assistant entry + turn boundary.
//
// Backend is a deliberately UNREGISTERED name, not "antigravity": every real
// registered backend (claude-code/antigravity/codex/kiro/opencode/acp/mock)
// now implements agent.StructuredChat (antigravity's own bespoke prose driver
// landed in chat.go), so backends.Get(rs.Backend) returning nil — the
// PrepareAgentChat capability check's OTHER route to "!ok" — is the only way
// left to exercise this fallback with a real registry lookup. The test's own
// Factory bypasses real backend construction entirely for the actual
// execution, so the name only matters for that one capability check.
func TestPrepareAgentChat_OneshotFallback(t *testing.T) {
	resetStrictness(t)
	stub := &stubClient{echo: true}
	p, err := PrepareAgentChat(context.Background(), &config.Config{}, AgentChatRequest{
		Resolved:    &ResolvedAgent{Name: "w1", Backend: "no-structured-chat-backend", Label: "agy", Context: "CTX-LEAD"},
		WorkDir:     t.TempDir(),
		Permissions: agent.PermissionBypass,
		Factory:     func(string, string, int) (pb.Client, error) { return stub, nil },
	})
	require.NoError(t, err)

	launch, err := p.Start(context.Background())
	require.NoError(t, err)
	assert.True(t, launch.Oneshot, "an unregistered backend name has no structured chat — the oneshot fallback drives it")
	defer launch.Close()

	launch.In <- agent.ChatMessage{Text: "do the thing"}

	var contents []string
	sawComplete := false
	timeout := time.After(5 * time.Second)
	for !sawComplete {
		select {
		case ev := <-launch.Events:
			switch {
			case ev.Entry != nil:
				contents = append(contents, ev.Entry.Content)
			case ev.Complete != nil:
				sawComplete = true
			}
		case <-timeout:
			t.Fatal("no turn boundary from the oneshot fallback")
		}
	}
	require.NotEmpty(t, contents)
	assert.Equal(t, "do the thing", contents[0], "the turn text is the oneshot prompt")

	require.NotNil(t, stub.gotReq)
	require.Len(t, stub.gotReq.Fragments, 1)
	assert.Equal(t, "CTX-LEAD", stub.gotReq.Fragments[0].Content, "the composed context leads every oneshot turn")
	assert.Equal(t, pb.ExecutionMode_ONESHOT, stub.gotReq.Options.Mode)

	close(launch.In)
	for range launch.Events { // drain to stream end
	}
}

// claudeChatPrepareRequest builds an AgentChatRequest for a claude-code
// delegated child, with a stub factory so PrepareAgentChat never touches real
// isolation/plugin machinery — only resolveChatModel's gate is under test.
func claudeChatPrepareRequest(rs *ResolvedAgent) AgentChatRequest {
	return AgentChatRequest{
		Resolved: rs,
		WorkDir:  "/tmp",
		Factory:  func(string, string, int) (pb.Client, error) { return &stubClient{}, nil },
	}
}

// TestPrepareAgentChat_ClaudeModelTranslatesNickname pins item 1's alias
// translation: a claude-code child configured with a bare interactive
// nickname (the shape a user's saved `/model` default takes) resolves to the
// concrete id before the engine ever spawns — never the raw alias the ACP/API
// path rejects.
func TestPrepareAgentChat_ClaudeModelTranslatesNickname(t *testing.T) {
	resetStrictness(t)
	rs := &ResolvedAgent{Name: "coordinator", Backend: config.BackendClaudeCode, Label: "claude-code", Model: "fable"}
	p, err := PrepareAgentChat(context.Background(), &config.Config{}, claudeChatPrepareRequest(rs))
	require.NoError(t, err)
	defer p.Abort()
	assert.Equal(t, "claude-fable-5", rs.Model, "the nickname resolves to its concrete id in place")
	assert.Empty(t, strictness.All(), "a resolvable model records no finding")
}

// TestPrepareAgentChat_ClaudeModelPinnedConcretePassesThrough pins that an
// already-concrete pinned model is never rewritten by the translation step.
func TestPrepareAgentChat_ClaudeModelPinnedConcretePassesThrough(t *testing.T) {
	resetStrictness(t)
	rs := &ResolvedAgent{Name: "coordinator", Backend: config.BackendClaudeCode, Label: "claude-code", Model: "claude-opus-4-8"}
	p, err := PrepareAgentChat(context.Background(), &config.Config{}, claudeChatPrepareRequest(rs))
	require.NoError(t, err)
	defer p.Abort()
	assert.Equal(t, "claude-opus-4-8", rs.Model, "a pinned concrete model passes through untouched")
}

// TestPrepareAgentChat_ClaudeModelEmptyFailsLoud pins item 1's REQUIRED
// behavior: a delegated claude child with no resolvable model never launches
// and dies on an opaque ACP error — PrepareAgentChat refuses it up front, in
// strict mode, with a strictness finding whose fix-it names the agent and the
// llm label to pin. Degraded mode downgrades the refusal to a warning and
// lets the (still-unresolved) launch proceed — CLAUDE.md's "things may be
// broken, get me an agent" escape hatch.
func TestPrepareAgentChat_ClaudeModelEmptyFailsLoud(t *testing.T) {
	t.Run("strict: refused with a fix-it naming the agent and llm label", func(t *testing.T) {
		resetStrictness(t)
		rs := &ResolvedAgent{Name: "coordinator", Backend: config.BackendClaudeCode, Label: "claude-code", Model: ""}
		p, err := PrepareAgentChat(context.Background(), &config.Config{}, claudeChatPrepareRequest(rs))
		require.Error(t, err)
		assert.Nil(t, p)
		assert.Contains(t, err.Error(), "coordinator", "names the agent")
		assert.Contains(t, err.Error(), "claude-code", "names the llm label")
		assert.Contains(t, err.Error(), "pin model", "the fix-it names the remedy")

		findings := strictness.All()
		require.Len(t, findings, 1)
		assert.Equal(t, strictness.ClassConfig, findings[0].Class)
		assert.Contains(t, findings[0].FixIt, "coordinator")
		assert.Contains(t, findings[0].FixIt, "claude-code")
	})

	t.Run("degraded: launches anyway with a warning, no error", func(t *testing.T) {
		resetStrictness(t)
		strictness.SetDegraded(true)
		rs := &ResolvedAgent{Name: "coordinator", Backend: config.BackendClaudeCode, Label: "claude-code", Model: ""}
		p, err := PrepareAgentChat(context.Background(), &config.Config{}, claudeChatPrepareRequest(rs))
		require.NoError(t, err)
		defer p.Abort()
		assert.Empty(t, rs.Model, "degraded mode launches anyway rather than fabricating a model")
		assert.Empty(t, strictness.All(), "degraded mode records nothing (Fail is a no-op)")
	})
}

// TestPrepareAgentChat_NonClaudeModelUntouched pins that the model-resolution
// gate is claude-code specific: another StructuredChat backend's model (even
// empty) is never validated or rewritten — only claude's ACP path rejects an
// unresolved model this way.
func TestPrepareAgentChat_NonClaudeModelUntouched(t *testing.T) {
	resetStrictness(t)
	rs := &ResolvedAgent{Name: "w1", Backend: "mock", Label: "fast", Model: ""}
	p, err := PrepareAgentChat(context.Background(), &config.Config{}, claudeChatPrepareRequest(rs))
	require.NoError(t, err)
	defer p.Abort()
	assert.Empty(t, rs.Model)
	assert.Empty(t, strictness.All())
}

// ===== Dirty-parent-tree worktree spawn gate =====
//
// Worktree isolation runs `git worktree add --detach <ref>` — a checkout of
// COMMITTED state only. A delegated child spawned into a worktree while the
// parent's own project tree carries uncommitted changes would silently run
// against stale or missing content (this project's signature failure mode).
// These pin the hard-fail: refused in strict mode, downgraded to a warning
// under --degraded, and never triggered when the resolved axis isn't
// worktree at all (the "workspace: none" escape hatch the error promises).

// TestPrepareAgentChat_DirtyParentTree_RefusesWorktreeSpawn is the crux case:
// a delegated child that resolves to worktree isolation (here: the project's
// OWN silent default, cfg.Fixture{} with no explicit workspace anywhere) is
// refused when the parent tree is dirty. The error names the agent, lists
// the uncommitted paths, and states both ways forward (commit, or pass
// workspace: "none").
func TestPrepareAgentChat_DirtyParentTree_RefusesWorktreeSpawn(t *testing.T) {
	resetStrictness(t)
	fake := &git.Fake{
		Dirty:   map[string]bool{"/proj": true},
		Changes: []string{" M internal/foo.go", "?? internal/bar.go"},
	}
	cfg := config.NewFixture(config.Fixture{}) // no explicit workspace anywhere -> silently defaults to worktree
	p, err := PrepareAgentChat(context.Background(), cfg, AgentChatRequest{
		Resolved: &ResolvedAgent{Name: "coder", Backend: "mock", Label: "fast", Runtime: "host"},
		WorkDir:  "/proj",
		Git:      fake,
	})
	require.Error(t, err)
	assert.Nil(t, p)
	assert.Contains(t, err.Error(), "coder", "names the agent")
	assert.Contains(t, err.Error(), "/proj", "names the dirty tree")
	assert.Contains(t, err.Error(), "internal/foo.go", "lists the uncommitted path")
	assert.Contains(t, err.Error(), "internal/bar.go", "lists the uncommitted path")
	assert.Contains(t, err.Error(), "committed state only", "states WHY the child can't see it")
	assert.Contains(t, err.Error(), "commit", "states the first way forward")
	assert.Contains(t, err.Error(), `workspace: "none"`, "states the escape-hatch way forward")

	findings := strictness.All()
	require.Len(t, findings, 1, "records exactly one ClassIsolation finding")
	assert.Equal(t, strictness.ClassIsolation, findings[0].Class)
	assert.Contains(t, findings[0].FixIt, "commit")
	assert.Contains(t, findings[0].FixIt, `workspace: "none"`)
}

// TestPrepareAgentChat_DirtyParentTree_UntrackedOnlyStillRefuses pins the
// untracked-files rule: a NEW file git itself does not consider
// ignored/excluded ("?? " in porcelain) counts as dirty on its own, with no
// tracked modification present at all — it is exactly the content a
// worktree checkout would silently drop and a bare `git add` would pick up.
func TestPrepareAgentChat_DirtyParentTree_UntrackedOnlyStillRefuses(t *testing.T) {
	resetStrictness(t)
	fake := &git.Fake{
		Dirty:   map[string]bool{"/proj": true},
		Changes: []string{"?? internal/newthing.go"},
	}
	cfg := config.NewFixture(config.Fixture{Workspace: "worktree"})
	_, err := PrepareAgentChat(context.Background(), cfg, AgentChatRequest{
		Resolved: &ResolvedAgent{Name: "coder", Backend: "mock", Label: "fast", Runtime: "host"},
		WorkDir:  "/proj",
		Git:      fake,
	})
	require.Error(t, err, "an untracked-but-not-ignored file alone must still refuse the spawn")
	assert.Contains(t, err.Error(), "internal/newthing.go")
}

// TestPrepareAgentChat_DirtyParentTree_BoundsFileList pins the listing
// bound: an agent worktree routinely carries dozens of modified
// delivered-surface files, and the refusal must print at most
// maxDirtyFilesListed of them plus a "+N more" tail rather than a wall of
// text.
func TestPrepareAgentChat_DirtyParentTree_BoundsFileList(t *testing.T) {
	resetStrictness(t)
	var changes []string
	for i := 0; i < 15; i++ {
		changes = append(changes, fmt.Sprintf(" M internal/file%02d.go", i))
	}
	fake := &git.Fake{
		Dirty:   map[string]bool{"/proj": true},
		Changes: changes,
	}
	cfg := config.NewFixture(config.Fixture{Workspace: "worktree"})
	_, err := PrepareAgentChat(context.Background(), cfg, AgentChatRequest{
		Resolved: &ResolvedAgent{Name: "coder", Backend: "mock", Label: "fast", Runtime: "host"},
		WorkDir:  "/proj",
		Git:      fake,
	})
	require.Error(t, err)
	for i := 0; i < maxDirtyFilesListed; i++ {
		assert.Contains(t, err.Error(), fmt.Sprintf("file%02d.go", i))
	}
	assert.NotContains(t, err.Error(), "file14.go", "the tail collapses past the bound")
	assert.Contains(t, err.Error(), "+5 more")
}

// TestPrepareAgentChat_DirtyParentTree_ExplicitNoneStillAllowed is the
// escape hatch the error message promises: a dirty parent tree never blocks
// a spawn that explicitly opts OUT of worktree isolation (workspace: none),
// because a shared-checkout child sees the live tree exactly as-is —
// dirtiness is irrelevant to it. Uses a git.Fake with no factory stub
// override (WorkDir won't be probed for isolation at all on the none axis;
// Dirty is still set true to prove the gate never even asks).
func TestPrepareAgentChat_DirtyParentTree_ExplicitNoneStillAllowed(t *testing.T) {
	resetStrictness(t)
	fake := &git.Fake{Dirty: map[string]bool{"/proj": true}}
	cfg := config.NewFixture(config.Fixture{})
	p, err := PrepareAgentChat(context.Background(), cfg, AgentChatRequest{
		Resolved:  &ResolvedAgent{Name: "coder", Backend: "mock", Label: "fast", Runtime: "host"},
		WorkDir:   "/proj",
		Workspace: "none", // the agent_run caller's explicit opt-out
		Git:       fake,
		Factory:   func(string, string, int) (pb.Client, error) { return &stubClient{}, nil },
	})
	require.NoError(t, err)
	defer p.Abort()
	assert.Empty(t, strictness.All(), "the none axis never even runs the dirty check")
}

// TestPrepareAgentChat_CleanParentTree_WorktreeAllowed is the negative
// control: a clean parent tree never trips the gate, even when the resolved
// axis IS worktree.
func TestPrepareAgentChat_CleanParentTree_WorktreeAllowed(t *testing.T) {
	resetStrictness(t)
	fake := &git.Fake{Dirty: map[string]bool{"/proj": false}}
	prev := prepareIsolation
	prepareIsolation = func(_ context.Context, axes isolation.Axes, _ string, _ isolation.ImageConfig, projectDir, _ string, _ isolation.SessionState) (isolation.Policy, isolation.Workspace) {
		return stubPolicy{mk: func() pb.Client { return &stubClient{} }}, stubWorkspace{dir: projectDir}
	}
	t.Cleanup(func() { prepareIsolation = prev })

	cfg := config.NewFixture(config.Fixture{Workspace: "worktree"})
	p, err := PrepareAgentChat(context.Background(), cfg, AgentChatRequest{
		Resolved: &ResolvedAgent{Name: "coder", Backend: "mock", Label: "fast", Runtime: "host"},
		WorkDir:  "/proj",
		Git:      fake,
	})
	require.NoError(t, err)
	defer p.Abort()
	assert.Empty(t, strictness.All())
}

// TestPrepareAgentChat_DirtyParentTree_DegradedAllowsWithWarning pins
// --degraded parity with the sibling ClassIsolation gates (chainFor,
// isolationGateErr, resolveChatModel): the finding still records (and
// streams as a warning), but the spawn proceeds on the worktree anyway.
func TestPrepareAgentChat_DirtyParentTree_DegradedAllowsWithWarning(t *testing.T) {
	resetStrictness(t)
	strictness.SetDegraded(true)
	fake := &git.Fake{
		Dirty:   map[string]bool{"/proj": true},
		Changes: []string{" M internal/foo.go"},
	}
	prev := prepareIsolation
	prepareIsolation = func(_ context.Context, axes isolation.Axes, _ string, _ isolation.ImageConfig, projectDir, _ string, _ isolation.SessionState) (isolation.Policy, isolation.Workspace) {
		return stubPolicy{mk: func() pb.Client { return &stubClient{} }}, stubWorkspace{dir: projectDir}
	}
	t.Cleanup(func() { prepareIsolation = prev })

	cfg := config.NewFixture(config.Fixture{Workspace: "worktree"})
	p, err := PrepareAgentChat(context.Background(), cfg, AgentChatRequest{
		Resolved: &ResolvedAgent{Name: "coder", Backend: "mock", Label: "fast", Runtime: "host"},
		WorkDir:  "/proj",
		Git:      fake,
	})
	require.NoError(t, err, "degraded mode downgrades the refusal to a warning")
	defer p.Abort()
	assert.Empty(t, strictness.All(), "degraded mode records nothing (Fail is a no-op in degraded mode)")
}
