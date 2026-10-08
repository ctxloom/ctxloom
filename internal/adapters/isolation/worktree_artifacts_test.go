package isolation

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/git"
	"github.com/ctxloom/ctxloom/internal/adapters/gitignore"
)

// The per-agent worktree exclude set is ctxloom's own artifacts plus the
// union of what every REGISTERED engine declares it writes into a working
// tree (engine.Definition.ProjectArtifacts) — read through the registry, so
// an engine nobody told the gitignore package about is covered the moment it
// is registered.
//
// MUTATION -- drop the engines' ProjectArtifacts from
// WorktreeArtifactPatterns -- turns this red.
func TestWorktreeArtifactPatterns_AreCtxloomsOwnPlusEveryRegisteredEnginesDeclaredPaths(t *testing.T) {
	stageEngineFacts(t, "fake-engine", func(f *EngineFacts) {
		f.ProjectArtifacts = []string{".fake-engine/", "FAKE_CONTEXT.md"}
	})

	want := slices.Clone(gitignore.CtxloomWorktreePatterns)
	for _, name := range factNames() {
		f, _ := factsFor(name)
		for _, p := range f.ProjectArtifacts {
			if !slices.Contains(want, p) {
				want = append(want, p)
			}
		}
	}
	got := WorktreeArtifactPatterns()
	assert.ElementsMatch(t, want, got)
	assert.Contains(t, got, ".fake-engine/")
	assert.Contains(t, got, "FAKE_CONTEXT.md")
}

// The exclude block a worktree writes carries a registered engine's declared
// paths: the fake engine's land without the gitignore package naming it.
func TestWorktree_ExcludeConfigFromMerge_CarriesARegisteredEnginesDeclaredPaths(t *testing.T) {
	stageEngineFacts(t, "fake-engine", func(f *EngineFacts) {
		f.ProjectArtifacts = []string{".fake-engine/"}
	})
	common := t.TempDir()
	sessionWorktree(t, &git.Fake{CommonDirValue: common}).excludeConfigFromMerge(context.Background(), "/proj")

	raw, err := os.ReadFile(filepath.Join(common, "info", "exclude"))
	require.NoError(t, err)
	assert.Contains(t, strings.Split(string(raw), "\n"), ".fake-engine/")
}
