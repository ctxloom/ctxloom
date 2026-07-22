package operations

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/git"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/lm/isolation"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
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

// ===== Dirty-parent-tree spawn: handler dispatch =====
//
// Worktree isolation runs `git worktree add --detach <ref>` — a checkout of
// COMMITTED state only. A delegated child spawned into a worktree while the
// parent's own project tree carries uncommitted changes needs an explicit
// decision: commit, copy, proceed stale, or refuse (dirty_tree_handler).
// These pin each handler, the config/per-call precedence, that --degraded
// softens NONE of them, and (fail's original behavior) never triggering
// when the resolved axis isn't worktree at all.

// captureWarnings redirects clidiag's Warn/WarnOnce sink to a buffer for the
// test's duration, returning it so assertions can inspect the exact printed
// text (payload, not just "an error happened").
func captureWarnings(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	restore := clidiag.SetSink(&buf)
	t.Cleanup(restore)
	return &buf
}

// ----- fail -----

// TestHandleDirtyParentTree_Fail_RefusesAndNamesEverything is the crux case:
// dirty_tree_handler: "fail" (this gate's original, sole behavior before the
// other three existed) refuses the spawn, naming the agent, the dirty tree,
// the uncommitted paths, and both ways forward.
func TestHandleDirtyParentTree_Fail_RefusesAndNamesEverything(t *testing.T) {
	resetStrictness(t)
	fake := &git.Fake{
		Dirty:   map[string]bool{"/proj": true},
		Changes: []string{" M internal/foo.go", "?? internal/bar.go"},
	}
	cfg := config.NewFixture(config.Fixture{})
	_, err := handleDirtyParentTree(context.Background(), cfg, fake, "/proj", "coder", DirtyTreeHandlerFail)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "coder", "names the agent")
	assert.Contains(t, err.Error(), "/proj", "names the dirty tree")
	assert.Contains(t, err.Error(), "internal/foo.go", "lists the uncommitted path")
	assert.Contains(t, err.Error(), "internal/bar.go", "lists the uncommitted path")
	assert.Contains(t, err.Error(), "committed state only", "states WHY the child can't see it")
	assert.Contains(t, err.Error(), "commit", "states the first way forward")
	assert.Contains(t, err.Error(), `workspace: "none"`, "states the escape-hatch way forward")
}

// TestHandleDirtyParentTree_Fail_UntrackedOnlyStillRefuses pins the
// untracked-files rule: a NEW file git itself does not consider
// ignored/excluded ("?? " in porcelain) counts as dirty on its own.
func TestHandleDirtyParentTree_Fail_UntrackedOnlyStillRefuses(t *testing.T) {
	resetStrictness(t)
	fake := &git.Fake{
		Dirty:   map[string]bool{"/proj": true},
		Changes: []string{"?? internal/newthing.go"},
	}
	cfg := config.NewFixture(config.Fixture{})
	_, err := handleDirtyParentTree(context.Background(), cfg, fake, "/proj", "coder", DirtyTreeHandlerFail)
	require.Error(t, err, "an untracked-but-not-ignored file alone must still refuse the spawn")
	assert.Contains(t, err.Error(), "internal/newthing.go")
}

// TestHandleDirtyParentTree_Fail_BoundsFileList pins the listing bound: an
// agent worktree routinely carries dozens of modified delivered-surface
// files, and the refusal must print at most maxDirtyFilesListed of them plus
// a "+N more" tail rather than a wall of text.
func TestHandleDirtyParentTree_Fail_BoundsFileList(t *testing.T) {
	resetStrictness(t)
	var changes []string
	for i := 0; i < 15; i++ {
		changes = append(changes, fmt.Sprintf(" M internal/file%02d.go", i))
	}
	fake := &git.Fake{Dirty: map[string]bool{"/proj": true}, Changes: changes}
	cfg := config.NewFixture(config.Fixture{})
	_, err := handleDirtyParentTree(context.Background(), cfg, fake, "/proj", "coder", DirtyTreeHandlerFail)
	require.Error(t, err)
	for i := 0; i < maxDirtyFilesListed; i++ {
		assert.Contains(t, err.Error(), fmt.Sprintf("file%02d.go", i))
	}
	assert.NotContains(t, err.Error(), "file14.go", "the tail collapses past the bound")
	assert.Contains(t, err.Error(), "+5 more")
}

