package backends

import (
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"

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

// nativeSurfaceBackends is every registered backend that builds a real
// (non-Empty) SurfaceSet — the four whose SupportedApproaches/DefaultApproach
// pair is served by the shared agent.TableDispatch carrier.
var nativeSurfaceBackends = []string{"claude-code", "codex", "kiro", "opencode"}

// TestApproachDispatch_DefaultIsFirstSupported is the parity gate for
// approach dispatch.
//
// Written per backend, SupportedApproaches and DefaultApproach come to EIGHT
// bodies — once per (backend × method) — each a one-liner naming that backend's
// own table. Eight hand-written bodies can disagree with each other and with the
// contract cells.go states for them: "DefaultApproach reports the approach WithEverything
// selects for kind — the backend's native realization. false means kind is
// absent/folded for this backend."
//
// This test states that contract ONCE and holds all four backends to it, so the
// single shared agent.TableDispatch carrier is checked against behaviour rather
// than against a diff. It is deliberately written against the
// agent.SurfaceSet interface, not against any backend's concrete Surfaces, so it
// keeps gating a fifth backend added later.
func TestApproachDispatch_DefaultIsFirstSupported(t *testing.T) {
	for _, name := range nativeSurfaceBackends {
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
	for _, name := range nativeSurfaceBackends {
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
