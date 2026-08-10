package operations

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/afero"
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
		// non-container ClassIsolation finding (e.g. a worktree's
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

// overridingStubPolicy is stubPolicy plus the mcpCommandOverrider capability
// (isolation.Container's MCPCommandOverride method), so a test can drive
// PrepareAgentChat's policy-resolved override path without standing up a
// real container.
type overridingStubPolicy struct {
	stubPolicy
	override string
}

func (p overridingStubPolicy) MCPCommandOverride() string { return p.override }

// TestPrepareAgentChat_MCPCommandOverride_PatchesManagedEntry pins the
// coordinator-delegation fix: coord/spawner.go's childMCPServers composes
// plan.MCPServers at Resolve time — BEFORE Launch/StartEngine ever call
// PrepareAgentChat and resolve the real isolation.Policy — so the
// auto-registered ctxloom entry's Command was frozen at the HOST self-exec
// path no matter what the policy later turned out to be. A runtime:container
// child's structured chat therefore baked an unreachable host path into its
// own mcpServers. PrepareAgentChat must patch that ONE field once the policy
// resolves here — never recompose the whole set (that would re-fire the
// executable trust gate's WarnWithheld and violate the "resolved exactly
// once" invariant plan.MCPServers' own doc comment states).
func TestPrepareAgentChat_MCPCommandOverride_PatchesManagedEntry(t *testing.T) {
	resetStrictness(t)
	const containerBin = "/opt/ctxloom-in-container"
	prev := prepareIsolation
	prepareIsolation = func(_ context.Context, _ isolation.Axes, _ string, _ isolation.ImageConfig, projectDir, _ string, _ isolation.SessionState) (isolation.Policy, isolation.Workspace) {
		return overridingStubPolicy{override: containerBin}, stubWorkspace{dir: projectDir}
	}
	t.Cleanup(func() { prepareIsolation = prev })

	p, err := PrepareAgentChat(context.Background(), &config.Config{}, AgentChatRequest{
		Resolved: &ResolvedAgent{Name: "builder", Backend: "mock", Label: "fast", Runtime: "container"},
		WorkDir:  t.TempDir(),
		MCPServers: []agent.ChatMCPServer{
			{Name: agent.MCPServerName, Command: "/usr/local/bin/ctxloom", Args: agent.CtxloomMCPArgs},
			{Name: "other", Command: "/usr/local/bin/other-tool"},
		},
	})
	require.NoError(t, err)
	defer p.Abort()

	assert.Equal(t, containerBin, p.MCPCommandOverride(),
		"the resolved container policy's MCP command override must be exposed to callers (coord/spawner.go's StartEngine) that build their own server list outside Start")
	require.Len(t, p.req.MCPServers, 2)
	assert.Equal(t, containerBin, p.req.MCPServers[0].Command,
		"the auto-registered ctxloom entry must be patched to the in-container path once the policy resolves")
	assert.Equal(t, "/usr/local/bin/other-tool", p.req.MCPServers[1].Command,
		"a non-ctxloom managed entry's command must be left untouched")
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

// TestHandleDirtyParentTree_IsDirtyErrorIsInspected pins the second of the two
// production IsDirty call sites against the claim that a caller
// writing `dirty, _ := IsDirty(…)` would silently receive the UNSAFE zero
// value. No such caller exists: isolation's unsafeToRemove is fail-closed
// (TestWorktree_TeardownAbortsOnUnknownIgnoredContentState pins that), and
// this gate is deliberately best-effort — a git failure (no binary, a workDir
// that is not a repo, which several test doubles pass) must never block the
// spawn, matching how the isolation chain's own git checks degrade.
//
// What must NOT happen either way is the error going uninspected: this pins
// that an IsDirty error yields the no-op outcome BY DECISION, so a future
// `dirty, _ :=` — which would reach the same outcome by accident, and reach a
// wrong one the moment this gate's policy changes — is a visible change here.
func TestHandleDirtyParentTree_IsDirtyErrorIsInspected(t *testing.T) {
	resetStrictness(t)
	fake := &git.Fake{
		DirtyErr: assert.AnError,
		// Configured dirty AND with changes: were the error ignored, the
		// "fail" handler below would refuse the spawn instead of no-opping.
		Dirty:   map[string]bool{"/proj": true},
		Changes: []string{" M internal/foo.go"},
	}
	cfg := config.NewFixture(config.Fixture{})
	outcome, err := handleDirtyParentTree(context.Background(), cfg, fake, "/proj", "coder", DirtyTreeHandlerFail)
	require.NoError(t, err, "an unreadable dirty state degrades to a no-op gate; it must never block the spawn")
	assert.Equal(t, dirtyTreeOutcome{}, outcome, "and must not carry a snapshot built on state it could not read")
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
	cfg := config.NewFixture(config.Fixture{})
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
	cfg := config.NewFixture(config.Fixture{})
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
	cfg := config.NewFixture(config.Fixture{}) // copy needs no ack
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

// ackedFixture builds a *config.Config carrying a REAL, on-disk dirty-tree-
// commit acknowledgement — the ack no longer lives on config.Fixture itself
// (config-layer-scope: it moved to its own admission-store file outside the
// config chain entirely), so proving the "commit" handler's authorized path
// requires writing a genuine record via config.SetDirtyTreeCommitAck and
// injecting the SAME (fs, appDir) pair commitDirtyTree reads through
// (cfg.FS()/cfg.GetAppDir()) — constructing a Fixture alone can no longer
// grant it.
func ackedFixture(t *testing.T, f config.Fixture) *config.Config {
	t.Helper()
	if f.AppDir == "" {
		f.AppDir = "/proj/.ctxloom"
	}
	fs := afero.NewMemMapFs()
	require.NoError(t, config.SetDirtyTreeCommitAck(fs, f.AppDir, true))
	cfg := config.NewFixture(f)
	cfg.SetFS(fs)
	return cfg
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
	cfg := ackedFixture(t, config.Fixture{}) // even acknowledged, this must still refuse
	_, err := handleDirtyParentTree(context.Background(), cfg, fake, "/child-wt", "grandchild", DirtyTreeHandlerCommit)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "detached-HEAD")
	assert.Empty(t, fake.CommitMessages, "never even attempts the commit")
}

// TestHandleDirtyParentTree_Commit_CurrentBranchErrorRefuses pins the fix:
// before it, `branch, _ := gitClient.CurrentBranch(...)` discarded the
// error, leaving branch=="" — which is NOT "HEAD", so the detached-HEAD guard
// above never fired and the commit proceeded on an unresolvable branch name.
// An unresolvable branch is exactly the condition the guard exists to catch
// (the caller cannot tell whether this is a bare checkout), so it must refuse
// rather than silently treat the error as "not detached".
func TestHandleDirtyParentTree_Commit_CurrentBranchErrorRefuses(t *testing.T) {
	resetStrictness(t)
	fake := &git.Fake{
		Dirty:            map[string]bool{"/child-wt": true},
		Changes:          []string{" M f.go"},
		CurrentBranchErr: fmt.Errorf("git rev-parse: unknown revision or path not in the working tree"),
	}
	cfg := ackedFixture(t, config.Fixture{}) // even acknowledged, this must still refuse
	_, err := handleDirtyParentTree(context.Background(), cfg, fake, "/child-wt", "grandchild", DirtyTreeHandlerCommit)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "could not determine", "names the failure rather than silently guessing a branch")
	assert.Contains(t, err.Error(), "unknown revision", "carries the underlying git error")
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
	cfg := config.NewFixture(config.Fixture{}) // no ack recorded -> DirtyTreeCommitAcknowledged defaults false
	_, err := handleDirtyParentTree(context.Background(), cfg, fake, "/proj", "coder", DirtyTreeHandlerCommit)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "release/1.0", "names the branch it would commit to")
	assert.Contains(t, err.Error(), "internal/foo.go")
	assert.Contains(t, err.Error(), "internal/bar.go")
	assert.Contains(t, err.Error(), "committed state", "explains a worktree checkout's limit")
	assert.Contains(t, err.Error(), "ctxloom manage commit trust", "names the exact remedy")
	assert.Contains(t, err.Error(), "dirty_tree_commit_ack", "names the acknowledgement by its key name")
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
	cfg := ackedFixture(t, config.Fixture{})
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
	cfg := ackedFixture(t, config.Fixture{})
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
	cfg := ackedFixture(t, config.Fixture{})
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
	cfg := ackedFixture(t, config.Fixture{})
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