// TestHandleDirtyParentTree_Fail_UnaffectedByMissingAck proves fail needs no
// dirty_tree_commit_ack at all — that flag gates ONLY the commit handler.
func TestHandleDirtyParentTree_Fail_UnaffectedByMissingAck(t *testing.T) {
	resetStrictness(t)
	fake := &git.Fake{Dirty: map[string]bool{"/proj": true}, Changes: []string{" M f.go"}}
	cfg := config.NewFixture(config.Fixture{DirtyTreeCommitAck: false})
	_, err := handleDirtyParentTree(context.Background(), cfg, fake, "/proj", "coder", DirtyTreeHandlerFail)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "dirty_tree_commit_ack", "fail's refusal has nothing to do with the commit ack")
}

// TestPrepareAgentChat_DirtyParentTree_DegradedDoesNotSoftenFail is the
// direct proof that --degraded no longer softens this gate at all: before
// this change, --degraded downgraded the (then-only) refusal to a warning.
// Now the handler governs, and --degraded changes nothing about it.
func TestPrepareAgentChat_DirtyParentTree_DegradedDoesNotSoftenFail(t *testing.T) {
	resetStrictness(t)
	strictness.SetDegraded(true)
	fake := &git.Fake{Dirty: map[string]bool{"/proj": true}, Changes: []string{" M internal/foo.go"}}
	cfg := config.NewFixture(config.Fixture{Workspace: "worktree", DirtyTreeHandler: DirtyTreeHandlerFail})
	p, err := PrepareAgentChat(context.Background(), cfg, AgentChatRequest{
		Resolved: &ResolvedAgent{Name: "coder", Backend: "mock", Label: "fast", Runtime: "host"},
		WorkDir:  "/proj",
		Git:      fake,
	})
	require.Error(t, err, "--degraded must NOT soften the fail handler's refusal")
	assert.Nil(t, p)
}

// ----- workspace: none / clean tree escape hatches (unchanged shape) -----

// TestPrepareAgentChat_DirtyParentTree_ExplicitNoneStillAllowed is the
// escape hatch every handler's message names: a dirty parent tree never
// blocks a spawn that explicitly opts OUT of worktree isolation, because a
// shared-checkout child sees the live tree exactly as-is — dirtiness is
// irrelevant to it, and the dirty-tree handler never even runs.
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
	assert.Empty(t, fake.Calls, "the none axis never even probes commit-related git operations")
}

// TestPrepareAgentChat_CleanParentTree_WorktreeAllowed is the negative
// control: a clean parent tree never trips any handler, even when the
// resolved axis IS worktree.
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

// ----- stale -----

// TestHandleDirtyParentTree_Stale_ProceedsAndWarns pins the "stale" handler:
// it proceeds (nil error — no refusal, no mutation) and warns, naming the
// listed changes and both alternatives.
func TestHandleDirtyParentTree_Stale_ProceedsAndWarns(t *testing.T) {
	resetStrictness(t)
	buf := captureWarnings(t)
	fake := &git.Fake{Dirty: map[string]bool{"/proj": true}, Changes: []string{" M internal/foo.go"}}
	cfg := config.NewFixture(config.Fixture{})
	outcome, err := handleDirtyParentTree(context.Background(), cfg, fake, "/proj", "coder", DirtyTreeHandlerStale)
	require.NoError(t, err)
	assert.Nil(t, outcome.copy)
	warned := buf.String()
	assert.Contains(t, warned, "coder")
	assert.Contains(t, warned, "internal/foo.go")
	assert.Contains(t, warned, `dirty_tree_handler: "stale"`)
	assert.Contains(t, warned, "will NOT see these changes")
	assert.Contains(t, warned, `"commit" or "copy"`)
	assert.Contains(t, warned, `workspace: "none"`)
	assert.Empty(t, fake.Calls, "stale never mutates or applies anything")
}

