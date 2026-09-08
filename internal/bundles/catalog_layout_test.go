package bundles

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/content"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/signing"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// Precedence between the two on-disk bundle SHAPES a single format root can
// hold: a bare document ("<name>.yaml") and a tree ("<name>/bundle.yaml" plus
// item files).
//
// Before format v1 was removed, a name colliding across shapes collided across
// ROOTS too (v1's document root vs v2's tree root), and bundleLayoutPrecedence
// gave the walk a deliberate, documented tiebreak (v2 first) plus a way to
// REPORT the shadowed alternative (Located.AlsoIn, diffed by layout). With one
// live format there is exactly one root, so a same-named document and tree are
// now SIBLING ENTRIES in the SAME walk — precedence between them is decided by
// afero.Walk's traversal order, not by any layout list, and AlsoIn (which only
// ever diffs LAYOUTS) cannot see the collision at all: both entries report
// LayoutV2. That is a real, unguarded loss of the old cross-format
// determinism and reporting — see this package's v1-removal report for the
// finding — so these tests no longer assert a winner between the two shapes.
// What they still pin: a document-only or tree-only name resolves correctly,
// a colliding name still produces exactly ONE read (dedup holds), and search
// directory order still dominates shape.

// stageBothShapes writes the same bundle name as BOTH a document and a tree
// under one bundles root. The two bodies differ so a test can tell which one
// answered, without asserting which one MUST.
func stageBothShapes(t *testing.T, root, name, docBody, treeBody string) afero.Fs {
	t.Helper()
	fsys := afero.NewMemMapFs()
	v2root := paths.BundlesLayoutRoot(root, paths.LayoutV2)
	require.NoError(t, fsys.MkdirAll(v2root, 0o755))

	st, err := content.NewTreeStore(fsys, v2root, content.Provenance{IsLocal: true})
	require.NoError(t, err)
	putFragmentIn(st, name, "marker", treeBody)
	require.NoError(t, st.PutRootFile(context.Background(), content.BundleID(name), DirectoryFormManifest,
		[]byte("name: "+name+"\nversion: 2.0.0\n")))

	writeDocument(t, fsys, root, name, docBody)
	return fsys
}

// writeDocument writes the single-file document form at the bundles root.
func writeDocument(t *testing.T, fsys afero.Fs, root, name, body string) {
	t.Helper()
	doc := "name: " + name + "\nversion: 1.0.0\nfragments:\n  marker:\n    content: " + body + "\n"
	testsupport.WriteFileString(t, fsys,
		filepath.Join(paths.BundlesLayoutRoot(root, paths.LayoutV2), name+".yaml"), doc, 0o644)
}

// putFragmentIn writes one fragment into a NAMED bundle, so a fixture can
// stage the same bundle name in two shapes with different bodies.
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

// TestLocate_DocumentAloneStillResolvesAndSaysSo: a name that exists only as a
// bare document must resolve by its BARE name — the ".yaml" is a file suffix,
// never part of what a caller asks for — and must report v2 (the only format)
// with nothing shadowed.
func TestLocate_DocumentAloneStillResolvesAndSaysSo(t *testing.T) {
	fsys := afero.NewMemMapFs()
	require.NoError(t, fsys.MkdirAll("/bundles", 0o755))
	writeDocument(t, fsys, "/bundles", "solo", "ONLY-DOC-MARKER")

	loc, b := locate(t, fsys, "/bundles", "solo")
	assert.Equal(t, paths.LayoutV2, loc.Layout)
	assert.Empty(t, loc.AlsoIn, "a name in one shape has no other layout to report")
	assert.Equal(t, filepath.Join(paths.BundlesLayoutRoot("/bundles", paths.LayoutV2), "solo.yaml"), loc.Path)
	assert.Equal(t, "ONLY-DOC-MARKER", b.Fragments["marker"].Content)
}