// hangingChatClient is a pb.Client whose Chat call never returns on its own —
// it blocks until the ctx it was given is cancelled, standing in for a
// legacy go-plugin dial that is stuck (a wedged subprocess, a container whose
// grpc connection never comes up). killed closes when Kill() is called, so a
// test can assert the stuck client was actually torn down rather than merely
// abandoned.
type hangingChatClient struct {
	killed   chan struct{}
	killOnce bool
}

func (h *hangingChatClient) Chat(ctx context.Context, _ agent.ChatRequest) (chan<- agent.ChatMessage, <-chan agent.ChatEvent, <-chan error, error) {
	<-ctx.Done()
	return nil, nil, nil, ctx.Err()
}
func (h *hangingChatClient) Info(context.Context) (*pb.LLMInfo, error) { return &pb.LLMInfo{}, nil }
func (h *hangingChatClient) Run(context.Context, *pb.RunStart, io.Reader, io.Writer, io.Writer, <-chan *pb.WindowSize) (int32, error) {
	return 0, nil
}
func (h *hangingChatClient) RunWithModelInfo(context.Context, *pb.RunStart, io.Reader, io.Writer, io.Writer, <-chan *pb.WindowSize) (*pb.RunResult, error) {
	return &pb.RunResult{}, nil
}
func (h *hangingChatClient) GetSession(context.Context, string) (*agent.Session, error) {
	return nil, nil
}
func (h *hangingChatClient) WatchSession(context.Context, string) (<-chan *pb.WatchEvent, <-chan error, error) {
	return nil, nil, nil
}
func (h *hangingChatClient) ListSessions(context.Context) ([]agent.SessionMeta, error) {
	return nil, nil
}
func (h *hangingChatClient) GetPlans(context.Context, string) ([]agent.PlanFile, error) {
	return nil, nil
}
func (h *hangingChatClient) Kill() {
	if !h.killOnce {
		h.killOnce = true
		close(h.killed)
	}
}

