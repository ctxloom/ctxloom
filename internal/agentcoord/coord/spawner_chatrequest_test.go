package coord

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// Launch and StartEngine each built their own operations.AgentChatRequest
// literal, with different field subsets, so a field both paths need could be added
// to one and silently omitted from the other.
//
// The pin is at the PUBLIC SEAM above the duplication (Spawner.Launch /
// Spawner.StartEngine, via the prepareAgentChat seam), not against the shared
// constructor — a test against a symbol the collapse introduces cannot be red, it
// fails to compile. Written this way it is unchanged by the collapse and red only
// if the two requests genuinely diverge.
//
// It compares the two requests with the three legacy-dial-only fields zeroed, so
// ANY future field set on one path and not the other fails here. Those three were
// verified against PreparedAgentChat.StartEngine, which reads none of them: their
// absence on the StartRun path is intentional, not the defect the label implies.
func TestSpawner_LaunchAndStartEngine_ShareOneChatRequest(t *testing.T) {
	resetStrictness(t)
	t.Setenv("HOME", t.TempDir())
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	writeSpawnerConfig(t, appDir, "version: 6\nagents:\n  dev:\n    llm: claude-code\n    permissions: bypass\n")
	cfg, err := config.Load(config.WithAppDir(appDir))
	require.NoError(t, err)
	s := newProdSpawner(cfg, filepath.Dir(appDir), nil)

	plan, err := s.Resolve(context.Background(), "dev")
	require.NoError(t, err)
	plan.Workspace = "worktree"
	plan.DirtyTreeHandler = "stale"

	var seen []operations.AgentChatRequest
	stop := errors.New("captured")
	prev := prepareAgentChat
	prepareAgentChat = func(_ context.Context, _ *config.Config, req operations.AgentChatRequest) (*operations.PreparedAgentChat, error) {
		seen = append(seen, req)
		return nil, stop
	}
	t.Cleanup(func() { prepareAgentChat = prev })

	env := map[string]string{"CTXLOOM_SESSION_HARP": "child-a"}
	runnerEnv := map[string]string{"CTXLOOM_COORD_URL": "tcp://127.0.0.1:1"}

	_, err = s.Launch(context.Background(), plan, "the briefing", "sess-9", env, runnerEnv)
	require.ErrorIs(t, err, stop)
	_, err = s.StartEngine(context.Background(), plan, env, runnerEnv)
	require.ErrorIs(t, err, stop)
	require.Len(t, seen, 2)
	launched, started := seen[0], seen[1]

	// Only Launch carries the legacy-dial trio.
	assert.Equal(t, "the briefing", launched.Context)
	assert.Equal(t, "sess-9", launched.ResumeSessionID)
	assert.Equal(t, plan.MCPServers, launched.MCPServers)
	assert.Empty(t, started.Context, "StartRun has no first turn for a lead block to ride")
	assert.Empty(t, started.ResumeSessionID, "StartRun resumes via HarnessSpec.resume_session_id")
	assert.Empty(t, started.MCPServers, "the StartRun path patches plan.MCPServers into its EngineSpawn instead")

	// The trust gate must be installed on BOTH — it is a fail-closed security
	// surface, so an omission here is not a tidiness matter.
	assert.NotNil(t, launched.Gate)
	assert.NotNil(t, started.Gate)

	// Everything else must be identical. Func-typed fields are zeroed because
	// reflect.DeepEqual only ever calls two non-nil funcs unequal; each is
	// asserted on its own above or is nil on both paths here.
	common := func(r operations.AgentChatRequest) operations.AgentChatRequest {
		r.Context, r.ResumeSessionID, r.MCPServers = "", "", nil
		r.Gate, r.Factory, r.Starter, r.Git = nil, nil, nil, nil
		return r
	}
	assert.Equal(t, common(launched), common(started),
		"both launch paths must compose the SAME shared request: a field added to one and not the other is the defect this pins")

	// And the shared half really did carry the plan through, so the comparison
	// is not two equally-empty structs.
	assert.Equal(t, plan.resolved, started.Resolved)
	assert.Equal(t, agent.PermissionBypass, started.Permissions)
	assert.Equal(t, "worktree", started.Workspace)
	assert.Equal(t, "stale", started.DirtyTreeHandler)
	assert.Equal(t, env, started.Env)
	assert.Equal(t, runnerEnv, started.RunnerEnv)
	assert.Equal(t, filepath.Dir(appDir), started.WorkDir)
}
