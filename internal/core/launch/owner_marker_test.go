package launch_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/launch/launchtest"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
)

// The turn-start mail-drain hook drains only under the session-owner marker
// (sessions.EnvSessionOwner). These pin who the launch hands it to: the
// human's interactive session and nobody else, whatever a caller's
// passthrough says — on BOTH carriers, the engine env every host arm starts
// from (Launch.EngineEnv) and the cell env a container is built with.

// resolveAs resolves the fixture's launch as id in mode, with srcEnv as the
// caller's passthrough, returning the launch and the cell request's env.
func resolveAs(t *testing.T, id func(sessions.Identity) sessions.Identity, mode engine.Mode, srcEnv map[string]string) (launch.Launch, map[string]string) {
	t.Helper()
	env := launchtest.Deps(t)
	l, err := launch.Resolve(context.Background(), env.Deps, launch.Source{
		Identity: id(env.Identity), Profiles: []string{"base"}, Mode: mode, Prompt: "x", WorkDir: env.Project, Env: srcEnv,
	})
	require.NoError(t, err)
	return l, env.LastCellRequest().Env
}

func asIs(id sessions.Identity) sessions.Identity { return id }

func TestResolve_TheSessionOwnersEngineCarriesTheOwnerMarker(t *testing.T) {
	l, cellEnv := resolveAs(t, asIs, engine.Interactive, nil)

	require.Equal(t, sessions.OriginSession, l.Identity.Origin(), "the fixture's identity is a human's session")
	assert.Equal(t, sessions.SessionOwnerOn, l.EngineEnv()[sessions.EnvSessionOwner])
	assert.Equal(t, sessions.SessionOwnerOn, cellEnv[sessions.EnvSessionOwner], "the cell a container is built from carries it too")
}

func TestResolve_ADelegatedChildNeverCarriesTheOwnerMarkerEvenIfAsked(t *testing.T) {
	child := func(id sessions.Identity) sessions.Identity { id.Depth = 1; return id }
	forged := map[string]string{sessions.EnvSessionOwner: sessions.SessionOwnerOn}

	for _, mode := range []engine.Mode{engine.Interactive, engine.Structured} {
		l, cellEnv := resolveAs(t, child, mode, forged)

		assert.NotContains(t, l.EngineEnv(), sessions.EnvSessionOwner,
			"mode %v: a child's mail is its runner's to deliver, and a passthrough cannot forge ownership", mode)
		assert.NotContains(t, cellEnv, sessions.EnvSessionOwner, "mode %v: nor on the cell", mode)
	}
}

func TestResolve_AStructuredRunAtDepthZeroIsNotTheOwner(t *testing.T) {
	forged := map[string]string{sessions.EnvSessionOwner: sessions.SessionOwnerOn}

	l, cellEnv := resolveAs(t, asIs, engine.Structured, forged)

	assert.NotContains(t, l.EngineEnv(), sessions.EnvSessionOwner, "a structured run is handed its mail as its turns")
	assert.NotContains(t, cellEnv, sessions.EnvSessionOwner)
}