// TestPrepareAgentChat_Start_ChatDialDoesNotHangForever is the red/green pin
// for the legacy go-plugin Chat dial's missing bound (spawner.Launch /
// prep.Start's client.Chat call, internal/operations/delegate.go). Before the
// fix, Start blocked on client.Chat until its ctx was externally cancelled —
// there was no internal budget at all, so a wedged backend hung the launch
// indefinitely (confirmed red: Start did not return within a 2s watchdog
// window with no ChatDialTimeout seam to inject). Now Start races the dial
// against AgentChatRequest.ChatDialTimeout (defaultChatDialTimeout in
// production; injected short here so the test asserts the real behavior
// without waiting out the real 5-minute budget) and fails loud within it.
func TestPrepareAgentChat_Start_ChatDialDoesNotHangForever(t *testing.T) {
	resetStrictness(t)
	client := &hangingChatClient{killed: make(chan struct{})}
	const dialTimeout = 50 * time.Millisecond
	p, err := PrepareAgentChat(context.Background(), &config.Config{}, AgentChatRequest{
		Resolved:        &ResolvedAgent{Name: "builder", Backend: "mock", Label: "fast", Runtime: "host"},
		WorkDir:         t.TempDir(),
		Factory:         func(string, string, int) (pb.Client, error) { return client, nil },
		ChatDialTimeout: dialTimeout,
	})
	require.NoError(t, err)
	defer p.Abort()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel) // reaps client.Chat's own goroutine once dialChat gives up on it

	done := make(chan struct{})
	var startErr error
	start := time.Now()
	go func() {
		_, startErr = p.Start(ctx)
		close(done)
	}()

	select {
	case <-done:
		elapsed := time.Since(start)
		require.Error(t, startErr, "a chat dial that never completes must fail loud, not hang forever")
		assert.Contains(t, startErr.Error(), "mock", "the failure names the backend that never dialed")
		assert.Less(t, elapsed, 2*time.Second, "must fail within its injected budget, nowhere near the real 5m default")
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return within 2s despite a 50ms injected ChatDialTimeout — the bound is not being honored")
	}

	select {
	case <-client.killed:
	case <-time.After(2 * time.Second):
		t.Error("expected the wedged client to be Kill()ed (via reapLateChatDial) once its dial was declared failed")
	}
}

