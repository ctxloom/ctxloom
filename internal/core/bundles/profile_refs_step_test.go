package bundles

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/schemaver"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

const (
	fetchBundleRef     = "https://github.com/o/r@bundles/core"
	canonicalBundleRef = "ctxloom+git://github.com/o/r//bundles/core"
	fetchParentRef     = "https://github.com/o/r@bundles/kit#profiles/base"
	canonicalParentRef = "ctxloom+git://github.com/o/r//bundles/kit#profiles/base"
)

// profileRefsGeneration names the generation profileRefsStep lands on, which
// the tree reader and the write-backs gate on; this keeps the constant and the
// step's position in envelopeKind one fact.
func TestProfileRefsGeneration_IsWhereProfileRefsStepLands(t *testing.T) {
	_, ok := envelopeKind.Steps[profileRefsGeneration-1-envelopeKind.Oldest].(profileRefsStep)
	assert.True(t, ok)
}

const profileDoc = "bundles:\n  - " + fetchBundleRef + "\nparents:\n  - " + fetchParentRef + "\n"

// An inline envelope older than the step has its profiles' stored refs moved
// onto the canonical URI grammar; a current one is read as written — the step
// runs because the generation says so.
func TestParseBundle_ProfileRefsStep(t *testing.T) {
	inline := func(gen int) []byte {
		return []byte(fmt.Sprintf("%s: %d\nversion: 1.0.0\nprofiles:\n  dev:\n    bundles:\n      - %s\n    parents:\n      - %s\n",
			schemaver.Key, gen, fetchBundleRef, fetchParentRef))
	}

	old, err := ParseBundle(inline(profileRefsGeneration - 1))
	require.NoError(t, err)
	assert.Equal(t, []string{canonicalBundleRef}, old.Profiles["dev"].Bundles)
	assert.Equal(t, []string{canonicalParentRef}, old.Profiles["dev"].Parents)

	current, err := ParseBundle(inline(profileRefsGeneration))
	require.NoError(t, err)
	assert.Equal(t, []string{fetchBundleRef}, current.Profiles["dev"].Bundles, "a current envelope is not migrated")
}

// writeProfileTree writes a tree whose envelope declares gen and whose one
// profile item is profileDoc, and returns its envelope path.
func writeProfileTree(t *testing.T, fs afero.Fs, gen int) string {
	t.Helper()
	dir := filepath.Join(paths.BundlesLayoutRoot("/bundles", paths.LayoutV2), "kit")
	envelope := filepath.Join(dir, DirectoryFormManifest)
	require.NoError(t, fs.MkdirAll(filepath.Join(dir, paths.ProfilesDir), 0o755))
	testsupport.WriteFileString(t, fs, envelope, fmt.Sprintf("%s: %d\nversion: 1.0.0\n", schemaver.Key, gen), 0o644)
	testsupport.WriteFileString(t, fs, filepath.Join(dir, paths.ProfilesDir, "dev.yaml"), profileDoc, 0o644)
	return envelope
}

// A tree's item files carry no format key: the envelope's generation is
// theirs. A tree whose envelope predates the step has its profiles migrated by
// the same rule; a current tree's are read as written.
func TestReadTree_ProfileItemsFollowTheEnvelopeGeneration(t *testing.T) {
	for _, tc := range []struct {
		gen  int
		want []string
	}{
		{profileRefsGeneration - 1, []string{canonicalBundleRef}},
		{profileRefsGeneration, []string{fetchBundleRef}},
	} {
		t.Run(fmt.Sprint(tc.gen), func(t *testing.T) {
			fs := afero.NewMemMapFs()
			writeProfileTree(t, fs, tc.gen)

			b, err := NewLoader(NewProjectReader(fs, []string{"/bundles"})).Load("kit")
			require.NoError(t, err)
			assert.Equal(t, tc.want, b.Profiles["dev"].Bundles)
		})
	}
}

// Persisting an envelope's migration past the step migrates the tree's
// profile items too: a current envelope over items still in the old grammar
// would claim a migration that never happened.
func TestUpgradeEnvelopeAt_MigratesProfileItems(t *testing.T) {
	fs := afero.NewMemMapFs()
	envelope := writeProfileTree(t, fs, profileRefsGeneration-1)

	res, err := UpgradeEnvelopeAt(fs, envelope)
	require.NoError(t, err)
	assert.Equal(t, envelopeKind.Current(), res.To)

	item, err := afero.ReadFile(fs, filepath.Join(filepath.Dir(envelope), paths.ProfilesDir, "dev.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(item), canonicalBundleRef)
	assert.Contains(t, string(item), canonicalParentRef)
	assert.NotContains(t, string(item), fetchBundleRef)
}
