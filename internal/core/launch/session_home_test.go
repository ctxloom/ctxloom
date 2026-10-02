package launch_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/launch/launchtest"
	"github.com/ctxloom/ctxloom/internal/core/paths"
)

// fixtureEngine is launchtest's fixture engine, as the registry hands it out.
func fixtureEngine(t *testing.T, opts ...launchtest.Option) engine.Engine {
	t.Helper()
	e, ok := launchtest.Deps(t, opts...).Deps.Engines.Lookup(launchtest.EngineName)
	require.True(t, ok)
	return e
}

// TestSessionHome_IsOneRuleForEveryEngine pins the session-home rule (C8):
// <sessionDir>/home/<leaf> for EVERY engine — the declared subdir for one
// that relocates its home, its name for one that relocates nothing — and no
// session home at all for engine_home: host, relocating or not.
func TestSessionHome_IsOneRuleForEveryEngine(t *testing.T) {
	dir := t.TempDir()
	relocating := fixtureEngine(t, launchtest.RelocatableHome())
	plain := fixtureEngine(t)
	homes := filepath.Join(dir, paths.SessionEngineHomesDirName)
	cases := []struct {
		name string
		eng  engine.Engine
		mode agents.HomeMode
		want string
		ok   bool
	}{
		{"a relocating engine is placed at its declared subdir", relocating, agents.HomeModeSession, filepath.Join(homes, ".fixture"), true},
		{"an engine that relocates nothing is placed at its name", plain, agents.HomeModeSession, filepath.Join(homes, string(launchtest.EngineName)), true},
		{"the zero mode is the session", plain, "", filepath.Join(homes, string(launchtest.EngineName)), true},
		{"engine_home: host has no session home", relocating, agents.HomeModeHost, "", false},
		{"engine_home: host on an engine that relocates nothing has none either", plain, agents.HomeModeHost, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := launch.SessionHome(dir, tc.eng, tc.mode)
			require.Equal(t, tc.ok, ok)
			require.Equal(t, tc.want, got)
		})
	}
	_, ok := launch.SessionHome("", plain, agents.HomeModeSession)
	require.False(t, ok, "a run with no session dir has nowhere to put a session home")
}

// TestResolve_TheCellsSessionHomeIsTheRule: the resolver's cell (launchtest's
// roots builder) advises exactly the session home the rule names for the
// request it was handed.
func TestResolve_TheCellsSessionHomeIsTheRule(t *testing.T) {
	for _, relocates := range []bool{true, false} {
		var opts []launchtest.Option
		if relocates {
			opts = append(opts, launchtest.RelocatableHome())
		}
		env := launchtest.Deps(t, opts...)
		l, err := launch.Resolve(context.Background(), env.Deps, launch.Source{Identity: env.Identity, Mode: engine.Structured, Permission: "bypass", Prompt: "x", WorkDir: env.Project})
		require.NoError(t, err)
		req := env.LastCellRequest()
		want, ok := launch.SessionHome(req.SessionDir, req.Engine, agents.HomeMode(req.HomeMode))
		require.True(t, ok, "a session-home run has a session home")
		require.Equal(t, want, l.Cell.Paths.Paths().SessionHome.Host, "relocates=%v", relocates)
	}
}