// TestApplyCopySnapshot_ReproducesUntrackedSymlink pins that "copy"
// reproduces an untracked SYMLINK, not just an untracked regular file.
// `git ls-files --others` lists symlinks exactly like regular
// files, so a parent tree whose uncommitted work includes a new symlink used
// to have that link dropped on the floor — no error, no warning, a worktree
// that silently differs from what the copy handler promised to reproduce.
// applyCopySnapshot's contract is "never half-applies and continues", and a
// skipped symlink is precisely a half-application.
func TestApplyCopySnapshot_ReproducesUntrackedSymlink(t *testing.T) {
	resetStrictness(t)
	parent := t.TempDir()
	target := t.TempDir()

	require.NoError(t, os.WriteFile(filepath.Join(parent, "real.go"), []byte("package real"), 0o644))
	require.NoError(t, os.Symlink("real.go", filepath.Join(parent, "link.go")))
	require.NoError(t, os.MkdirAll(filepath.Join(parent, "nested"), 0o755))
	require.NoError(t, os.Symlink("../real.go", filepath.Join(parent, "nested", "deep.go")))

	snap := &copySnapshot{
		untracked: []string{"real.go", "link.go", "nested/deep.go"},
		sourceDir: parent,
	}
	require.NoError(t, applyCopySnapshot(context.Background(), &git.Fake{}, target, snap))

	info, err := os.Lstat(filepath.Join(target, "link.go"))
	require.NoError(t, err, "the untracked symlink must exist in the worktree")
	assert.NotZero(t, info.Mode()&os.ModeSymlink, "and must still be a symlink, not a materialized copy of its target")
	dest, err := os.Readlink(filepath.Join(target, "link.go"))
	require.NoError(t, err)
	assert.Equal(t, "real.go", dest, "the link target is reproduced verbatim (never resolved to an absolute host path)")

	deep, err := os.Readlink(filepath.Join(target, "nested", "deep.go"))
	require.NoError(t, err, "a symlink under a subdirectory needs its parent dirs created too")
	assert.Equal(t, "../real.go", deep)
}

// TestApplyCopySnapshot_UnsupportedUntrackedEntryFailsLoud pins the other
// half of the symlink guard above: an untracked path that is NEITHER a
// regular file nor a symlink (a fifo, a socket, a device node) must REFUSE
// rather than be skipped in silence. The copy handler's whole promise is
// that the child's worktree reproduces the parent's uncommitted state;
// anything it cannot reproduce is a fact the caller is entitled to.
func TestApplyCopySnapshot_UnsupportedUntrackedEntryFailsLoud(t *testing.T) {
	resetStrictness(t)
	parent := t.TempDir()
	target := t.TempDir()
	fifo := filepath.Join(parent, "pipe")
	if out, err := exec.Command("mkfifo", fifo).CombinedOutput(); err != nil {
		t.Skipf("mkfifo unavailable: %v (%s)", err, out)
	}

	snap := &copySnapshot{untracked: []string{"pipe"}, sourceDir: parent}
	err := applyCopySnapshot(context.Background(), &git.Fake{}, target, snap)
	require.Error(t, err, "an unreproducible entry must fail loud, never be skipped silently")
	assert.Contains(t, err.Error(), "pipe", "the refusal names the path it could not reproduce")
}

// TestStartOneshot_CloseTerminatesTheDriverGoroutine pins the real half of
// the fix: AgentChatLaunch.Close is the orchestrator's "this child is over"
// lever (coord.terminateRun calls it for agent_stop, launch failure and
// every other terminal cause), and on the oneshot fallback it used to only
// cancel the per-turn context — the driver goroutine stayed parked on its
// input channel forever, so Events/Errs never closed and the coordinator's
// driveChild select stayed alive until the whole coordinator shut down.
//
// The claimed DEADLOCK (endChild draining Errs before closing In) is a
// DIFFERENT proposition and is refuted by construction: endChild is only
// reached when Events closes, and on this path Events closing already
// implies In was closed. What was real is the parked goroutine, which is
// what this pins — Close alone must wind the launch down.
func TestStartOneshot_CloseTerminatesTheDriverGoroutine(t *testing.T) {
	resetStrictness(t)
	p := &PreparedAgentChat{
		cfg:     config.NewFixture(config.Fixture{}),
		oneshot: true,
		req: AgentChatRequest{
			Resolved: &ResolvedAgent{Name: "coder", Backend: "mock", Label: "fast"},
			WorkDir:  t.TempDir(),
		},
	}
	launch := p.startOneshot(context.Background())
	require.True(t, launch.Oneshot)

	launch.Close()

	select {
	case _, ok := <-launch.Events:
		assert.False(t, ok, "Events must be CLOSED, not carrying a late event")
	case <-time.After(2 * time.Second):
		t.Fatal("Events never closed after Close() — the oneshot driver goroutine is parked forever")
	}
	select {
	case _, ok := <-launch.Errs:
		assert.False(t, ok, "Errs must be CLOSED")
	case <-time.After(2 * time.Second):
		t.Fatal("Errs never closed after Close()")
	}
}

