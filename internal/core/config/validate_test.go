package config_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/composite/compositetest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/shared/report"
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

// Permissions: every stub declares stubModel, which refuses the key "bad".
func (stubKind) Permissions() engine.Declared[engine.PermissionModel] {
	return engine.Provide[engine.PermissionModel](stubModel{})
}

func (stubKind) Trust() engine.Declared[engine.RepoTrust] {
	return engine.Absent[engine.RepoTrust]("a test double trusts no repository")
}

type stubModel struct{}

func (stubModel) Keys() []string     { return []string{"mode"} }
func (stubModel) Postures() []string { return []string{"on"} }
func (stubModel) Validate(doc map[string]any) error {
	if _, bad := doc["bad"]; bad {
		return errStubBad
	}
	return nil
}
func (stubModel) Resolve(engine.PostureRequest) (map[string]any, error) { return map[string]any{}, nil }
func (stubModel) Floor() map[string]any                                 { return map[string]any{} }
func (stubModel) Decode(map[string]any) (string, error)                 { return "on", nil }
func (stubModel) Transitions(map[string]any) []engine.PostureTransition { return nil }
func (stubModel) Label(string) string                                   { return "" }
func (stubModel) Sandboxes(string) []engine.Sandbox                     { return []engine.Sandbox{engine.SandboxFull} }
func (stubModel) DefaultSandbox() engine.Sandbox                        { return engine.SandboxFull }
func (stubModel) Reviewer() bool                                        { return false }

var errStubBad = errors.New("stub: bad key")

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

// An agent binding has no auth to choose: every run ctxloom spawns
// authenticates with the token. A binding still carrying `auth:` -- any
// value, the retired api-key and cloud and the human's login among them -- is
// refused at load, typed, naming the token and where the human's own login
// is chosen instead; never dropped in silence by the lenient decode.
func TestParseConfig_RefusesAnAgentAuthKey(t *testing.T) {
	for _, mode := range []string{"login", "api-key", "cloud", "token"} {
		_, err := config.ParseConfig([]byte("version: 6\nagents:\n  dev:\n    profiles: [base]\n    auth: " + mode + "\n"))
		require.ErrorIs(t, err, agents.ErrRetiredAuthKey, mode)
		assert.Contains(t, err.Error(), `"dev"`, mode)
		assert.Contains(t, err.Error(), "claude setup-token", mode)
		assert.Contains(t, err.Error(), "CLAUDE_CODE_OAUTH_TOKEN", mode)
		assert.Contains(t, err.Error(), "auth: login", mode)
	}
}

// The top-level `auth:` is how the HUMAN's own session authenticates:
// login or token, undeclared the token. api-key and cloud are gone for
// everyone -- refused, typed, naming what is accepted.
func TestParseConfig_SessionAuth(t *testing.T) {
	for doc, want := range map[string]engine.AuthMode{
		"version: 6\n":              engine.AuthToken,
		"version: 6\nauth: token\n": engine.AuthToken,
		"version: 6\nauth: login\n": engine.AuthLogin,
	} {
		cfg, err := config.ParseConfig([]byte(doc))
		require.NoError(t, err, doc)
		assert.Equal(t, want, cfg.SessionAuth(), doc)
	}
	for _, mode := range []string{"api-key", "cloud", "keychain"} {
		_, err := config.ParseConfig([]byte("version: 6\nauth: " + mode + "\n"))
		require.ErrorIs(t, err, engine.ErrUnknownAuthMode, mode)
		var r report.Remediable
		require.ErrorAs(t, err, &r, mode)
		assert.Contains(t, r.Remedy(), "login", mode)
		assert.Contains(t, r.Remedy(), "token", mode)
	}
}
