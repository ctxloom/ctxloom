package config

import (
	"fmt"
	"os"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/profiles"
	"github.com/ctxloom/ctxloom/internal/testsupport/bundletree"
)

// A LOCAL bundle at <bundles>/team/reviews, listed in a profile as
// "team/reviews" while a remote alias "team" is configured, must stay the local
// bundle: the profile loader the config hands out wires the project's local
// bundles in as the local-file-wins oracle. The control profile names a bundle
// that does NOT exist locally, proving the alias would otherwise be applied.
func TestProfileLoader_LocalBundleWinsOverSameSpelledRemoteAlias(t *testing.T) {
	const (
		appDir  = "/proj/.ctxloom"
		teamURL = "https://github.com/acme/team"
	)
	fs := afero.NewMemMapFs()
	bundletree.Write(t, fs, paths.BundlesLayoutRoot(paths.LocalBundlesPath(appDir), paths.LayoutV2),
		"team/reviews", "version: \"1.0\"\ndescription: local reviews\n")
	bundletree.WriteDirProfiles(t, fs, appDir, map[string]any{
		"dev": Profile{Bundles: []string{"team/reviews"}},
		"ctl": Profile{Bundles: []string{"team/absent"}},
	})

	b := NewBuilder(fs, true, appDir, SourceProject)
	b.BindProfileResolvers(func(alias string) string {
		if alias == "team" {
			return teamURL
		}
		return ""
	})
	cfg := b.Build()

	loader := cfg.GetProfileLoader()
	dev, err := loader.Load("dev")
	require.NoError(t, err)
	assert.Equal(t, []string{"team/reviews"}, dev.Bundles, "the local bundle must not be re-pointed at the remote")

	ctl, err := cfg.GetProfileLoader().Load("ctl")
	require.NoError(t, err)
	assert.Equal(t, []string{remote.CanonicalSpelling(teamURL + "@bundles/absent")}, ctl.Bundles, "control: a non-local ref resolves through the alias, canonically spelled")
}

// openCounter counts the opens of one file.
type openCounter struct {
	afero.Fs
	path string
	n    int
}

func (o *openCounter) Open(name string) (afero.File, error) {
	if name == o.path {
		o.n++
	}
	return o.Fs.Open(name)
}

func (o *openCounter) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	if name == o.path {
		o.n++
	}
	return o.Fs.OpenFile(name, flag, perm)
}

// aliasProfileProject is a project whose local bundles team/b0..b2 are named
// by its profiles as "team/<bundle>" refs while the alias "team" is bound:
// each such ref is one local-file-wins question when a loader is built.
func aliasProfileProject(t *testing.T, refs int) (*Config, *openCounter, afero.Fs) {
	t.Helper()
	const appDir = "/proj/.ctxloom"
	mem := afero.NewMemMapFs()
	root := paths.BundlesLayoutRoot(paths.LocalBundlesPath(appDir), paths.LayoutV2)
	envelope := ""
	for i := range 3 {
		envelope = bundletree.Write(t, mem, root, fmt.Sprintf("team/b%d", i), "version: \"1.0\"\ndescription: local\n")
	}
	var bundleRefs []string
	for i := range refs {
		bundleRefs = append(bundleRefs, fmt.Sprintf("team/b%d", i%3))
	}
	bundletree.WriteDirProfiles(t, mem, appDir, map[string]any{"dev": Profile{Bundles: bundleRefs}})
	counter := &openCounter{Fs: mem, path: envelope}
	b := NewBuilder(counter, true, appDir, SourceProject)
	b.BindProfileResolvers(func(alias string) string {
		if alias == "team" {
			return "https://github.com/acme/team"
		}
		return ""
	})
	return b.Build(), counter, mem
}

// Building a loader answers its every local-file-wins question from ONE read
// of the local bundles: the questions come in one synchronous burst, so
// reading every bundle afresh per alias ref is cost with nothing to show for
// it (a project of a few bundles paid a human-visible delay per loader).
func TestProfileLoader_AnswersItsLocalBundleQuestionsFromOneRead(t *testing.T) {
	few, fewOpens, _ := aliasProfileProject(t, 1)
	few.GetProfileLoader()
	many, manyOpens, _ := aliasProfileProject(t, 8)
	many.GetProfileLoader()
	assert.Equal(t, fewOpens.n, manyOpens.n, "a loader reads a local bundle as often for eight alias refs as for one")
}

// A local bundle created after the loader was built is the loader's to write
// into: the read its construction shared answers construction only, never a
// later Save.
func TestProfileLoader_SavesIntoABundleCreatedAfterItWasBuilt(t *testing.T) {
	cfg, _, mem := aliasProfileProject(t, 4)
	loader := cfg.GetProfileLoader()
	bundletree.Write(t, mem, paths.BundlesLayoutRoot(paths.LocalBundlesPath("/proj/.ctxloom"), paths.LayoutV2),
		"team/fresh", "version: \"1.0\"\ndescription: created after the loader\n")
	require.NoError(t, loader.Save(&profiles.Profile{Name: "team/fresh#profiles/x", Description: "x"}))
}