// TestStartOneshot_ClosingInStillTerminates keeps the pre-existing wind-down
// route working: the orchestrator (coord.endChild) closes In as its own
// last act, and that must still close Events and Errs. Close() gaining its
// own exit must not become the ONLY way out — and, critically, Close must
// never itself close In, because endChild closes In too and a second close
// would panic.
func TestStartOneshot_ClosingInStillTerminates(t *testing.T) {
	resetStrictness(t)
	p := &PreparedAgentChat{
		cfg:     config.NewFixture(config.Fixture{}),
		oneshot: true,
		req: AgentChatRequest{
			Resolved: &ResolvedAgent{Name: "coder", Backend: "mock", Label: "fast"},
			WorkDir:  t.TempDir(),
		},
	}
	launch := p.startOneshot(context.Background())

	in := launch.In
	close(in)
	launch.Close() // the orchestrator's real order: close In, THEN Close — must not double-close

	select {
	case _, ok := <-launch.Events:
		assert.False(t, ok)
	case <-time.After(2 * time.Second):
		t.Fatal("Events never closed after In was closed")
	}
}

// TestStartEngine_FactoryWithoutStarterRefusesInsteadOfPanicking pins the
// nil-starter refusal. The two spawn seams are independent — Factory fakes
// the legacy go-plugin Chat dial, Starter fakes the StartRun runner launch
// — and a non-nil Factory skips the whole isolation block that would
// otherwise BIND a production starter. A caller that supplies only Factory
// and then takes
// the StartRun path therefore reached StartEngine with p.starter == nil and
// called it: a nil-func panic, from an exported method, naming nothing.
//
// (The register's stated mechanism — "p.starter is assigned only inside the
// p.factory == nil branch" — no longer holds: req.Starter is copied across
// unconditionally. The reachable defect is the unset case pinned here.)
func TestStartEngine_FactoryWithoutStarterRefusesInsteadOfPanicking(t *testing.T) {
	resetStrictness(t)
	p, err := PrepareAgentChat(context.Background(), config.NewFixture(config.Fixture{}), AgentChatRequest{
		Resolved: &ResolvedAgent{Name: "coder", Backend: "mock", Label: "fast", Runtime: "host"},
		WorkDir:  t.TempDir(),
		Factory:  func(string, string, int) (pb.Client, error) { return &stubClient{}, nil },
	})
	require.NoError(t, err)
	require.Nil(t, p.starter, "the Factory seam deliberately binds no production starter")

	proc, serr := p.StartEngine(context.Background())
	require.Error(t, serr, "StartEngine must refuse a launch it has no starter for, not panic")
	assert.Nil(t, proc)
	assert.Contains(t, serr.Error(), "Starter", "the refusal names the seam that was not supplied")
}

// TestPrepareAgentChat_Copy_OneshotRefusedEvenWithNothingCaptured pins a
// claim that turned out to be REFUTED. The claim was that the
// oneshot+"copy" refusal firing on an EMPTY capture turns a harmless spawn
// into an error, so it should be suppressed when there is nothing to
// reproduce. It should not:
//
//   - the refusal is about a CONFIGURATION the fallback path cannot honor
//     ("copy" needs a durable worktree; the oneshot fallback carves and tears
//     down a fresh one per turn), which is equally true whether this
//     particular parent tree had one dirty file or a thousand;
//   - suppressing it would make the same misconfiguration refuse or proceed
//     depending on the parent tree's momentary contents — the caller learns
//     about an unsatisfiable request only sometimes;
//   - and the "harmless" premise needs a tree git reports DIRTY while both
//     `git diff HEAD` and `git ls-files --others --exclude-standard` come
//     back empty, which real git does not produce for any ordinary edit.
//
// This pin goes red the moment the refusal is made conditional on the
// snapshot's contents.
func TestPrepareAgentChat_Copy_OneshotRefusedEvenWithNothingCaptured(t *testing.T) {
	resetStrictness(t)
	fake := &git.Fake{
		Dirty:   map[string]bool{"/proj": true},
		Changes: []string{" M f.go"},
		// The row's premise, made explicit: nothing at all is captured.
		DiffPatchValue: "",
		UntrackedList:  nil,
	}
	cfg := config.NewFixture(config.Fixture{})
	p, err := PrepareAgentChat(context.Background(), cfg, AgentChatRequest{
		Resolved:         &ResolvedAgent{Name: "coder", Backend: "no-structured-chat-backend", Label: "fast"},
		WorkDir:          "/proj",
		DirtyTreeHandler: DirtyTreeHandlerCopy,
		Git:              fake,
	})
	require.Error(t, err, "an unsatisfiable copy request is refused on the configuration, not on the snapshot's contents")
	assert.Nil(t, p)
	assert.Contains(t, err.Error(), `dirty_tree_handler "copy"`)
	assert.Contains(t, err.Error(), "stale", "and names the handlers that DO work for this backend")
}