// TestLocate_TreeAloneResolves: a bundle that exists ONLY as a tree resolves
// by its bare name, with nothing shadowed.
func TestLocate_TreeAloneResolves(t *testing.T) {
	fsys := afero.NewMemMapFs()
	v2root := paths.BundlesLayoutRoot("/bundles", paths.LayoutV2)
	require.NoError(t, fsys.MkdirAll(v2root, 0o755))
	st, err := content.NewTreeStore(fsys, v2root, content.Provenance{IsLocal: true})
	require.NoError(t, err)
	putFragmentIn(st, "fresh", "marker", "ONLY-TREE-MARKER")
	require.NoError(t, st.PutRootFile(context.Background(), "fresh", DirectoryFormManifest, []byte("name: fresh\nversion: 2.0.0\n")))

	loc, b := locate(t, fsys, "/bundles", "fresh")
	assert.Equal(t, paths.LayoutV2, loc.Layout)
	assert.Empty(t, loc.AlsoIn)
	assert.Equal(t, "ONLY-TREE-MARKER", b.Fragments["marker"].Content)
}

// TestRead_ACollidingNameProducesExactlyOneRead: whichever shape the walk
// visits first for a colliding name, `seen` dedup must keep the count at one —
// a name must never surface twice under one format root.
func TestRead_ACollidingNameProducesExactlyOneRead(t *testing.T) {
	fsys := stageBothShapes(t, "/bundles", "vault", "DOC-BODY-MARKER", "TREE-BODY-MARKER")
	reads, err := NewProjectReader(fsys, []string{"/bundles"}).Read(context.Background())
	require.NoError(t, err)

	refs := make([]string, 0, len(reads))
	for _, r := range reads {
		refs = append(refs, r.ref)
	}
	assert.Equal(t, []string{"vault"}, refs,
		"one bundle name, one read, whichever shape the walk found first")
}

// TestSearchDirPrecedenceStillDominatesShape: search directory order is a
// property of the DIR-MAJOR loop in searchRoots, independent of how many
// shapes or layouts exist within a directory. An earlier directory still wins
// outright over a later one, regardless of which shape each holds.
func TestSearchDirPrecedenceStillDominatesShape(t *testing.T) {
	fsys := afero.NewMemMapFs()
	require.NoError(t, fsys.MkdirAll("/first", 0o755))
	writeDocument(t, fsys, "/first", "vault", "FIRST-DIR-DOC-MARKER")

	second := paths.BundlesLayoutRoot("/second", paths.LayoutV2)
	require.NoError(t, fsys.MkdirAll(second, 0o755))
	st, err := content.NewTreeStore(fsys, second, content.Provenance{IsLocal: true})
	require.NoError(t, err)
	putFragmentIn(st, "vault", "marker", "SECOND-DIR-TREE-MARKER")
	require.NoError(t, st.PutRootFile(context.Background(), "vault", DirectoryFormManifest, []byte("name: vault\nversion: 2.0.0\n")))

	cat := NewLoader(NewProjectReader(fsys, []string{"/first", "/second"})).Catalog()
	loc, err := cat.Locate("vault")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(paths.BundlesLayoutRoot("/first", paths.LayoutV2), "vault.yaml"), loc.Path,
		"the first search directory wins even though the second holds a tree")
}

// TestFind_DelegatesToLocate keeps the two answers from drifting: Find is
// Locate with the layout discarded, and a second resolution body is exactly how
// they would come to disagree.
func TestFind_DelegatesToLocate(t *testing.T) {
	fsys := stageBothShapes(t, "/bundles", "vault", "DOC-BODY-MARKER", "TREE-BODY-MARKER")
	cat := NewLoader(NewProjectReader(fsys, []string{"/bundles"})).Catalog()
	loc, err := cat.Locate("vault")
	require.NoError(t, err)
	path, ferr := cat.Find("vault")
	require.NoError(t, ferr)
	assert.Equal(t, loc.Path, path)
}