// TestHandleDirtyParentTree_Stale_UnaffectedByMissingAck proves stale needs
// no dirty_tree_commit_ack — that flag gates ONLY the commit handler.
func TestHandleDirtyParentTree_Stale_UnaffectedByMissingAck(t *testing.T) {
	resetStrictness(t)
	captureWarnings(t)
	fake := &git.Fake{Dirty: map[string]bool{"/proj": true}, Changes: []string{" M f.go"}}
	cfg := config.NewFixture(config.Fixture{DirtyTreeCommitAck: false})
	_, err := handleDirtyParentTree(context.Background(), cfg, fake, "/proj", "coder", DirtyTreeHandlerStale)
	require.NoError(t, err)
}

// ----- copy -----

// TestHandleDirtyParentTree_Copy_CapturesPatchAndUntrackedList pins the
// capture half: "copy" reads the tracked patch and the untracked file list
// from the PARENT once, at decision time, deferring application until the
// worktree exists — it never mutates anything itself.
func TestHandleDirtyParentTree_Copy_CapturesPatchAndUntrackedList(t *testing.T) {
	resetStrictness(t)
	fake := &git.Fake{
		Dirty:          map[string]bool{"/proj": true},
		Changes:        []string{" M tracked.go", "?? untracked.go"},
		DiffPatchValue: "--- a/tracked.go\n+++ b/tracked.go\n@@ -1 +1 @@\n-old\n+new\n",
		UntrackedList:  []string{"untracked.go", "nested/other.go"},
	}
	cfg := config.NewFixture(config.Fixture{DirtyTreeCommitAck: false}) // copy needs no ack
	outcome, err := handleDirtyParentTree(context.Background(), cfg, fake, "/proj", "coder", DirtyTreeHandlerCopy)
	require.NoError(t, err)
	require.NotNil(t, outcome.copy)
	assert.Equal(t, fake.DiffPatchValue, outcome.copy.patch)
	assert.Equal(t, []string{"untracked.go", "nested/other.go"}, outcome.copy.untracked)
	assert.Equal(t, "/proj", outcome.copy.sourceDir)
	assert.Empty(t, fake.AppliedPatches, "capture never applies — that's applyCopySnapshot's job, run later against the worktree")
}

