package launch_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/composite/compositetest"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/launch/launchtest"
)

// Owner ruling 2026-10-02: the engine's OWN ctxloom children (its MCP server,
// its hooks) share the session's signature-check posture. The launch carries
// it to them in the engine's environment, derived from the generation the
// launch decided with and from nothing else.

func resolveUnder(t *testing.T, tr composite.Trust, srcEnv map[string]string) launch.Launch {
	t.Helper()
	env := launchtest.Deps(t)
	env.Deps.Snapshot.Trust = tr
	l, err := launch.Resolve(context.Background(), env.Deps, launch.Source{
		Identity: env.Identity, Profiles: []string{"base"}, Mode: engine.Interactive, Prompt: "x", WorkDir: env.Project, Env: srcEnv,
	})
	require.NoError(t, err)
	return l
}

func TestResolve_AWaivedGenerationHandsItsEngineChildrenTheWaiver(t *testing.T) {
	root, records, retraction := compositetest.Ports()
	waived, err := composite.NewTrust(root, records, retraction, composite.WithoutSignatureCheck())
	require.NoError(t, err)

	l := resolveUnder(t, waived, nil)

	assert.Equal(t, bundles.SessionSigCheckOn, l.EngineEnv()[bundles.SessionSigCheckEnv])
	assert.NotContains(t, l.EngineEnv(), bundles.SigCheckEnv, "the invocation switch itself is never re-exported")
}

func TestResolve_AnEnforcedGenerationCarriesNoWaiverEvenIfAsked(t *testing.T) {
	l := resolveUnder(t, compositetest.Trust(), map[string]string{bundles.SessionSigCheckEnv: bundles.SessionSigCheckOn})

	assert.NotContains(t, l.EngineEnv(), bundles.SessionSigCheckEnv,
		"the carrier is the generation's posture: a caller's passthrough cannot forge it for a child")
}
