package operations

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
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

	cfg := &config.Config{Workspace: "worktree"}
	p, err := PrepareAgentChat(context.Background(), cfg, AgentChatRequest{
		Resolved: &ResolvedAgent{Name: "builder", Backend: "mock", Label: "fast", Runtime: "container"},
		WorkDir:  t.TempDir(),
	})
	require.NoError(t, err)
	defer p.Abort()

	assert.Equal(t, isolation.RuntimeAxis("container"), gotAxes.Runtime, "the agent's runtime axis drives the chain")
	assert.Equal(t, isolation.WorkspaceAxis("worktree"), gotAxes.Workspace, "the project workspace default is the session trait")
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
		assert.Contains(t, err.Error(), "UNSANDBOXED")
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
func TestPrepareAgentChat_OneshotFallback(t *testing.T) {
	resetStrictness(t)
	stub := &stubClient{echo: true}
	p, err := PrepareAgentChat(context.Background(), &config.Config{}, AgentChatRequest{
		Resolved:    &ResolvedAgent{Name: "w1", Backend: "antigravity", Label: "agy", Context: "CTX-LEAD"},
		WorkDir:     t.TempDir(),
		Permissions: agent.PermissionBypass,
		Factory:     func(string, string, int) (pb.Client, error) { return stub, nil },
	})
	require.NoError(t, err)

	launch, err := p.Start(context.Background())
	require.NoError(t, err)
	assert.True(t, launch.Oneshot, "antigravity has no structured chat — the oneshot fallback drives it")
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