// TestHandleDirtyParentTree_Commit_ListingFailureIsNamedInThePreview pins a
// defect: the "commit" handler prints a preview naming the files it is
// about to commit on the user's branch, and boundDirtyChanges turned a
// WorkingChanges FAILURE into an empty list — so the preview immediately
// before an auto-commit of the user's tree said, with no qualification, that
// there was nothing to name. "I could not find out" must not render as the
// empty set in the one message whose job is to show what is about to be
// committed.
func TestHandleDirtyParentTree_Commit_ListingFailureIsNamedInThePreview(t *testing.T) {
	resetStrictness(t)
	warnings := captureWarnings(t)
	fake := &git.Fake{
		Dirty:            map[string]bool{"/proj": true},
		ChangesErr:       assert.AnError,
		CommitAllChanged: []string{"internal/foo.go"},
	}
	cfg := ackedFixture(t, config.Fixture{})
	_, err := handleDirtyParentTree(context.Background(), cfg, fake, "/proj", "coder", DirtyTreeHandlerCommit)
	require.NoError(t, err, "a listing failure stays best-effort: it must not block the configured commit")
	assert.Contains(t, warnings.String(), "could not list",
		"the preview must SAY the file listing failed rather than showing an empty list")
}

// TestHandleDirtyParentTree_Fail_ListingFailureIsNamedInTheRefusal is the
// same defect on the refusal path: "fail" exists to tell the caller
// WHICH uncommitted paths the child would not see, and a swallowed listing
// error made it name none of them while asserting they exist.
func TestHandleDirtyParentTree_Fail_ListingFailureIsNamedInTheRefusal(t *testing.T) {
	resetStrictness(t)
	fake := &git.Fake{Dirty: map[string]bool{"/proj": true}, ChangesErr: assert.AnError}
	cfg := config.NewFixture(config.Fixture{})
	_, err := handleDirtyParentTree(context.Background(), cfg, fake, "/proj", "coder", DirtyTreeHandlerFail)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "could not list",
		"the refusal must distinguish an unreadable listing from an empty one")
}

// TestHandleDirtyParentTree_Stale_ListingFailureIsNamedInTheWarning covers
// the third message: "stale" promises to list what the child will
// NOT see.
func TestHandleDirtyParentTree_Stale_ListingFailureIsNamedInTheWarning(t *testing.T) {
	resetStrictness(t)
	warnings := captureWarnings(t)
	fake := &git.Fake{Dirty: map[string]bool{"/proj": true}, ChangesErr: assert.AnError}
	cfg := config.NewFixture(config.Fixture{})
	_, err := handleDirtyParentTree(context.Background(), cfg, fake, "/proj", "coder", DirtyTreeHandlerStale)
	require.NoError(t, err)
	assert.Contains(t, warnings.String(), "could not list")
}

// reportingWorkspace mirrors the production Workspace contract every policy
// implements: a teardown failure is REPORTED from inside Cleanup (see
// isolation.warnCleanupResidue — it names the residue path, the likely cause
// and the manual fix) and only then, for the container policy, also returned.
type reportingWorkspace struct{ dir, residue string }

func (w reportingWorkspace) Dir() string { return w.dir }
func (w reportingWorkspace) Cleanup() error {
	clidiag.Warn("ctxloom", "container scratch %s could not be removed (%v)", w.residue, assert.AnError)
	return fmt.Errorf("remove container scratch %s: %w", w.residue, assert.AnError)
}

