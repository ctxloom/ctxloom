package operations

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/agents"
	"github.com/ctxloom/ctxloom/internal/config"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
)

// agentFanConfig is the Phase-C fan fixture: two profiles each carrying a
// DISTINCT fragment (so a member's composed context is identifiable in its
// output), and two agents whose engine DISAGREES with their profile's llm —
// so a resolved member's backend proves whether the agent engine (override)
// or the profile llm (bare-profile fall-through) won.
//
//	profile sec-profile : llm agy-code,    fragment security-rules ("Security Rules")
//	profile perf-profile: llm claude-fast, fragment go-patterns    ("Go Patterns")
//	agent sec : engine claude-fast over sec-profile  (overrides agy-code)
//	agent perf: engine agy-code   over perf-profile  (overrides claude-fast)
func agentFanConfig() *config.Config {
	return config.NewFixture(config.Fixture{
		AppPaths: []string{testBaseDir},
		LM: config.LMConfig{
			Configs: map[string]config.LLMConfig{
				"claude-fast": {Type: "claude-code"},
				"agy-code":    {Type: "antigravity"},
			},
			Defaults: config.RoleDefaults{Primary: "claude-fast"},
		},
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{
			"sec-profile":  {LLM: "agy-code", Fragments: []config.FragmentRef{{Name: "dev#fragments/security-rules"}}},
			"perf-profile": {LLM: "claude-fast", Fragments: []config.FragmentRef{{Name: "dev#fragments/go-patterns"}}},
		}},
		Agents: map[string]agents.Agent{
			"sec":  {Engine: "claude-fast", Profiles: []string{"sec-profile"}},
			"perf": {Engine: "agy-code", Profiles: []string{"perf-profile"}},
		},
	})
}

func emitFragmentsFactory(string, string, int) (pb.Client, error) {
	return &stubClient{emitFragments: true}, nil
}

// TestMapProfiles_FansAgentsEachOwnEngineAndContext is the heart of Phase C:
// `map --agents sec,perf` fans the two NAMED agents, and each runs on ITS
// OWN engine + ITS OWN composed profile-context — not one shared engine.
func TestMapProfiles_FansAgentsEachOwnEngineAndContext(t *testing.T) {
	_, loader := setupContextTestFS(t)
	cfg := agentFanConfig()

	parts := MapProfiles(context.Background(), cfg, MapProfilesRequest{
		Members: []string{"sec", "perf"},
		Task:    "review",
		Loader:  loader,
		Factory: emitFragmentsFactory,
	})

	require.Len(t, parts, 2)
	assert.Equal(t, "sec", parts[0].Profile)
	assert.Equal(t, "perf", parts[1].Profile)

	// Per-member engine: each agent's engine OVERRODE its profile's llm, so the
	// two members resolved to DIFFERENT backends (not one shared engine).
	assert.False(t, parts[0].Failed())
	assert.Equal(t, "claude-code", parts[0].Backend, "sec engine claude-fast overrides sec-profile's agy-code")
	assert.False(t, parts[1].Failed())
	assert.Equal(t, "antigravity", parts[1].Backend, "perf engine agy-code overrides perf-profile's claude-fast")

	// Per-member composed context: each member carried only its own profile's
	// fragment (the stub echoes the assembled context it was launched with).
	assert.Contains(t, parts[0].Output, "Security Rules")
	assert.NotContains(t, parts[0].Output, "Go Patterns", "sec must not see perf's context")
	assert.Contains(t, parts[1].Output, "Go Patterns")
	assert.NotContains(t, parts[1].Output, "Security Rules", "perf must not see sec's context")
}

// TestMapProfiles_BareProfileSugarUsesDefaultEngine proves the no-regression
// guarantee: bare profile members (no agent of that name) run exactly like
// before — each on its OWN profile's llm, NOT an agent override. The contrast
// with the previous test is sharp: the same two profiles, fanned bare, resolve
// to the OPPOSITE backends because no agent engine is in play.
func TestMapProfiles_BareProfileSugarUsesDefaultEngine(t *testing.T) {
	_, loader := setupContextTestFS(t)
	cfg := agentFanConfig()

	parts := MapProfiles(context.Background(), cfg, MapProfilesRequest{
		Members: []string{"sec-profile", "perf-profile"}, // bare profiles, not agents
		Task:    "review",
		Loader:  loader,
		Factory: emitFragmentsFactory,
	})

	require.Len(t, parts, 2)
	// Bare profile = default-engine sugar: each member uses its profile's own llm.
	assert.Equal(t, "antigravity", parts[0].Backend, "bare sec-profile keeps its own agy-code")
	assert.Equal(t, "claude-code", parts[1].Backend, "bare perf-profile keeps its own claude-fast")
	assert.Contains(t, parts[0].Output, "Security Rules")
	assert.Contains(t, parts[1].Output, "Go Patterns")
}

