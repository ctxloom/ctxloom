package launch_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/paths"
)

// ForSession roots the claim check through the composed constructor at the
// ctxloom home's sessions directory and the minted harp — and the store it
// builds is the one the launch carries.
func TestDeps_ForSession_RootsTheComposedStoreAtTheSession(t *testing.T) {
	var gotRoot, gotHarp string
	store := composite.Inline{Max: 1}
	deps := launch.Deps{
		Host: launch.HostFacts{CtxloomHome: "/ctx-home"},
		SessionClaims: func(sessionsRoot, harp string) composite.Transport {
			gotRoot, gotHarp = sessionsRoot, harp
			return store
		},
	}

	got := deps.ForSession("brisk-harp")

	assert.Equal(t, filepath.Join("/ctx-home", paths.SessionsDir), gotRoot)
	assert.Equal(t, "brisk-harp", gotHarp)
	assert.Equal(t, composite.Transport(store), got.ClaimCheck)
	assert.Nil(t, deps.ClaimCheck, "ForSession returns a rooted copy; the process-wide Deps stays unrooted")
}

// A Deps composed without SessionClaims has no claim store to root, and
// nothing to fall back on: launching with claims that land nowhere is the
// silent failure this refuses. It is a composition error, so it panics at
// the first launch rather than degrading.
func TestDeps_ForSession_WithoutSessionClaimsPanics(t *testing.T) {
	require.Panics(t, func() { launch.Deps{}.ForSession("brisk-harp") })
}
