package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/engines/mock"
	"github.com/ctxloom/ctxloom/internal/testsupport/enginefixture"
)

// The auth-fix hint is READ OFF THE ENGINE'S OWN DECLARATION: an engine that
// declares how its credential file comes to exist (CredentialSeed.LoginHint)
// and which env vars bypass it (EnvTriggers) gets a hint naming exactly
// those, without this package keeping a per-engine table that a new engine
// would have to be remembered in.
func TestEngineAuthFixHint_NamesTheEngineDeclaredLoginAndEnvVar(t *testing.T) {
	const name = "fixture-authfix"
	kind := enginefixture.Kind(name, mock.WithHome(engine.HomeSpec{
		Vars: []engine.HomeVar{{Name: "FIXTURE_HOME", Subdir: "fixture"}},
		Credentials: engine.Provide(engine.CredentialSeed{
			Subdir:      "fixture",
			EnvTriggers: []string{"FIXTURE_KEY"},
			LoginHint:   "fixture login",
			Files:       []engine.SeedFile{{HostRelHome: ".fixture/creds", DestName: "creds", Required: true}},
			Accept:      []engine.MaterialDelivery{engine.MaterialDeliveryReplicated},
		}),
	}))
	enginefixture.Install(t, kind)

	hint := engineAuthFixHint(name)
	assert.Contains(t, hint, "fixture login")
	assert.Contains(t, hint, "FIXTURE_KEY")
	assert.NotContains(t, hint, "authenticate the engine", "a declared fix is never the generic one")
}

// An engine whose credential does not ride a seedable file has no login to
// name; it gets the generic fix and no declared one can leak in.
func TestEngineAuthFixHint_EngineWithNoSeedGetsTheGenericFix(t *testing.T) {
	for _, engine := range operations.EngineNames() {
		if _, seeded := engineCredentialSeed(engine); seeded {
			continue
		}
		assert.Contains(t, engineAuthFixHint(engine), "authenticate the engine", engine)
	}
	assert.Contains(t, engineAuthFixHint("never-registered"), "authenticate the engine")
}

// Every registered engine that declares a seed has its hint derived from it —
// the same conformance shape TestPingEngineAuth_FailsLoud_NamesTheFix uses.
func TestEngineAuthFixHint_EveryDeclaredSeedIsNamed(t *testing.T) {
	checked := 0
	for _, engine := range operations.EngineNames() {
		seed, ok := engineCredentialSeed(engine)
		if !ok {
			continue
		}
		checked++
		hint := engineAuthFixHint(engine)
		assert.Contains(t, hint, seed.LoginHint, engine)
		for _, v := range seed.EnvTriggers {
			assert.Contains(t, hint, v, engine)
		}
	}
	require.GreaterOrEqual(t, checked, 1, "at least one registered engine declares a credential seed")
}
