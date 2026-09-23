package operations

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/fsstore"
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
