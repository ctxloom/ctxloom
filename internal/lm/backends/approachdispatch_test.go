package backends

import (
	"sort"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// allSurfaceKinds is every kind the SurfaceSelection builder can ask a backend
// about (cells.go's surfaceOrder), plus one out-of-range value to prove an
// unknown kind is reported absent rather than panicking.
var allSurfaceKinds = []agent.SurfaceKind{
	agent.SurfaceContext,
	agent.SurfaceMCP,
	agent.SurfaceSettings,
	agent.SurfaceCommands,
	agent.SurfaceSkills,
	agent.SurfaceKind(99),
}

// nativeSurfaceBackends is every registered backend with a REAL (non-Empty)
// SurfaceSet — exactly the backends BuildSurfaces answers with something other
// than agent.EmptySurfaceSet, which is the same `newSurfaces != nil` predicate
// BuildSurfaces itself branches on.
//
// DERIVED from the registry rather than listed. A hand-written roster of engine
// names silently stops covering a backend the moment one is added, and silently
// names a backend that no longer exists the moment one is deleted — the second
// of which is how this list came to name two removed engines while still
// reading as an authority. Deriving it means a backend cannot enter or leave
// the registry without entering or leaving this gate with it.
// It takes t and refuses an EMPTY result on purpose: every caller ranges over
// it, so a derivation that silently returned nothing would run zero subtests
// and report a confident pass having gated no backend at all.
func nativeSurfaceBackends(t *testing.T) []string {
	t.Helper()
	var names []string
	for name, d := range descriptors {
		if d.newSurfaces != nil {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	require.NotEmpty(t, names, "no registered backend has a real SurfaceSet; this gate would range over nothing")
	return names
}

// TestApproachDispatch_DefaultIsFirstSupported is the parity gate for
// approach dispatch.
//
// Written per backend, SupportedApproaches and DefaultApproach come to SIX
// bodies — once per (backend × method) — each a one-liner naming that backend's
// own table. Six hand-written bodies can disagree with each other and with the
// contract cells.go states for them: "DefaultApproach reports the approach WithEverything
// selects for kind — the backend's native realization. false means kind is
// absent/folded for this backend."
//
// This test states that contract ONCE and holds every one of them to it, so the
// single shared agent.TableDispatch carrier is checked against behaviour rather
// than against a diff. It is deliberately written against the
// agent.SurfaceSet interface, not against any backend's concrete Surfaces, so it
// keeps gating a backend added later.
func TestApproachDispatch_DefaultIsFirstSupported(t *testing.T) {
	for _, name := range nativeSurfaceBackends(t) {
		t.Run(name, func(t *testing.T) {
			set := BuildSurfaces(name, agent.SurfaceInputs{Context: "ctx"}, afero.NewMemMapFs())

			anySupported := false
			for _, kind := range allSurfaceKinds {
				supported := set.SupportedApproaches(kind)
				def, ok := set.DefaultApproach(kind)

				if len(supported) == 0 {
					assert.False(t, ok, "%s: %s advertises no approach, so DefaultApproach must report absent/folded", name, kind)
					continue
				}
				anySupported = true
				assert.True(t, ok, "%s: %s advertises approaches, so it must have a default", name, kind)
				assert.Equal(t, supported[0], def,
					"%s: %s's default must be the FIRST declared approach (cells.go's stated contract)", name, kind)
			}
			assert.True(t, anySupported, "%s: a native-surface backend must advertise at least one approach", name)
		})
	}
}

// TestApproachDispatch_SupportedIsResolvable pins the other half of the pair:
// every approach a backend ADVERTISES must actually resolve to a concrete
// surface via SurfaceFor. An advertised-but-unresolvable approach is the
// silent-no-op shape — Build() accepts the selection and the delivery writes
// nothing.
func TestApproachDispatch_SupportedIsResolvable(t *testing.T) {
	for _, name := range nativeSurfaceBackends(t) {
		t.Run(name, func(t *testing.T) {
			set := BuildSurfaces(name, agent.SurfaceInputs{Context: "ctx"}, afero.NewMemMapFs())
			for _, kind := range allSurfaceKinds {
				for _, a := range set.SupportedApproaches(kind) {
					d, err := set.SurfaceFor(kind, a)
					assert.NoError(t, err, "%s: %s advertises %s but SurfaceFor rejects it", name, kind, a)
					assert.NotNil(t, d, "%s: %s via %s resolved to a nil Delivery", name, kind, a)
				}
			}
		})
	}
}
