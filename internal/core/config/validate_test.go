package config_test

import (
	"context"
	"testing"

	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/composite/compositetest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// stubKind is the smallest engine kind a registry can hold.
type stubKind struct{ engine.Base }

func (stubKind) Instance(engine.Session) (engine.Instance, error) { return nil, nil }
func (stubKind) Exports(engine.Items) (engine.Exports, error)     { return engine.Exports{}, nil }
func (stubKind) Home() engine.HomeSpec                            { return engine.HomeSpec{} }
func (s stubKind) Container() (engine.ContainerSpec, error) {
	return engine.ContainerSpec{}, engine.ErrUnsupported{Engine: s.Name, Capability: "container"}
}
func (stubKind) Transcripts() []engine.TranscriptReader { return nil }
func (stubKind) Hooks() engine.HookCodec                { return nil }
func (stubKind) Wake() engine.Declared[engine.WakeSpec] {
	return engine.Absent[engine.WakeSpec]("a test double wakes nothing")
}
func (stubKind) Approvals() engine.Declared[engine.ApprovalCodec] {
	return engine.Absent[engine.ApprovalCodec]("a test double approves nothing")
}

// registryOf composes stub kinds; the first named ships by default.
func registryOf(names ...engine.Name) engine.Registry {
	kinds := make([]engine.Engine, 0, len(names))
	for i, n := range names {
		dist := engine.DistributionTestOnly
		if i == 0 {
			dist = engine.DistributionDefault
		}
		kinds = append(kinds, stubKind{engine.Base{Definition: engine.Definition{Name: n, Distribution: dist}}})
	}
	reg, err := engine.NewRegistry(kinds...)
	if err != nil {
		panic(err)
	}
	return reg
}

// TestConfig_Validate_ChecksEveryConfiguredEngineNameAgainstTheRegistry:
// an llm entry's type and an isolation engine name must be engines the
// process was composed with; an untyped entry declares no engine and is
// not checked; the refusal names the entry and the engines ctxloom knows.
func TestConfig_Validate_ChecksEveryConfiguredEngineNameAgainstTheRegistry(t *testing.T) {
	reg := registryOf("alpha", "beta")
	cfg := config.NewFixture(config.Fixture{
		LM: config.LMConfig{Configs: map[string]config.LLMConfig{
			"typed":   {Type: "alpha"},
			"untyped": {},
		}},
		IsolationEngines: []string{"beta"},
	})
	require.NoError(t, cfg.Validate(reg))
	assert.Equal(t, "alpha", cfg.DefaultEngine(), "the registry's default is bound as the engine an untyped entry drives")
	assert.Equal(t, "alpha", cfg.EffectiveType(config.LLMConfig{}))
	assert.Equal(t, "beta", cfg.EffectiveType(config.LLMConfig{Type: "beta"}))

	bad := config.NewFixture(config.Fixture{LM: config.LMConfig{Configs: map[string]config.LLMConfig{"stale": {Type: "gamma"}}}})
	err := bad.Validate(reg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `llm.configs.stale`)
	assert.Contains(t, err.Error(), `"gamma"`)
	assert.Contains(t, err.Error(), "alpha, beta")

	badIso := config.NewFixture(config.Fixture{IsolationEngines: []string{"gamma"}})
	assert.ErrorContains(t, badIso.Validate(reg), "isolation.engines")

	noDefault, _ := engine.NewRegistry(stubKind{engine.Base{Definition: engine.Definition{Name: "t", Distribution: engine.DistributionTestOnly}}})
	assert.ErrorContains(t, cfg.Validate(noDefault), "ship by default", "a registry with no default engine cannot resolve an untyped entry")
}

// stubSources serves one parsed config with no bundles and no trust gate.
type stubSources struct{ yaml string }

func (s stubSources) Read(context.Context) (*config.Config, []config.Warning, error) {
	cfg, err := config.ParseConfig([]byte(s.yaml))
	return cfg, nil, err
}
func (stubSources) Readers(context.Context, *config.Config) ([]bundles.Reader, error) {
	return nil, nil
}
func (stubSources) TrustPorts(context.Context, *config.Config) (composite.TrustRoot, composite.ReviewRecords, composite.RetractionRecords, error) {
	root, records, retraction := compositetest.Ports()
	return root, records, retraction, nil
}

// TestOpen_WithEngines_ValidatesEveryGeneration: a process composed with
// engines validates each published generation against them, surfacing an
// unknown name as a validate warning — the config still opens, and the
// strict gate records the warning as a finding.
func TestOpen_WithEngines_ValidatesEveryGeneration(t *testing.T) {
	src := stubSources{yaml: "llm:\n  configs:\n    stale:\n      type: gamma\n"}
	owner, err := config.Open(context.Background(), src, config.WithEngines(registryOf("alpha")))
	require.NoError(t, err)
	snap := owner.Current()
	require.Len(t, snap.Warnings, 1)
	assert.Equal(t, config.WarnKindValidate, snap.Warnings[0].Kind)
	assert.Contains(t, snap.Warnings[0].Text, `"gamma"`)

	unvalidated, err := config.Open(context.Background(), src)
	require.NoError(t, err)
	assert.Empty(t, unvalidated.Current().Warnings, "without engines there is nothing to validate against")
}

// authKind is a stub kind whose Auth supports only the token.
type authKind struct{ stubKind }

type tokenOnlyAuth struct{}

func (tokenOnlyAuth) Modes() []engine.AuthMode { return []engine.AuthMode{engine.AuthToken} }
func (tokenOnlyAuth) Credentials(engine.AuthMode, func(string) (string, bool)) (engine.Credentials, error) {
	return engine.Credentials{}, nil
}

func (authKind) Home() engine.HomeSpec {
	return engine.HomeSpec{Vars: []engine.HomeVar{{Name: "X_HOME", Subdir: "x"}}, Auth: engine.Provide[engine.Auth](tokenOnlyAuth{})}
}

// Config load runs the same auth check `agent create/edit` and every launch
// run (engine.CheckAuth): an agent whose own llm names an engine is refused
// at load for a mode that engine lacks, an unknown mode, or any mode on an
// engine with no auth — typed, with a remedy.
func TestConfig_Validate_RefusesAnAgentsInvalidAuthAtLoad(t *testing.T) {
	reg, err := engine.NewRegistry(
		authKind{stubKind{engine.Base{Definition: engine.Definition{Name: "withauth", Distribution: engine.DistributionDefault}}}},
		stubKind{engine.Base{Definition: engine.Definition{Name: "noauth", Distribution: engine.DistributionTestOnly}}},
	)
	require.NoError(t, err)
	for _, tc := range []struct {
		llm, mode string
		sentinel  error
	}{
		{"withauth", "login", engine.ErrAuthModeUnsupported},
		{"withauth", "apikey", engine.ErrUnknownAuthMode},
		{"noauth", "token", engine.ErrEngineHasNoAuth},
	} {
		cfg := config.NewFixture(config.Fixture{Agents: map[string]agents.Agent{"a": {Name: "a", LLM: tc.llm, Auth: tc.mode}}})
		err := cfg.Validate(reg)
		require.ErrorIs(t, err, tc.sentinel, "%s/%s", tc.llm, tc.mode)
		assert.Contains(t, err.Error(), "agents.a")
	}
	ok := config.NewFixture(config.Fixture{Agents: map[string]agents.Agent{"a": {Name: "a", LLM: "withauth", Auth: "token"}, "b": {Name: "b", LLM: "noauth"}}})
	require.NoError(t, ok.Validate(reg))
}