// TestPrepareAgentChat_Copy_AppliesPatchAndCopiesUntrackedIntoWorktree is the
// end-to-end proof: BOTH tracked (via ApplyPatch, asserted on the exact
// patch text and target dir) AND untracked (via a REAL byte-for-byte
// filesystem copy, asserted on actual file content — this half never
// touches git.Fake at all) land in the worktree.
func TestPrepareAgentChat_Copy_AppliesPatchAndCopiesUntrackedIntoWorktree(t *testing.T) {
	resetStrictness(t)
	parent := t.TempDir()
	target := t.TempDir() // a REAL, separate directory standing in for the created worktree

	require.NoError(t, os.MkdirAll(filepath.Join(parent, "nested"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(parent, "untracked.go"), []byte("package untracked"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(parent, "nested", "other.go"), []byte("package nested"), 0o644))

	fake := &git.Fake{
		Dirty:          map[string]bool{parent: true},
		Changes:        []string{" M tracked.go", "?? untracked.go", "?? nested/other.go"},
		DiffPatchValue: "FAKE-PATCH-CONTENT",
		UntrackedList:  []string{"untracked.go", "nested/other.go"},
	}
	prev := prepareIsolation
	prepareIsolation = func(_ context.Context, axes isolation.Axes, _ string, _ isolation.ImageConfig, projectDir, _ string, _ isolation.SessionState) (isolation.Policy, isolation.Workspace) {
		return stubPolicy{mk: func() pb.Client { return &stubClient{} }}, stubWorkspace{dir: target}
	}
	t.Cleanup(func() { prepareIsolation = prev })

	cfg := config.NewFixture(config.Fixture{})
	p, err := PrepareAgentChat(context.Background(), cfg, AgentChatRequest{
		Resolved:         &ResolvedAgent{Name: "coder", Backend: "mock", Label: "fast", Runtime: "host"},
		WorkDir:          parent,
		DirtyTreeHandler: DirtyTreeHandlerCopy,
		Git:              fake,
	})
	require.NoError(t, err)
	defer p.Abort()

	require.Len(t, fake.AppliedPatches, 1, "the tracked patch was applied exactly once")
	assert.Equal(t, "FAKE-PATCH-CONTENT", fake.AppliedPatches[0])
	require.Contains(t, fake.Calls, fmt.Sprintf("apply-patch %s", target), "applied INTO the worktree, never the parent")

	gotUntracked, err := os.ReadFile(filepath.Join(target, "untracked.go"))
	require.NoError(t, err)
	assert.Equal(t, "package untracked", string(gotUntracked), "untracked file reproduced byte-for-byte")
	gotNested, err := os.ReadFile(filepath.Join(target, "nested", "other.go"))
	require.NoError(t, err)
	assert.Equal(t, "package nested", string(gotNested), "nested untracked file reproduced, parent dirs created")

	_, err = os.ReadFile(filepath.Join(parent, "untracked.go"))
	require.NoError(t, err, "the PARENT's own copy is untouched — copy only ever reads it")
}

// TestPrepareAgentChat_Copy_ApplyPatchFailureFailsLoud pins "FAIL LOUDLY; do
// not half-apply and continue": an ApplyPatch error refuses the whole spawn.
func TestPrepareAgentChat_Copy_ApplyPatchFailureFailsLoud(t *testing.T) {
	resetStrictness(t)
	parent := t.TempDir()
	target := t.TempDir()
	fake := &git.Fake{
		Dirty:          map[string]bool{parent: true},
		Changes:        []string{" M tracked.go"},
		DiffPatchValue: "FAKE-PATCH",
		ApplyPatchErr:  fmt.Errorf("patch does not apply"),
	}
	prev := prepareIsolation
	prepareIsolation = func(_ context.Context, axes isolation.Axes, _ string, _ isolation.ImageConfig, projectDir, _ string, _ isolation.SessionState) (isolation.Policy, isolation.Workspace) {
		return stubPolicy{mk: func() pb.Client { return &stubClient{} }}, stubWorkspace{dir: target}
	}
	t.Cleanup(func() { prepareIsolation = prev })

	cfg := config.NewFixture(config.Fixture{})
	p, err := PrepareAgentChat(context.Background(), cfg, AgentChatRequest{
		Resolved:         &ResolvedAgent{Name: "coder", Backend: "mock", Label: "fast", Runtime: "host"},
		WorkDir:          parent,
		DirtyTreeHandler: DirtyTreeHandlerCopy,
		Git:              fake,
	})
	require.Error(t, err)
	assert.Nil(t, p)
	assert.Contains(t, err.Error(), "patch does not apply")
}

// TestPrepareAgentChat_Copy_UntrackedFileMissingFailsLoud pins the same
// no-half-apply contract for the untracked-file half: a file the snapshot
// named but that vanished before application refuses the whole spawn rather
// than silently reproducing a partial WIP set.
func TestPrepareAgentChat_Copy_UntrackedFileMissingFailsLoud(t *testing.T) {
	resetStrictness(t)
	parent := t.TempDir() // deliberately never write untracked.go here
	target := t.TempDir()
	fake := &git.Fake{
		Dirty:         map[string]bool{parent: true},
		Changes:       []string{"?? untracked.go"},
		UntrackedList: []string{"untracked.go"},
	}
	prev := prepareIsolation
	prepareIsolation = func(_ context.Context, axes isolation.Axes, _ string, _ isolation.ImageConfig, projectDir, _ string, _ isolation.SessionState) (isolation.Policy, isolation.Workspace) {
		return stubPolicy{mk: func() pb.Client { return &stubClient{} }}, stubWorkspace{dir: target}
	}
	t.Cleanup(func() { prepareIsolation = prev })

	cfg := config.NewFixture(config.Fixture{})
	p, err := PrepareAgentChat(context.Background(), cfg, AgentChatRequest{
		Resolved:         &ResolvedAgent{Name: "coder", Backend: "mock", Label: "fast", Runtime: "host"},
		WorkDir:          parent,
		DirtyTreeHandler: DirtyTreeHandlerCopy,
		Git:              fake,
	})
	require.Error(t, err)
	assert.Nil(t, p)
	assert.Contains(t, err.Error(), "untracked.go")
}

// TestPrepareAgentChat_Copy_OneshotFallbackRefused pins the documented
// oneshot-fallback limitation: "copy"'s one-time file reproduction has
// nowhere durable to land against a backend whose per-turn isolation
// prepares and tears down a fresh worktree every turn — refused loudly
// rather than silently reproducing into a worktree that won't outlive the
// turn.
func TestPrepareAgentChat_Copy_OneshotFallbackRefused(t *testing.T) {
	resetStrictness(t)
	fake := &git.Fake{Dirty: map[string]bool{"/proj": true}, Changes: []string{" M f.go"}}
	cfg := config.NewFixture(config.Fixture{})
	p, err := PrepareAgentChat(context.Background(), cfg, AgentChatRequest{
		Resolved:         &ResolvedAgent{Name: "coder", Backend: "no-structured-chat-backend", Label: "fast"},
		WorkDir:          "/proj",
		DirtyTreeHandler: DirtyTreeHandlerCopy,
		Git:              fake,
	})
	require.Error(t, err)
	assert.Nil(t, p)
	assert.Contains(t, err.Error(), `dirty_tree_handler "copy"`)
	assert.Contains(t, err.Error(), "coder")
}

// ----- commit -----

// TestHandleDirtyParentTree_Commit_DetachedHeadRefuses pins the grandchild-
// coherence guard: committing inside a detached-HEAD checkout (exactly what
// a delegated child's OWN worktree looks like) would land on no branch and
// could be silently discarded when that worktree is torn down.
func TestHandleDirtyParentTree_Commit_DetachedHeadRefuses(t *testing.T) {
	resetStrictness(t)
	fake := &git.Fake{
		Dirty:              map[string]bool{"/child-wt": true},
		Changes:            []string{" M f.go"},
		CurrentBranchValue: "HEAD", // git's own detached-HEAD sentinel
	}
	cfg := config.NewFixture(config.Fixture{DirtyTreeCommitAck: true}) // even acknowledged, this must still refuse
	_, err := handleDirtyParentTree(context.Background(), cfg, fake, "/child-wt", "grandchild", DirtyTreeHandlerCommit)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "detached-HEAD")
	assert.Empty(t, fake.CommitMessages, "never even attempts the commit")
}

// TestHandleDirtyParentTree_Commit_NoAckRefusesAndNamesKey is the first-time
// consent requirement: an absent project acknowledgement refuses the spawn
// (never commits), and the message is fully actionable — the branch, the
// bounded file list, the exact config key/file, and the alternatives.
func TestHandleDirtyParentTree_Commit_NoAckRefusesAndNamesKey(t *testing.T) {
	resetStrictness(t)
	fake := &git.Fake{
		Dirty:              map[string]bool{"/proj": true},
		Changes:            []string{" M internal/foo.go", "?? internal/bar.go"},
		CurrentBranchValue: "release/1.0",
	}
	cfg := config.NewFixture(config.Fixture{}) // DirtyTreeCommitAck defaults false
	_, err := handleDirtyParentTree(context.Background(), cfg, fake, "/proj", "coder", DirtyTreeHandlerCommit)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "release/1.0", "names the branch it would commit to")
	assert.Contains(t, err.Error(), "internal/foo.go")
	assert.Contains(t, err.Error(), "internal/bar.go")
	assert.Contains(t, err.Error(), "committed state", "explains a worktree checkout's limit")
	assert.Contains(t, err.Error(), ".ctxloom/config.yaml", "names the exact file")
	assert.Contains(t, err.Error(), "dirty_tree_commit_ack: true", "names the exact config key/value")
	assert.Contains(t, err.Error(), `"copy"`)
	assert.Contains(t, err.Error(), `"stale"`)
	assert.Contains(t, err.Error(), `"fail"`)
	assert.Empty(t, fake.CommitMessages, "no commit is attempted without the ack")
}