// TestMapProfiles_MixedAgentAndBareProfile proves members may MIX: a named
// agent and a bare profile in one fan, each resolving by its own rule. sec
// (agent, engine override) and sec-profile (bare, own llm) compose the SAME
// fragment yet resolve to DIFFERENT engines, side by side.
func TestMapProfiles_MixedAgentAndBareProfile(t *testing.T) {
	_, loader := setupContextTestFS(t)
	cfg := agentFanConfig()

	parts := MapProfiles(context.Background(), cfg, MapProfilesRequest{
		Members: []string{"sec", "sec-profile"}, // agent + bare profile
		Task:    "review",
		Loader:  loader,
		Factory: emitFragmentsFactory,
	})

	require.Len(t, parts, 2)
	assert.Equal(t, "sec", parts[0].Profile)
	assert.Equal(t, "claude-code", parts[0].Backend, "agent sec: engine override → claude-fast")
	assert.Equal(t, "sec-profile", parts[1].Profile)
	assert.Equal(t, "antigravity", parts[1].Backend, "bare sec-profile: own llm → agy-code")
	// Both composed the same fragment.
	assert.Contains(t, parts[0].Output, "Security Rules")
	assert.Contains(t, parts[1].Output, "Security Rules")
}

// TestMapProfiles_LLMOverrideWinsOverAgentEngine proves the map/weave --llm
// override beats even an agent's declared engine: forcing every member onto one
// engine (a cost/availability knob) collapses the per-member engines.
func TestMapProfiles_LLMOverrideWinsOverAgentEngine(t *testing.T) {
	_, loader := setupContextTestFS(t)
	cfg := agentFanConfig()

	parts := MapProfiles(context.Background(), cfg, MapProfilesRequest{
		Members: []string{"sec", "perf"}, // sec→claude-fast, perf→agy-code by default
		Task:    "review",
		LLM:     "agy-code", // force all members onto agy-code
		Loader:  loader,
		Factory: emitFragmentsFactory,
	})

	require.Len(t, parts, 2)
	assert.Equal(t, "antigravity", parts[0].Backend, "--llm override beats sec's engine")
	assert.Equal(t, "antigravity", parts[1].Backend, "--llm override beats perf's engine")
}

// TestWeave_FansAgentBareMixSynthesizesAndInjects proves the weave composite
// over the Phase-C fan: an agent + a bare profile member run (each on its own
// engine), an injected part is appended, and the synthesizer reduces them all.
func TestWeave_FansAgentBareMixSynthesizesAndInjects(t *testing.T) {
	_, loader := setupContextTestFS(t)
	cfg := withProfileDefs(agentFanConfig(), map[string]config.Profile{
		"synth": {LLM: "agy-code"},
	})

	// echo stub: the synthesizer's report is the framed synthesis input, so we can
	// assert the member + injected parts flowed in.
	factory := func(string, string, int) (pb.Client, error) { return &stubClient{echo: true}, nil }

	res, err := Weave(context.Background(), cfg, WeaveRequest{
		Members:       []string{"sec", "perf-profile"}, // agent + bare profile
		Synthesize:    "synth",
		Task:          "review the diff",
		InjectedParts: []Part{{Profile: "legacy", Output: "old finding"}},
		Loader:        loader,
		Factory:       factory,
	})
	require.NoError(t, err)

	// Members ran (each on its own engine) + injected part appended, in order.
	require.Len(t, res.Parts, 3)
	assert.Equal(t, "sec", res.Parts[0].Profile)
	assert.Equal(t, "claude-code", res.Parts[0].Backend, "agent sec engine override")
	assert.Equal(t, "perf-profile", res.Parts[1].Profile)
	assert.Equal(t, "claude-code", res.Parts[1].Backend, "bare perf-profile own claude-fast")
	assert.Equal(t, "legacy", res.Parts[2].Profile)
	assert.Equal(t, "old finding", res.Parts[2].Output)

	// Synthesizer ran on its own llm and the report carries every labeled part.
	require.NotNil(t, res.Synthesizer)
	assert.Equal(t, "antigravity", res.Synthesizer.Backend)
	assert.Contains(t, res.Report, "===== part: sec")
	assert.Contains(t, res.Report, "===== part: perf-profile")
	assert.Contains(t, res.Report, "===== part: legacy")
	assert.Contains(t, res.Report, "old finding")
}
