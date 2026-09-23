package bundles

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/content"
	"github.com/ctxloom/ctxloom/internal/adapters/signing"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/trust"
)

// Resolution across the bundles root: a bundle is a tree, so a name has one
// place per search directory, and search-directory order decides between
// directories.

// stageTree writes the tree <root>/v2/<name>/ holding one fragment, marker.
func stageTree(t *testing.T, fsys afero.Fs, root, name, body string) {
	t.Helper()
	v2root := paths.BundlesLayoutRoot(root, paths.LayoutV2)
	require.NoError(t, fsys.MkdirAll(v2root, 0o755))
	st, err := content.NewTreeStore(fsys, v2root, content.Provenance{IsLocal: true})
	require.NoError(t, err)
	putFragmentIn(st, name, "marker", body)
	require.NoError(t, st.PutRootFile(context.Background(), content.BundleID(name), DirectoryFormManifest,
		[]byte("name: "+name+"\nversion: 2.0.0\n")))
}

// putFragmentIn writes one fragment into a NAMED bundle.
func putFragmentIn(w content.Writer, bundle, name, body string) {
	_ = w.Put(context.Background(),
		trust.Ref{Bundle: bundle, Kind: trust.KindFragment, Name: name},
		signing.FormRaw,
		content.Fragment{Name: name, ItemMeta: content.ItemMeta{Body: body}})
}

func locate(t *testing.T, fsys afero.Fs, root, name string) (Located, *Bundle) {
	t.Helper()
	cat := NewLoader(NewProjectReader(fsys, []string{root})).Catalog()
	loc, err := cat.Locate(name)
	require.NoError(t, err)
	read, err := cat.Lookup(name)
	require.NoError(t, err)
	return loc, read.Bundle
}

// TestLocate_TreeResolves: a tree resolves by its bare name to its envelope,
// with nothing shadowed.
func TestLocate_TreeResolves(t *testing.T) {
	fsys := afero.NewMemMapFs()
	stageTree(t, fsys, "/bundles", "fresh", "ONLY-TREE-MARKER")

	loc, b := locate(t, fsys, "/bundles", "fresh")
	assert.Equal(t, paths.LayoutV2, loc.Layout)
	assert.Empty(t, loc.AlsoIn)
	assert.Equal(t, filepath.Join(paths.BundlesLayoutRoot("/bundles", paths.LayoutV2), "fresh", DirectoryFormManifest), loc.Path)
	assert.Equal(t, "ONLY-TREE-MARKER", b.Fragments["marker"].Content)
}

// TestSearchDirPrecedence: search directory order is a property of the
// DIR-MAJOR loop in searchRoots. An earlier directory wins outright over a
// later one.
func TestSearchDirPrecedence(t *testing.T) {
	fsys := afero.NewMemMapFs()
	stageTree(t, fsys, "/first", "vault", "FIRST-DIR-MARKER")
	stageTree(t, fsys, "/second", "vault", "SECOND-DIR-MARKER")

	cat := NewLoader(NewProjectReader(fsys, []string{"/first", "/second"})).Catalog()
	loc, err := cat.Locate("vault")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(paths.BundlesLayoutRoot("/first", paths.LayoutV2), "vault", DirectoryFormManifest), loc.Path,
		"the first search directory wins")
}

// TestFind_DelegatesToLocate keeps the two answers from drifting: Find is
// Locate with the layout discarded, and a second resolution body is exactly how
// they would come to disagree.
func TestFind_DelegatesToLocate(t *testing.T) {
	fsys := afero.NewMemMapFs()
	stageTree(t, fsys, "/bundles", "vault", "TREE-BODY-MARKER")
	cat := NewLoader(NewProjectReader(fsys, []string{"/bundles"})).Catalog()
	loc, err := cat.Locate("vault")
	require.NoError(t, err)
	path, ferr := cat.Find("vault")
	require.NoError(t, ferr)
	assert.Equal(t, loc.Path, path)
}