// TestHandleDirtyParentTree_Commit_PerCallHandlerCannotSupplyAck pins the
// boundary the ack is most likely to erode at: choosing "commit" via the
// per-call agent_run parameter selects the HANDLER, never the
// acknowledgement — with no project ack, it still refuses even though the
// caller explicitly asked for commit.
func TestHandleDirtyParentTree_Commit_PerCallHandlerCannotSupplyAck(t *testing.T) {
	resetStrictness(t)
	fake := &git.Fake{Dirty: map[string]bool{"/proj": true}, Changes: []string{" M f.go"}}
	cfg := config.NewFixture(config.Fixture{}) // project has NOT acknowledged
	// resolveDirtyTreeHandler is exactly what a per-call agent_run
	// dirty_tree_handler: "commit" resolves to — there is no field anywhere
	// in AgentChatRequest/agentRunInput that can also carry an ack.
	handler := resolveDirtyTreeHandler(cfg, DirtyTreeHandlerCommit)
	require.Equal(t, DirtyTreeHandlerCommit, handler)
	_, err := handleDirtyParentTree(context.Background(), cfg, fake, "/proj", "coder", handler)
	require.Error(t, err, "an explicit per-call request for \"commit\" still refuses without the project's own ack")
	assert.Contains(t, err.Error(), "dirty_tree_commit_ack")
}

