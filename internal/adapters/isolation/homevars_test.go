package isolation

import (
	"os"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

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
