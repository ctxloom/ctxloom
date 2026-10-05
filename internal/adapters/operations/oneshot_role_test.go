package operations

import (
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// roleAgentEnvKey is the env key the role agent's label declares and the
// fast label does not, so its presence on a launch proves which label's env
// crossed.
const roleAgentEnvKey = "CTXLOOM_MOCK_RESPONSE"

// roleTestConfig is a project whose fast role and role agents disagree on
// everything a launch takes from its source: the agents' label is a
// different entry with its own env, and they declare a container runtime the
// project default does not. agentNames are the role agents to declare.
func roleTestConfig(t *testing.T, agentNames ...string) *config.Config {
	t.Helper()
	bindings := map[string]agents.Agent{}
	for _, name := range agentNames {
		bindings[name] = agents.Agent{LLM: "role-llm", Runtime: string(launch.RuntimeRootless)}
	}
	return cfgWithDirProfiles(t, afero.NewMemMapFs(), testBaseDir, nil, config.Fixture{
		LM: config.LMConfig{
			Configs: map[string]config.LLMConfig{
				"fast-llm": {Type: "mock"},
				"role-llm": {Type: "mock", Body: map[string]any{"model": "role-model", "mock_control": map[string]any{roleAgentEnvKey: "from-the-role-agent"}}},
			},
			Defaults: config.RoleDefaults{Primary: "fast-llm", Fast: "fast-llm"},
		},
		Agents: bindings,
	})
}

// assertRunsAsRoleAgent resolves src and pins that the launch is the role
// agent's: its label, its label's env, its declared runtime, at plan.
func assertRunsAsRoleAgent(t *testing.T, cfg *config.Config, src launch.Source, name string) {
	t.Helper()
	assert.Equal(t, name, src.Agent, "the one-shot runs as the agent named for its role")
	assert.False(t, src.Internal, "an agent binding is not the label-only internal arm")
	_, loader := setupContextTestFS(t)
	o, err := testOneShot(t, cfg, opPipe(cfg, loader), &stubEngine{out: "ok"}, src)
	require.NoError(t, err)
	assert.Equal(t, "role-llm", o.Launch.Label.Label, "the agent's label, not defaults.fast")
	assert.Equal(t, "role-model", o.Launch.Label.Model, "no Model override keeps the agent label's own model")
	assert.Equal(t, "from-the-role-agent", o.Launch.Env[roleAgentEnvKey], "the agent's label env crossed")
	assert.Equal(t, launch.RuntimeRootless, o.Launch.Declared.Runtime, "the agent's runtime axis is honoured")
	assert.Equal(t, "plan", o.Launch.Permission.Posture.Document["mode"], "a role agent's one-shot still only reads and answers")
}

// assertFallsBackToFast pins the no-such-agent arm: defaults.fast, label-only,
// with exactly one advisory finding naming the missing agent.
func assertFallsBackToFast(t *testing.T, cfg *config.Config, src launch.Source, found report.Findings, name string) {
	t.Helper()
	assert.True(t, src.Internal)
	assert.Empty(t, src.Agent)
	assert.Equal(t, cfg.FastLabel(), src.Label)
	require.Len(t, found, 1, "the fallback is reported, never silent")
	assert.False(t, found[0].Fatal(), "the fallback runs: a project with no role agent is still served")
	assert.Contains(t, found[0].Text, name)
}

func TestDistillerOneShot_RunsAsTheDistillerAgent(t *testing.T) {
	cfg := roleTestConfig(t, distillerAgent)
	var found report.Collector
	src := DistillerOneShot(LaunchFacts{}, nil, cfg).WorkDir(t.TempDir()).source(&found)
	assert.Empty(t, found.All())
	assertRunsAsRoleAgent(t, cfg, src, distillerAgent)
}

func TestDistillerOneShot_NoDistillerAgentFallsBackToFastWithAFinding(t *testing.T) {
	cfg := roleTestConfig(t, triageAgent)
	var found report.Collector
	src := DistillerOneShot(LaunchFacts{}, nil, cfg).WorkDir(t.TempDir()).source(&found)
	assertFallsBackToFast(t, cfg, src, found.All(), distillerAgent)
}

// An explicit label (`bundle distill --llm <label>`) is the caller's choice
// and wins over the role agent, with nothing to report.
func TestDistillerOneShot_ExplicitLabelWins(t *testing.T) {
	cfg := roleTestConfig(t, distillerAgent)
	var found report.Collector
	src := DistillerOneShot(LaunchFacts{}, nil, cfg).Label("fast-llm").WorkDir(t.TempDir()).source(&found)
	assert.True(t, src.Internal)
	assert.Equal(t, "fast-llm", src.Label)
	assert.Empty(t, found.All())
}

func TestTriageOneShot_RunsAsTheTriageAgent(t *testing.T) {
	cfg := roleTestConfig(t, triageAgent)
	var found report.Collector
	src := triageOneShot(LaunchFacts{}, EvaluateTriggersRequest{RepoDir: t.TempDir()}, cfg).source(&found)
	assert.Empty(t, found.All())
	assertRunsAsRoleAgent(t, cfg, src, triageAgent)
}

func TestTriageOneShot_NoTriageAgentFallsBackToFastWithAFinding(t *testing.T) {
	cfg := roleTestConfig(t, distillerAgent)
	var found report.Collector
	src := triageOneShot(LaunchFacts{}, EvaluateTriggersRequest{RepoDir: t.TempDir()}, cfg).source(&found)
	assertFallsBackToFast(t, cfg, src, found.All(), triageAgent)
}

func TestTriageOneShot_RequestLabelWins(t *testing.T) {
	cfg := roleTestConfig(t, triageAgent)
	var found report.Collector
	src := triageOneShot(LaunchFacts{}, EvaluateTriggersRequest{RepoDir: t.TempDir(), LLMLabel: "fast-llm"}, cfg).source(&found)
	assert.True(t, src.Internal)
	assert.Equal(t, "fast-llm", src.Label)
	assert.Empty(t, found.All())
}