// TestHandleDirtyParentTree_Commit_AckedWarnsAndCommits pins the
// authorized path: once the project has acknowledged, "commit" warns
// (naming the branch and the bounded file list) BEFORE mutating, then
// stages and commits everything (git add -A shape) with the documented
// message format, and verifies the commit actually captured content.
func TestHandleDirtyParentTree_Commit_AckedWarnsAndCommits(t *testing.T) {
	resetStrictness(t)
	buf := captureWarnings(t)
	fake := &git.Fake{
		Dirty:              map[string]bool{"/proj": true},
		Changes:            []string{" M internal/foo.go", "?? internal/bar.go"},
		CurrentBranchValue: "main",
		CommitAllSHA:       "abc123",
		CommitAllChanged:   []string{"internal/foo.go", "internal/bar.go"},
	}
	cfg := config.NewFixture(config.Fixture{DirtyTreeCommitAck: true})
	outcome, err := handleDirtyParentTree(context.Background(), cfg, fake, "/proj", "coder", DirtyTreeHandlerCommit)
	require.NoError(t, err)
	assert.Nil(t, outcome.copy)

	warned := buf.String()
	assert.Contains(t, warned, "main", "the warning names the branch")
	assert.Contains(t, warned, "internal/foo.go")
	assert.Contains(t, warned, "internal/bar.go")
	assert.Contains(t, warned, "coder")
	assert.Contains(t, warned, `dirty_tree_handler is configured to "commit"`)
	assert.Contains(t, warned, `"copy"`)
	assert.Contains(t, warned, `"stale"`)
	assert.Contains(t, warned, `"fail"`)

	require.Len(t, fake.CommitMessages, 1)
	msg := fake.CommitMessages[0]
	assert.Contains(t, msg, "ctxloom: auto-commit for delegated agent spawn")
	assert.Contains(t, msg, "coder", "names the delegated agent")
	assert.Contains(t, msg, "dirty_tree_handler=commit")
	assert.Contains(t, msg, `"copy"`)
	assert.Contains(t, msg, `"stale"`)
	assert.Contains(t, msg, `"fail"`)
	assert.Contains(t, fake.Calls, "commit-all /proj")
}