// TestPrepareAgentChat_AbortReportsTeardownFailureExactlyOnce pins a claim
// that turned out to be REFUTED. The claim — "both workspace teardowns discard
// ws.Cleanup()'s error, so a failed worktree removal is invisible" — has a
// true mechanism and a false consequence:
//
//   - isolation's worktreeWorkspace.Cleanup returns nil UNCONDITIONALLY. Every
//     failure it can have (an unremovable per-agent config-home/curated
//     HOME/scratch dir, a teardown it must abandon to protect nested WIP) is
//     already streamed from inside. There is no error to discard.
//   - containerWorkspace.Cleanup does return one, and its own doc settles the
//     question in tree: it warns via warnCleanupResidue AND returns, with
//     "callers discard the returned error by contract" (SD3).
//   - oneshot.go carries the matching decision block for why the caller must
//     NOT re-record it: this teardown runs outside the checkpoint→gate window,
//     so a finding raised here lands in an orphaned window nothing watches.
//
// So the report is not missing, and adding a caller-side one would print the
// same residue twice. This pins that: exactly one report per teardown.
func TestPrepareAgentChat_AbortReportsTeardownFailureExactlyOnce(t *testing.T) {
	resetStrictness(t)
	warnings := captureWarnings(t)
	const residue = "/scratch/ctxloom-abcd"
	prev := prepareIsolation
	prepareIsolation = func(_ context.Context, _ isolation.Axes, _ string, _ isolation.ImageConfig, projectDir, _ string, _ isolation.SessionState) (isolation.Policy, isolation.Workspace) {
		return stubPolicy{mk: func() pb.Client { return &stubClient{} }},
			reportingWorkspace{dir: projectDir, residue: residue}
	}
	t.Cleanup(func() { prepareIsolation = prev })

	p, err := PrepareAgentChat(context.Background(), config.NewFixture(config.Fixture{Workspace: "worktree"}), AgentChatRequest{
		Resolved: &ResolvedAgent{Name: "coder", Backend: "mock", Label: "fast", Runtime: "host"},
		WorkDir:  t.TempDir(),
	})
	require.NoError(t, err)
	p.Abort()

	assert.Equal(t, 1, strings.Count(warnings.String(), residue),
		"the workspace reports its own teardown failure; a caller-side report would say it twice")
	p.Abort() // idempotent: Abort clears its cleanup, so no second teardown either
	assert.Equal(t, 1, strings.Count(warnings.String(), residue))
}

// TestPrepareAgentChat_EmptyComposedContextIsAnnounced pins the zero-context
// floor. Both delegated launch paths funnel through PrepareAgentChat, and it
// resolved the lead context with no floor: req.Context, else rs.Context,
// else nothing at all. leadContextIn then prepends "" — the child runs with ZERO ctxloom
// bytes while agent_run reports a healthy spawn. That is this project's
// signature failure mode (exit 0, success message, nothing delivered), and
// the composed-context case is NOT covered by resolveAgentBinding's existing
// "declares no profiles" warning: here profiles ARE declared and assembly
// SUCCEEDED, producing nothing.
func TestPrepareAgentChat_EmptyComposedContextIsAnnounced(t *testing.T) {
	resetStrictness(t)
	warnings := captureWarnings(t)
	p, err := PrepareAgentChat(context.Background(), config.NewFixture(config.Fixture{}), AgentChatRequest{
		Resolved: &ResolvedAgent{Name: "coder", Backend: "mock", Label: "fast", Runtime: "host",
			Profiles: []string{"reviewer"}, Context: ""},
		WorkDir: t.TempDir(),
		Factory: func(string, string, int) (pb.Client, error) { return &stubClient{}, nil },
	})
	require.NoError(t, err, "an empty context stays fault-tolerant: it warns, it does not refuse")
	defer p.Abort()

	out := warnings.String()
	assert.Contains(t, out, "coder", "the warning names the agent")
	assert.Contains(t, out, "reviewer", "…and the profiles that composed to nothing")
}

// TestPrepareAgentChat_ComposedContextPresentIsSilent is the other half: the
// warning must fire on the EMPTY case only, or it is noise every spawn learns
// to ignore.
func TestPrepareAgentChat_ComposedContextPresentIsSilent(t *testing.T) {
	resetStrictness(t)
	warnings := captureWarnings(t)
	p, err := PrepareAgentChat(context.Background(), config.NewFixture(config.Fixture{}), AgentChatRequest{
		Resolved: &ResolvedAgent{Name: "coder", Backend: "mock", Label: "fast", Runtime: "host",
			Context: "# composed context\n"},
		WorkDir: t.TempDir(),
		Factory: func(string, string, int) (pb.Client, error) { return &stubClient{}, nil },
	})
	require.NoError(t, err)
	defer p.Abort()
	assert.NotContains(t, warnings.String(), "zero bytes")
}

