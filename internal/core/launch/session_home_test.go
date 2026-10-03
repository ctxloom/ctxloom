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

// TestNativeHome_SitsBesideTheSessionHome pins the native-history rule:
// <sessionDir>/native/<leaf>, the SAME leaf as the session home at the same
// depth, so the home's relative link resolves on the host and in a container
// that mounts the two as siblings — and only for an engine that keeps a
// history store, and only when there is a session home to link from.
func TestNativeHome_SitsBesideTheSessionHome(t *testing.T) {
	dir := t.TempDir()
	relocating := fixtureEngine(t, launchtest.RelocatableHome())
	plain := fixtureEngine(t)

	got, ok := launch.NativeHome(dir, relocating, agents.HomeModeSession)
	require.True(t, ok)
	require.Equal(t, filepath.Join(dir, paths.NativeDirName, ".fixture"), got)
	home, _ := launch.SessionHome(dir, relocating, agents.HomeModeSession)
	rel, err := filepath.Rel(filepath.Dir(home), got)
	require.NoError(t, err)
	require.Equal(t, filepath.Join("..", paths.NativeDirName, ".fixture"), rel, "siblings at the same depth")

	_, ok = launch.NativeHome(dir, plain, agents.HomeModeSession)
	require.False(t, ok, "an engine that keeps no history store has no native home")
	_, ok = launch.NativeHome(dir, relocating, agents.HomeModeHost)
	require.False(t, ok, "engine_home: host has no session home to link from")
	_, ok = launch.NativeHome("", relocating, agents.HomeModeSession)
	require.False(t, ok)
}
