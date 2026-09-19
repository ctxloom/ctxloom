package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	"github.com/ctxloom/ctxloom/internal/testsupport/enginefixture"
)

// The auth-fix hint is READ OFF THE ENGINE'S OWN DECLARATION: an engine that
// declares how its credential file comes to exist (CredentialSeed.LoginHint)
// and which env var bypasses it (EnvTrigger) gets a hint naming exactly
// those, without this package keeping a per-engine table that a new engine
// would have to be remembered in.
func TestEngineAuthFixHint_NamesTheEngineDeclaredLoginAndEnvVar(t *testing.T) {
	const name = "fixture-authfix"
	d := enginefixture.Hosting(name)
	d.Home = agent.Provide(agent.EngineHome{
		Vars: []agent.HomeVar{{EnvVar: "FIXTURE_HOME", Subdir: "fixture"}},
		Credentials: agent.Provide(agent.CredentialSeed{
			Subdir:     "fixture",
			EnvTrigger: "FIXTURE_KEY",
			LoginHint:  "fixture login",
			Files:      []agent.SeedFile{{HostRelHome: ".fixture/creds", DestName: "creds", Required: true}},
		}),
	})
	require.NoError(t, backends.Register(enginefixture.Registry(d), d))
	t.Cleanup(func() { backends.UnregisterForTesting(name) })

	hint := engineAuthFixHint(name)
	assert.Contains(t, hint, "fixture login")
	assert.Contains(t, hint, "FIXTURE_KEY")
	assert.NotContains(t, hint, "authenticate the engine", "a declared fix is never the generic one")
}

// An engine whose credential does not ride a seedable file has no login to
// name; it gets the generic fix and no declared one can leak in.
func TestEngineAuthFixHint_EngineWithNoSeedGetsTheGenericFix(t *testing.T) {
	for _, engine := range backends.List() {
		if _, seeded := backends.CredentialSeedFor(engine).Get(); seeded {
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
	for _, engine := range backends.List() {
		seed, ok := backends.CredentialSeedFor(engine).Get()
		if !ok {
			continue
		}
		checked++
		hint := engineAuthFixHint(engine)
		assert.Contains(t, hint, seed.LoginHint, engine)
		if seed.EnvTrigger != "" {
			assert.Contains(t, hint, seed.EnvTrigger, engine)
		}
	}
	require.GreaterOrEqual(t, checked, 1, "at least one registered engine declares a credential seed")
}
