package operations

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/fsstore"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// Build refuses facts missing either composed port, naming which.
func TestLaunchFacts_BuildRequiresEnginesAndClaims(t *testing.T) {
	_, err := NewLaunchFacts(engine.Registry{}).Claims(fsstore.SessionClaims).Build()
	require.ErrorIs(t, err, ErrLaunchFactsNoEngines)

	_, err = NewLaunchFacts(engines.Registry()).Build()
	require.ErrorIs(t, err, ErrLaunchFactsNoSessionClaims)

	f, err := NewLaunchFacts(engines.Registry()).Claims(fsstore.SessionClaims).Build()
	require.NoError(t, err)
	assert.NotNil(t, f.SessionClaims)
	assert.NotEmpty(t, f.Engines.Names(nil))
}

// The mode defaults to the most restrictive posture; only an explicit Mode
// loosens it.
func TestLaunchFacts_ModeDefaultsStrict(t *testing.T) {
	f, err := NewLaunchFacts(engines.Registry()).Claims(fsstore.SessionClaims).Build()
	require.NoError(t, err)
	assert.False(t, f.Mode.Degraded, "the default mode is never degraded")

	loose := strictness.Mode{Prog: "ctxloom", Degraded: true}
	f, err = NewLaunchFacts(engines.Registry()).Claims(fsstore.SessionClaims).Mode(loose).Build()
	require.NoError(t, err)
	assert.Equal(t, loose, f.Mode)
}

// An App composed without a claim store cannot hand out launch facts: it is
// a composition error, refused loudly with Build's sentinel.
func TestApp_LaunchFactsWithoutClaimsPanics(t *testing.T) {
	app := OpenedApp(nil, Handed{Engines: engines.Registry()})
	assert.PanicsWithError(t, ErrLaunchFactsNoSessionClaims.Error(), func() { app.LaunchFacts() })
}

// The one-shot builder refuses, by sentinel, a launch missing what it needs
// to be correct: a working directory, and facts carrying both composed ports.
func TestOneShot_StartRefusesAnIncompleteLaunch(t *testing.T) {
	ctx := context.Background()
	cfg := config.NewFixture(config.Fixture{})

	_, err := OneShot(testLaunchFacts(), nil, cfg).Label("primary").Start(ctx)
	require.ErrorIs(t, err, ErrOneShotNoWorkDir)

	_, err = OneShot(LaunchFacts{Engines: engines.Registry()}, nil, cfg).WorkDir(t.TempDir()).Start(ctx)
	require.ErrorIs(t, err, ErrLaunchFactsNoSessionClaims)

	_, err = OneShot(testLaunchFacts(), nil, cfg).WorkDir(t.TempDir()).Lazy().Turn(ctx, "hi")
	require.ErrorIs(t, err, config.ErrTrustUnbound, "Lazy defers Start's refusals to the first turn")
}
