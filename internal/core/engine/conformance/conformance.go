package conformance

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
)

// Run asserts the declarative half of the port for one engine: the
// constructor's Validate holds; the Definition is a value (equal on every
// call); the derived views agree with the typed fields; every declared
// approach, the dynamic one included, is named and offers a root; every
// declared Mode has a grammar. The instance half's properties are asserted
// where the instance half lands.
func Run(t *testing.T, eng engine.Engine) {
	t.Helper()
	def := eng.Root()
	require.NoError(t, def.Validate(), "the constructor's Validate must hold for every registered engine")
	require.Equal(t, def.Name, eng.Root().Name, "the Definition is a value, equal on every call")
	require.Equal(t, def.Modes, eng.Root().Modes)

	s := def.Surfaces()
	require.Equal(t, len(def.Static()), len(s), "Static() and Surfaces() walk the same typed fields")
	for _, k := range def.Static() {
		a, ok := s[k]
		require.True(t, ok, "Static lists %v but Surfaces omits it", k)
		require.True(t, def.Carries(k))
		require.NotEmpty(t, a.Name(), "the %v approach is nameless", k)
		require.NotEmpty(t, a.Traits().Roots, "the %v approach offers no root", k)
	}
	for _, k := range []present.Kind{present.Context, present.MCP, present.Settings, present.Hooks, present.Commands, present.Skills} {
		if !def.Carries(k) {
			_, has := s[k]
			require.False(t, has, "%v is not carried yet appears in Surfaces", k)
		}
	}
	if def.Dynamic != nil {
		require.NotNil(t, def.MCP, "a dynamic approach needs an MCP approach to name the endpoint")
		require.NotEmpty(t, def.Dynamic.Name())
		require.NotEmpty(t, def.Dynamic.Traits().Roots)
	}
	for _, m := range def.Modes {
		g, ok := engine.CLIFor(def.CLI, m)
		require.True(t, ok, "engine declares Mode %v but no CLI grammar for it", m)
		require.NotEmpty(t, g.Binary, "the %v grammar names no binary", m)
	}
}

// SessionFor builds the engine-facing Session a test hands to Instance: a
// throwaway identity, the mode asked for, the engine's declared host
// default posture, and roots under a temp dir with the project root and the
// session home on the host side.
func SessionFor(t *testing.T, eng engine.Engine, mode engine.Mode) engine.Session {
	t.Helper()
	dir := t.TempDir()
	def := eng.Root()
	return engine.Session{
		Identity:   sessions.Identity{Harp: "conformance-" + string(def.Name)},
		Label:      engine.LabelConfig{Label: string(def.Name)},
		Mode:       mode,
		Permission: def.Permissions.HostDefault,
		Roots: present.Paths{
			ProjectRoot: present.Root{Host: dir + "/project", Engine: dir + "/project"},
			EngineHome:  present.Root{Host: dir + "/home", Engine: dir + "/home"},
			CtxloomHome: present.Root{Host: dir + "/ctxloom", Engine: dir + "/ctxloom"},
		},
		WorkDir: dir + "/project",
	}
}
