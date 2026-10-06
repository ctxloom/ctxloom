package launch_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/launch/launchtest"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/core/trust"
)

// Owner ruling 2026-10-02: the engine's OWN ctxloom children (its MCP server,
// its hooks) share the session's signature-check posture. The launch carries
// it to them in the engine's environment, derived from the generation the
// launch decided with and from nothing else.

func resolveUnder(t *testing.T, waived bool, srcEnv map[string]string) launch.Launch {
	t.Helper()
	env := launchtest.Deps(t)
	env.Deps.Snapshot.Config.BindTrustRootForTesting(trust.NoSigners{}, waived)
	l, err := launch.Resolve(context.Background(), env.Deps, launch.Source{
		Identity: env.Identity, Profiles: []string{"base"}, Mode: engine.Interactive, Prompt: "x", WorkDir: env.Project, Env: srcEnv,
	})
	require.NoError(t, err)
	return l
}

func TestResolve_AWaivedGenerationHandsItsEngineChildrenTheWaiver(t *testing.T) {
	l := resolveUnder(t, true, nil)

	assert.Equal(t, sessions.SigCheckWaivedOn, l.EngineEnv()[sessions.EnvSigCheckWaived])
	assert.NotContains(t, l.EngineEnv(), bundles.SigCheckEnv, "the invocation switch itself is never re-exported")
}

func TestResolve_AnEnforcedGenerationCarriesNoWaiverEvenIfAsked(t *testing.T) {
	l := resolveUnder(t, false, map[string]string{sessions.EnvSigCheckWaived: sessions.SigCheckWaivedOn})

	assert.NotContains(t, l.EngineEnv(), sessions.EnvSigCheckWaived,
		"the carrier is the generation's posture: a caller's passthrough cannot forge it for a child")
}
