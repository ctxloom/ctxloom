package isolation

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
	"github.com/ctxloom/ctxloom/internal/engines/mock"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// multiVarEngine is a fixture engine declaring a home var that names the
// session home and two further vars beneath it (one nested), with its facts
// staged for the test so isolation prepares its home as it would a real
// engine's.
func multiVarEngine(t *testing.T) engine.Engine {
	t.Helper()
	eng := mock.NewNamed("homevars-fixture", mock.WithHome(engine.HomeSpec{
		Vars: []engine.HomeVar{
			{Name: "FIXTURE_HOME", Subdir: "fixture"},
			{Name: "FIXTURE_CACHE", Subdir: "cache"},
			{Name: "FIXTURE_CONFIG", Subdir: ".xdg/config"},
		},
		Auth: engine.Absent[engine.Auth]("the fixture authenticates against no vendor"),
	}))
	stageEngineFacts(t, string(eng.Root().Name), func(f *EngineFacts) { *f = FactsOf(eng) })
	return eng
}

// THE Q8 RULE, on both runtimes: every declared home var is bound — the
// first at the session home itself, each further one at its Subdir beneath
// it — as a home binding (in declaration order) and as env. A further var's
// directory is created owner-only inside the home, so a container reaches
// it through the home's one mount and needs no mount of its own.
func TestHomeVars_EveryDeclaredVarIsBound(t *testing.T) {
	for name, r := range map[string]relocator{"host": hostRelocator{}, "container": containerOf} {
		t.Run(name, func(t *testing.T) {
			home := fakeHostHome(t, "")
			eng := multiVarEngine(t)
			s := homeSpec(t, eng, home, harpA, agents.HomeModeSession)

			pl, mounts := placeOn(t, s, t.TempDir(), r)
			sh := pl.Paths.Paths().SessionHome
			require.NotEmpty(t, sh.Engine)
			beneath := func(rel string) string {
				return present.Presentation{HostPath: sh.Host, EnginePath: sh.Engine}.Beneath(rel).EnginePath
			}
			want := []engine.HomeBinding{
				{Var: "FIXTURE_HOME", Path: sh.Engine},
				{Var: "FIXTURE_CACHE", Path: beneath("cache")},
				{Var: "FIXTURE_CONFIG", Path: beneath(".xdg/config")},
			}
			assert.Equal(t, want, pl.Home)
			for _, b := range want {
				assert.Equal(t, b.Path, pl.Env[b.Var], "%s reaches the engine's env", b.Var)
			}
			for _, rel := range []string{"cache", ".xdg", ".xdg/config"} {
				dir := filepath.Join(sh.Host, filepath.FromSlash(rel))
				info, err := os.Stat(dir)
				require.NoError(t, err, "%s is created before the engine starts", rel)
				if runtime.GOOS != "windows" {
					assert.Equal(t, os.FileMode(0o700), info.Mode().Perm(), "%s is owner-only", rel)
				}
			}
			for _, m := range mounts {
				assert.NotContains(t, []string{want[1].Path, want[2].Path}, m.Container, "a further var rides the home's mount")
			}
			assert.Empty(t, strictness.All())
		})
	}
}

// A curated host env keeps every declared var's name, not only the first:
// on the real home the engine finds the human's config through each of them.
func TestHomeVars_ACuratedEnvKeepsEveryVarName(t *testing.T) {
	eng := multiVarEngine(t)
	s, err := NewSpec(launch.Axes{}, eng).Project(t.TempDir()).
		Session(t.TempDir(), SessionState{Harp: harpA}).EnvHost(agents.EnvHost{Curated: true, Env: []string{"PATH"}}).Build()
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"PATH", "FIXTURE_HOME", "FIXTURE_CACHE", "FIXTURE_CONFIG"}, s.curatedEnv().Env)
}

// homeEntries lists dir's top-level names, sorted.
func homeEntries(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	require.NoError(t, err)
	var names []string
	for _, e := range es {
		names = append(names, e.Name())
	}
	slices.Sort(names)
	return names
}

// CHARACTERIZATION (written before the multi-var change): claude declares
// one home var, and on both runtimes it binds exactly that var at the
// session home itself, as the placement's one home binding and its one env
// entry, and the prepared home holds what it held before and nothing a
// further var would add.
func TestHomeVars_ClaudeBindsItsOneVarAtTheSessionHome(t *testing.T) {
	for name, r := range map[string]relocator{"host": hostRelocator{}, "container": containerOf} {
		t.Run(name, func(t *testing.T) {
			home := fakeHostHome(t, "")
			s := homeSpec(t, claudeEngine(t), home, harpA, agents.HomeModeSession)

			pl, _ := placeOn(t, s, t.TempDir(), r)
			engineHome := pl.Paths.Paths().SessionHome.Engine
			require.NotEmpty(t, engineHome)
			assert.Equal(t, []engine.HomeBinding{{Var: claude.ConfigDirEnv, Path: engineHome}}, pl.Home)
			assert.Equal(t, map[string]string{claude.ConfigDirEnv: engineHome}, pl.Env)
			assert.Equal(t, []string{claude.InstanceConfigFileName, "projects"}, homeEntries(t, claudeHome(home, harpA)))
			assert.Empty(t, strictness.All())
		})
	}
}
