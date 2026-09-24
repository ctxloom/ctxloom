package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/engines/mock"
	"github.com/ctxloom/ctxloom/internal/testsupport/enginefixture"
)

// The auth-fix hint is READ OFF THE ENGINE'S OWN DECLARATION: an engine that
// declares the command minting its token (TokenAuth.MintHint) and which env
// vars authenticate it instead (EnvTriggers) gets a hint naming exactly
// those, without this package keeping a per-engine table that a new engine
// would have to be remembered in.
func TestEngineAuthFixHint_NamesTheEngineDeclaredMintCommandAndEnvVar(t *testing.T) {
	const name = "fixture-authfix"
	kind := enginefixture.Kind(name, mock.WithHome(engine.HomeSpec{
		Vars:        []engine.HomeVar{{Name: "FIXTURE_HOME", Subdir: "fixture"}},
		Auth:        engine.Provide(engine.TokenAuth{TokenVar: "FIXTURE_TOKEN", EnvTriggers: []string{"FIXTURE_KEY"}, MintHint: "fixture setup-token"}),
		SharedLogin: engine.Absent[engine.SharedLogin]("the fixture keeps no credential"),
	}))
	enginefixture.Install(t, kind)

	hint := engineAuthFixHint(name)
	assert.Contains(t, hint, "fixture setup-token")
	assert.Contains(t, hint, "ctxloom auth set-token")
	assert.Contains(t, hint, "FIXTURE_KEY")
	assert.NotContains(t, hint, "authenticate the engine", "a declared fix is never the generic one")
}

// An engine with no token auth has no mint command to name; it gets the
// generic fix and no declared one can leak in.
func TestEngineAuthFixHint_EngineWithNoTokenAuthGetsTheGenericFix(t *testing.T) {
	for _, engine := range operations.EngineNames(engines.Registry()) {
		if _, ok := isolation.TokenAuthFor(engine); ok {
			continue
		}
		assert.Contains(t, engineAuthFixHint(engine), "authenticate the engine", engine)
	}
	assert.Contains(t, engineAuthFixHint("never-registered"), "authenticate the engine")
}

// Every registered engine that declares token auth has its hint derived from
// it — the same conformance shape TestPingEngineAuth_FailsLoud_NamesTheFix
// uses.
func TestEngineAuthFixHint_EveryDeclaredTokenAuthIsNamed(t *testing.T) {
	checked := 0
	for _, engine := range operations.EngineNames(engines.Registry()) {
		a, ok := isolation.TokenAuthFor(engine)
		if !ok {
			continue
		}
		checked++
		hint := engineAuthFixHint(engine)
		assert.Contains(t, hint, a.MintHint, engine)
		for _, v := range a.EnvTriggers {
			assert.Contains(t, hint, v, engine)
		}
	}
	require.GreaterOrEqual(t, checked, 1, "at least one registered engine declares token auth")
}