// TestPrepareAgentChat_CallerContextCoversAnEmptyAgentContext pins that the
// floor reads what is actually DELIVERED: a resume primes req.Context with a
// rendered transcript, which is real ctxloom content even when the agent's
// own composed context is empty.
func TestPrepareAgentChat_CallerContextCoversAnEmptyAgentContext(t *testing.T) {
	resetStrictness(t)
	warnings := captureWarnings(t)
	p, err := PrepareAgentChat(context.Background(), config.NewFixture(config.Fixture{}), AgentChatRequest{
		Resolved: &ResolvedAgent{Name: "coder", Backend: "mock", Label: "fast", Runtime: "host"},
		Context:  "## resumed transcript\n",
		WorkDir:  t.TempDir(),
		Factory:  func(string, string, int) (pb.Client, error) { return &stubClient{}, nil },
	})
	require.NoError(t, err)
	defer p.Abort()
	assert.NotContains(t, warnings.String(), "zero bytes")
}

// TestPrepareAgentChat_BothLaunchPathsShareOneResolution pins a claim that
// turned out to be REFUTED. The claim is that AgentChatRequest and
// PreparedAgentChat are each "two objects" — a legacy-Chat prep and a
// StartRun prep — because a few fields (contextText, and the request's
// Context/MCPServers/ResumeSessionID/ChatDialTimeout) are read by only one of
// them.
//
// One shape, deliberately. Everything load-bearing is SHARED and resolved
// exactly once: the workspace axis and its dirty-parent-tree decision, the
// isolation prepare, the resolved (never-aliased) model, the MCP command
// override, the workspace env and the teardown. That is the point —
// coord/spawner.go composes both paths' request through ONE chatRequest
// helper whose doc states the reason ("so a field cannot land on one path and
// be forgotten on the other"), and each per-path field carries a doc saying
// which path reads it. Splitting the type would give the two launch paths two
// preps that can drift, reintroducing exactly what the shared shape prevents;
// and nothing is lost on the StartRun side, which receives the same composed
// context through runChildViaStartRun rather than through this field.
//
// So this pins the invariant instead: both launch halves report the SAME
// resolution. It goes red the moment a split lets one path resolve its own.
func TestPrepareAgentChat_BothLaunchPathsShareOneResolution(t *testing.T) {
	resetStrictness(t)
	workDir := t.TempDir()
	client := &fakeACPEngineClient{}
	started := false
	p, err := PrepareAgentChat(context.Background(), config.NewFixture(config.Fixture{}), AgentChatRequest{
		Resolved: &ResolvedAgent{Name: "coder", Backend: "mock", Label: "fast", Runtime: "host", Model: "resolved-model", Context: "lead"},
		WorkDir:  workDir,
		Env:      map[string]string{"CTXLOOM_SESSION_HARP": "ample-tidy-quail"},
		Factory:  func(string, string, int) (pb.Client, error) { return client, nil },
		Starter: func(context.Context) (*isolation.RunnerHandle, error) {
			started = true
			return &isolation.RunnerHandle{Name: "fake", Kill: func() {}}, nil
		},
	})
	require.NoError(t, err)

	launch, serr := p.Start(context.Background())
	require.NoError(t, serr)
	require.NotNil(t, launch)
	require.NotNil(t, client.gotReq, "the legacy dial must have opened")

	eng, eerr := p.StartEngine(context.Background())
	require.NoError(t, eerr)
	require.True(t, started, "the StartRun half must have launched its runner")

	assert.Equal(t, client.gotReq.WorkDir, eng.WorkDir, "both halves run the child in the SAME resolved workspace")
	assert.Equal(t, client.gotReq.Model, eng.Model, "…against the same resolved model")
	assert.Equal(t, "ample-tidy-quail", client.gotReq.Env["CTXLOOM_SESSION_HARP"], "…with the same ambient identity")
	assert.Equal(t, "ample-tidy-quail", eng.Env["CTXLOOM_SESSION_HARP"])
}
