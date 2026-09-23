package operations

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/paths"
)

// Tree-form AUTHORING.
//
// The defect: ctxloom could not author the shape its own reader accepts.
// CreateBundle only ever wrote <name>.yaml, and CreateSkill registered an
// inline `skills:` key — the retired shape bundles.readEnvelope refuses — so
// the only directory-form authoring verb produced a bundle that would not load.
//
// EVERY ASSERTION BELOW READS THE TREE BACK THROUGH THE PROJECT READER. Checking
// that files appeared would pass against a tree the reader refuses, which is
// precisely the bug being fixed.

// treeAuthoringFixture stages a config over a REAL temp directory. It is not a
// memory filesystem because the single-file create path this suite also covers
// reserves its path through os directly, and a fixture that only half-applied
// would quietly test two different filesystems.
func treeAuthoringFixture(t *testing.T) (afero.Fs, *config.Config, string) {
	t.Helper()
	appPath := filepath.Join(t.TempDir(), ".ctxloom")
	return afero.NewOsFs(), gatedFixture(config.Fixture{AppPaths: []string{appPath}}), appPath
}

// readBackTree resolves the authored bundle through the PROJECT READER — the
// production local read path — so a tree the reader refuses fails here.
func readBackTree(t *testing.T, fsys afero.Fs, appPath, name string) bundles.BundleRead {
	t.Helper()
	// The reader takes the bundles ROOT and expands the format roots itself.
	dirs := []string{paths.LocalBundlesPath(appPath)}
	cat := bundles.NewLoader(bundles.NewProjectReader(fsys, dirs)).Catalog()
	read, err := cat.Lookup(name)
	require.NoError(t, err, "the authored tree must be readable by the reader that refuses the retired shape")
	return read
}

// TestCreateBundle_Tree_AuthorsAShapeTheReaderAccepts is the settling test for
// the authoring half. Before it, no ctxloom verb could produce this.
func TestCreateBundle_AuthorsAShapeTheReaderAccepts(t *testing.T) {
	fsys, cfg, appPath := treeAuthoringFixture(t)
	res, err := CreateBundle(context.Background(), cfg, CreateBundleRequest{
		Name:        "vault",
		Description: "the vault bundle",
		FS:          fsys,
		Fragments: map[string]BundleFragmentInput{
			"house-style": {Content: "FRAG-BODY-MARKER", NoDistill: true},
		},
	})
	require.NoError(t, err)

	// It landed in the v2 layout, as a tree.
	wantDir := filepath.Join(paths.LocalBundlesPathFor(appPath, paths.LayoutV2), "vault")
	assert.Equal(t, filepath.Join(wantDir, bundles.DirectoryFormManifest), res.Path)
	assertEnvelopeDeclaresNoItems(t, fsys, res.Path)

	// The EFFECT: the reader serves the item's BYTES. A tree that was written
	// but does not load would satisfy every assertion above this one.
	read := readBackTree(t, fsys, appPath, "vault")
	frag, ok := read.Bundle.Fragments["house-style"]
	require.True(t, ok, "the authored fragment must resolve from the tree")
	assert.Equal(t, "FRAG-BODY-MARKER", frag.Content)
	assert.Equal(t, "the vault bundle", read.Bundle.Description)
}

// TestCreateBundle_WithNoItems_ScaffoldsAnEnvelopeOnlyTree: a bundle created
// empty is the author's scaffold — its envelope alone — and it loads.
func TestCreateBundle_WithNoItems_ScaffoldsAnEnvelopeOnlyTree(t *testing.T) {
	fsys, cfg, appPath := treeAuthoringFixture(t)
	res, err := CreateBundle(context.Background(), cfg, CreateBundleRequest{
		Name:        "hollow",
		Description: "nothing yet",
		FS:          fsys,
	})
	require.NoError(t, err)
	assertEnvelopeDeclaresNoItems(t, fsys, res.Path)

	read := readBackTree(t, fsys, appPath, "hollow")
	assert.Equal(t, "nothing yet", read.Bundle.Description)
	assert.Empty(t, read.Bundle.Fragments)
}

// assertEnvelopeDeclaresNoItems: a tree keeps its items in files, never
// inline in its envelope.
func assertEnvelopeDeclaresNoItems(t *testing.T, fsys afero.Fs, envelope string) {
	t.Helper()
	_, env, err := bundles.EnvelopeAt(fsys, envelope)
	require.NoError(t, err)
	assert.Empty(t, env.Fragments)
	assert.Empty(t, env.Commands)
	assert.Empty(t, env.MCP)
	assert.Empty(t, env.Skills)
	assert.Empty(t, env.Profiles)
	assert.False(t, env.Hooks.HasAny())
}

// TestCreateBundle_Tree_RefusesAnExistingBundle: create is "write only if
// absent", and what an overwrite destroys is authored content nobody has a copy
// of.
func TestCreateBundle_RefusesAnExistingBundle(t *testing.T) {
	fsys, cfg, appPath := treeAuthoringFixture(t)
	req := CreateBundleRequest{
		Name: "vault",
		FS:   fsys,
		Fragments: map[string]BundleFragmentInput{
			"house-style": {Content: "ORIGINAL-MARKER", NoDistill: true},
		},
	}
	_, err := CreateBundle(context.Background(), cfg, req)
	require.NoError(t, err)

	req.Fragments = map[string]BundleFragmentInput{"house-style": {Content: "OVERWRITE-MARKER", NoDistill: true}}
	_, err = CreateBundle(context.Background(), cfg, req)
	require.Error(t, err)

	// The original bytes survived, which is the fact that matters — not the error.
	read := readBackTree(t, fsys, appPath, "vault")
	assert.Equal(t, "ORIGINAL-MARKER", read.Bundle.Fragments["house-style"].Content)
}

// TestCreateSkill_InATree_WritesNoInlineSkillsKey is the second half of the
// authoring defect. CreateSkill registered every skill in bundle.yaml's
// `skills:` map — the retired inline shape bundles.readEnvelope REFUSES — so
// adding a skill to a tree scaffolded the package and made the whole bundle
// stop loading. The scaffold succeeded and the bundle disappeared.
func TestCreateSkill_InATree_WritesNoInlineSkillsKey(t *testing.T) {
	fsys, cfg, appPath := treeAuthoringFixture(t)
	_, err := CreateBundle(context.Background(), cfg, CreateBundleRequest{
		Name: "vault",
		FS:   fsys,
		Fragments: map[string]BundleFragmentInput{
			"house-style": {Content: "FRAG-BODY-MARKER", NoDistill: true},
		},
	})
	require.NoError(t, err)

	res, err := CreateSkill(context.Background(), cfg, CreateSkillRequest{
		Bundle: "vault", Name: "reviewer", Description: "Reviews things.", FS: fsys,
	})
	require.NoError(t, err)
	assert.Equal(t, "created", res.Status)

	// The envelope still declares nothing inline.
	assertEnvelopeDeclaresNoItems(t, fsys, filepath.Join(paths.LocalBundlesPathFor(appPath, paths.LayoutV2), "vault", bundles.DirectoryFormManifest))

	// The EFFECT: the bundle still LOADS, and the skill is enumerated from the
	// tree without any envelope registration. Asserting only that `skills:` is
	// absent would pass against a bundle that no longer loads at all.
	read := readBackTree(t, fsys, appPath, "vault")
	_, ok := read.Bundle.Skills["reviewer"]
	assert.True(t, ok, "a tree enumerates its skill package from the filesystem")
	assert.Equal(t, "FRAG-BODY-MARKER", read.Bundle.Fragments["house-style"].Content,
		"the fragment authored before the skill must still resolve")
}