// TestHandleDirtyParentTree_Commit_EmptyCommitRefusesLoud pins the
// empty-commit safety net: CommitAll reporting success but an empty
// changed-files diff (this codebase's documented empty-commit pre-commit-
// hook history) must refuse rather than spawn the child against what may be
// nothing.
func TestHandleDirtyParentTree_Commit_EmptyCommitRefusesLoud(t *testing.T) {
	resetStrictness(t)
	captureWarnings(t)
	fake := &git.Fake{
		Dirty:              map[string]bool{"/proj": true},
		Changes:            []string{" M internal/foo.go"},
		CurrentBranchValue: "main",
		CommitAllSHA:       "deadbeef",
		CommitAllChanged:   nil, // the empty-commit case
	}
	cfg := config.NewFixture(config.Fixture{DirtyTreeCommitAck: true})
	_, err := handleDirtyParentTree(context.Background(), cfg, fake, "/proj", "coder", DirtyTreeHandlerCommit)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "deadbeef")
	assert.Contains(t, err.Error(), "empty")
	assert.Contains(t, err.Error(), "main")
}

// TestHandleDirtyParentTree_Commit_CommitAllErrorPropagates pins that a real
// git failure (not the empty-commit case — an actual error) surfaces
// verbatim rather than being swallowed.
func TestHandleDirtyParentTree_Commit_CommitAllErrorPropagates(t *testing.T) {
	resetStrictness(t)
	captureWarnings(t)
	fake := &git.Fake{
		Dirty:              map[string]bool{"/proj": true},
		Changes:            []string{" M internal/foo.go"},
		CurrentBranchValue: "main",
		CommitAllErr:       fmt.Errorf("index.lock exists"),
	}
	cfg := config.NewFixture(config.Fixture{DirtyTreeCommitAck: true})
	_, err := handleDirtyParentTree(context.Background(), cfg, fake, "/proj", "coder", DirtyTreeHandlerCommit)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "index.lock exists")
}

// TestPrepareAgentChat_Commit_ChildSeesCommittedContent is the full,
// REAL-git end-to-end proof that "commit" actually achieves its purpose: an
// uncommitted file on the parent's branch, once auto-committed, is visible
// to a FRESH worktree checked out from HEAD afterward — exactly what a
// delegated child's own worktree creation does next. Skips cleanly when git
// is unavailable.
func TestPrepareAgentChat_Commit_ChildSeesCommittedContent(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH; skipping real-git commit-handler integration test")
	}
	resetStrictness(t)
	captureWarnings(t)
	repo := initTestRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(repo, "wip.go"), []byte("package wip"), 0o644))

	real := git.NewExec()
	cfg := config.NewFixture(config.Fixture{DirtyTreeCommitAck: true})
	outcome, err := handleDirtyParentTree(context.Background(), cfg, real, repo, "coder", DirtyTreeHandlerCommit)
	require.NoError(t, err)
	assert.Nil(t, outcome.copy)

	dirty, err := real.IsDirty(context.Background(), repo)
	require.NoError(t, err)
	assert.False(t, dirty, "the parent tree is clean after the auto-commit")

	// Exactly what worktree isolation does next: a fresh checkout from HEAD.
	childWT := filepath.Join(t.TempDir(), "child-wt")
	require.NoError(t, real.WorktreeAdd(context.Background(), repo, childWT, "HEAD"))
	got, err := os.ReadFile(filepath.Join(childWT, "wip.go"))
	require.NoError(t, err, "the child's worktree sees the file — it is no longer parent-only WIP")
	assert.Equal(t, "package wip", string(got))
}

// ----- dirty_tree_handler precedence (config default / per-call override) -----

