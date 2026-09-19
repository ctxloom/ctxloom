package backends

import (
	"sort"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/present"
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

// nativeSurfaceBackends is every registered backend with a REAL (non-empty)
// Declaration — exactly the backends Declared answers with something other
// than an empty agent.Declaration, which is the same `surfaces != nil`
// predicate Declared itself branches on.
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
	for name, r := range records {
		d := &r.host
		if len(d.Surfaces) > 0 {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	require.NotEmpty(t, names, "no registered backend has a real Declaration; this gate would range over nothing")
	return names
}

// TestApproachDispatch_DefaultIsDeclared is the parity gate for every
// engine's Declaration: for every kind an engine declares, its default is one
// of the names it declares — a NAMED default, compared by identity. It
// deliberately does NOT assert a position: Names() is sorted and carries no
// meaning, and no reader anywhere needs the default first (help and
// completion mark it by identity, the builder checks membership). The
// older "default is the FIRST advertised approach" contract was the bridge
// between two vocabularies; with one vocabulary there is nothing to bridge.
//
// It is written against agent.Declaration, not against any backend's concrete
// types, so it keeps gating a backend added later.
func TestApproachDispatch_DefaultIsDeclared(t *testing.T) {
	for _, name := range nativeSurfaceBackends(t) {
		t.Run(name, func(t *testing.T) {
			decl := Declared(name)

			anyDeclared := false
			for _, kind := range allSurfaceKinds {
				names := decl.Names(kind)
				def, ok := decl.Default(kind)

				if len(names) == 0 {
					assert.False(t, ok, "%s: %s declares no approach, so Default must report absent/folded", name, kind)
					continue
				}
				anyDeclared = true
				assert.True(t, ok, "%s: %s declares approaches, so it must have a default", name, kind)
				assert.Contains(t, names, def,
					"%s: %s's default must be one of its declared names", name, kind)
			}
			assert.True(t, anyDeclared, "%s: a native-surface backend must declare at least one approach", name)
		})
	}
}

// TestApproachDispatch_DeclaredIsConstructible pins the other half of the
// pair: every name a backend DECLARES must actually construct a non-nil
// Approach that presents somewhere. A declared-but-unconstructible name is
// the silent-no-op shape — Build accepts the selection and the delivery
// writes nothing.
func TestApproachDispatch_DeclaredIsConstructible(t *testing.T) {
	for _, name := range nativeSurfaceBackends(t) {
		t.Run(name, func(t *testing.T) {
			decl := Declared(name)
			for _, kind := range allSurfaceKinds {
				for _, n := range decl.Names(kind) {
					a, ok := decl.Construct(kind, n, agent.SurfaceInputs{Context: "ctx"}, afero.NewMemMapFs())
					require.True(t, ok, "%s: %s declares %s but Construct rejects it", name, kind, n)
					require.NotNil(t, a, "%s: %s via %s constructed a nil Approach", name, kind, n)
					// Present must not panic against advised roots; a Rider may
					// legitimately present nothing.
					_ = a.Present(present.ProjectOnHost("/p"))
				}
			}
		})
	}
}

// TestApproachDispatch_SharedPreferenceIsUnambiguous guards the one arm of
// the shared-cwd default derivation that fails loud at launch: a kind
// declaring MORE THAN ONE approach with an out-of-cwd form, none of them the
// default. No registered engine may reach it — an engine that rich has to
// name its preference, and this is where that requirement is checked before
// a user finds it at launch.
func TestApproachDispatch_SharedPreferenceIsUnambiguous(t *testing.T) {
	for _, name := range nativeSurfaceBackends(t) {
		t.Run(name, func(t *testing.T) {
			decl := Declared(name)
			for _, kind := range allSurfaceKinds {
				var converting []string
				def, _ := decl.Default(kind)
				for _, n := range decl.Names(kind) {
					a, _ := decl.Construct(kind, n, agent.SurfaceInputs{Context: "ctx"}, afero.NewMemMapFs())
					if _, ok := a.(agent.OutOfCwd); ok {
						converting = append(converting, n)
					}
				}
				if len(converting) > 1 {
					assert.Contains(t, converting, def,
						"%s: %s declares several out-of-cwd approaches (%v); its default must be one of them", name, kind, converting)
				}
			}
		})
	}
}