// TestResolveDirtyTreeHandler_Precedence pins the three-tier precedence
// (per-call > project config > built-in default), the identical shape
// Workspace's own GAP 2 resolution uses.
func TestResolveDirtyTreeHandler_Precedence(t *testing.T) {
	t.Run("per-call wins over project config", func(t *testing.T) {
		cfg := config.NewFixture(config.Fixture{DirtyTreeHandler: DirtyTreeHandlerFail})
		assert.Equal(t, DirtyTreeHandlerStale, resolveDirtyTreeHandler(cfg, DirtyTreeHandlerStale))
	})
	t.Run("empty per-call falls back to project config", func(t *testing.T) {
		cfg := config.NewFixture(config.Fixture{DirtyTreeHandler: DirtyTreeHandlerFail})
		assert.Equal(t, DirtyTreeHandlerFail, resolveDirtyTreeHandler(cfg, ""))
	})
	t.Run("both empty falls back to the built-in default (commit)", func(t *testing.T) {
		cfg := config.NewFixture(config.Fixture{})
		assert.Equal(t, "commit", resolveDirtyTreeHandler(cfg, ""))
		assert.Equal(t, defaultDirtyTreeHandler, resolveDirtyTreeHandler(cfg, ""))
	})
	t.Run("an unrecognized per-call value falls back to the default, not silently to something else", func(t *testing.T) {
		cfg := config.NewFixture(config.Fixture{DirtyTreeHandler: DirtyTreeHandlerFail})
		assert.Equal(t, defaultDirtyTreeHandler, resolveDirtyTreeHandler(cfg, "bogus-value-1"))
	})
	t.Run("an unrecognized project config value falls back to the default", func(t *testing.T) {
		cfg := config.NewFixture(config.Fixture{DirtyTreeHandler: "bogus-value-2"})
		assert.Equal(t, defaultDirtyTreeHandler, resolveDirtyTreeHandler(cfg, ""))
	})
}

// TestPrepareAgentChat_DirtyTreeHandler_PerCallOverridesProject proves the
// precedence at the PrepareAgentChat seam (not just the resolver in
// isolation): a project default of "fail" is overridden by a per-call
// "stale", so the spawn proceeds (with a warning) instead of refusing.
func TestPrepareAgentChat_DirtyTreeHandler_PerCallOverridesProject(t *testing.T) {
	resetStrictness(t)
	captureWarnings(t)
	fake := &git.Fake{Dirty: map[string]bool{"/proj": true}, Changes: []string{" M f.go"}}
	cfg := config.NewFixture(config.Fixture{Workspace: "worktree", DirtyTreeHandler: DirtyTreeHandlerFail})
	p, err := PrepareAgentChat(context.Background(), cfg, AgentChatRequest{
		Resolved:         &ResolvedAgent{Name: "coder", Backend: "no-structured-chat-backend", Label: "fast"},
		WorkDir:          "/proj",
		DirtyTreeHandler: DirtyTreeHandlerStale, // the agent_run caller's per-call override
		Git:              fake,
	})
	require.NoError(t, err, "the per-call override beats the project's \"fail\" default")
	defer p.Abort()
}

// TestPrepareAgentChat_DirtyTreeHandler_EmptyFallsBackToProjectDefault is
// the other half: an agent_run call that never sets dirty_tree_handler
// changes nothing — the project default still decides.
func TestPrepareAgentChat_DirtyTreeHandler_EmptyFallsBackToProjectDefault(t *testing.T) {
	resetStrictness(t)
	fake := &git.Fake{Dirty: map[string]bool{"/proj": true}, Changes: []string{" M f.go"}}
	cfg := config.NewFixture(config.Fixture{Workspace: "worktree", DirtyTreeHandler: DirtyTreeHandlerFail})
	p, err := PrepareAgentChat(context.Background(), cfg, AgentChatRequest{
		Resolved: &ResolvedAgent{Name: "coder", Backend: "mock", Label: "fast", Runtime: "host"},
		WorkDir:  "/proj",
		// DirtyTreeHandler left empty: no per-call override supplied.
		Git: fake,
	})
	require.Error(t, err, "the project's \"fail\" default still applies")
	assert.Nil(t, p)
}
