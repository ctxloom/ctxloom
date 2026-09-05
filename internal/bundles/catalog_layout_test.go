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

// Layout precedence between the two on-disk bundle forms.
//
// WHY THIS IS TESTED BY EFFECT AND NOT BY PATH ALONE: the failure being pinned
// is a migration that "appears to succeed and changes nothing" — the tree is
// written, the monolith keeps answering, and every surface reports success. A
// test that only compared paths would pass against a reader that resolved the
// right file and then decoded the wrong bundle, so each assertion below also
// reaches the BODY that was served.

// stageBothLayouts writes the same bundle name in BOTH layouts under one
// bundles root: a v1 single-file document, and a v2 tree carrying one fragment.
// The two bodies differ so a test can say which one answered.
func stageBothLayouts(t *testing.T, root, name, v1Body, v2Body string) afero.Fs {
	t.Helper()
	fsys := afero.NewMemMapFs()
	v2root := paths.BundlesLayoutRoot(root, paths.LayoutV2)
	require.NoError(t, fsys.MkdirAll(v2root, 0o755))

	st, err := content.NewTreeStore(fsys, v2root, content.Provenance{IsLocal: true})
	require.NoError(t, err)
	putFragmentIn(st, name, "marker", v2Body)
	require.NoError(t, st.PutRootFile(context.Background(), content.BundleID(name), DirectoryFormManifest,
		[]byte("name: "+name+"\nversion: 2.0.0\n")))

	writeV1(t, fsys, root, name, v1Body)
	return fsys
}

// writeV1 writes the single-file document form into the v1 layout root.
func writeV1(t *testing.T, fsys afero.Fs, root, name, body string) {
	t.Helper()
	doc := "name: " + name + "\nversion: 1.0.0\nfragments:\n  marker:\n    content: " + body + "\n"
	testsupport.WriteFileString(t, fsys, filepath.Join(paths.BundlesLayoutRoot(root, paths.LayoutV1), name+".yaml"), doc, 0o644)
}

// putFragmentIn writes one fragment into a NAMED bundle, so a fixture can
// stage the same bundle name in two layouts with different bodies.
func putFragmentIn(w content.Writer, bundle, name, body string) {
	_ = w.Put(context.Background(),
		trust.Ref{Bundle: bundle, Kind: trust.KindFragment, Name: name},
		signing.FormRaw,
		content.Fragment{Name: name, Body: body})
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

// TestLocate_V2WinsOverV1AndReportsTheOther is the inversion this migration
// turns on. Before the layout split a walk resolved the monolith first, so a
// migrated bundle kept serving its pre-migration body while every surface
// reported the migration had happened.
func TestLocate_V2WinsOverV1AndReportsTheOther(t *testing.T) {
	fsys := stageBothLayouts(t, "/bundles", "vault", "V1-BODY-MARKER", "V2-BODY-MARKER")
	loc, b := locate(t, fsys, "/bundles", "vault")

	assert.Equal(t, paths.LayoutV2, loc.Layout, "v2 must win when a name exists in both layouts")
	assert.Equal(t, []paths.BundleLayout{paths.LayoutV1}, loc.AlsoIn,
		"the shadowed layout must be reported, not silently dropped")
	assert.Equal(t, filepath.Join("/bundles", "v2", "vault", DirectoryFormManifest), loc.Path)

	// The EFFECT: the bytes served are the tree's, not the monolith's. A
	// resolver that returned the v2 path and then decoded the v1 document would
	// satisfy every assertion above and none of this one.
	require.NotNil(t, b)
	frag, ok := b.Fragments["marker"]
	require.True(t, ok, "the v2 tree's fragment must be the one that resolved")
	assert.Equal(t, "V2-BODY-MARKER", frag.Content)
	assert.Equal(t, "2.0.0", b.Version)
}

// TestLocate_V1AloneStillResolvesAndSaysSo: nothing has been migrated yet for
// the overwhelming majority of bundles, so an un-migrated name must keep
// resolving — and must report v1 rather than an empty layout.
func TestLocate_V1AloneStillResolvesAndSaysSo(t *testing.T) {
	fsys := afero.NewMemMapFs()
	require.NoError(t, fsys.MkdirAll("/bundles", 0o755))
	writeV1(t, fsys, "/bundles", "solo", "ONLY-V1-MARKER")

	loc, b := locate(t, fsys, "/bundles", "solo")
	assert.Equal(t, paths.LayoutV1, loc.Layout)
	assert.Empty(t, loc.AlsoIn, "a name in one layout has no other layout to report")
	assert.Equal(t, filepath.Join(paths.BundlesLayoutRoot("/bundles", paths.LayoutV1), "solo.yaml"), loc.Path)
	assert.Equal(t, "ONLY-V1-MARKER", b.Fragments["marker"].Content)
}

// TestLocate_V2AloneResolves: a bundle that exists ONLY as a tree resolves by
// its bare name, with nothing shadowed.
func TestLocate_V2AloneResolves(t *testing.T) {
	fsys := afero.NewMemMapFs()
	v2root := paths.BundlesLayoutRoot("/bundles", paths.LayoutV2)
	require.NoError(t, fsys.MkdirAll(v2root, 0o755))
	st, err := content.NewTreeStore(fsys, v2root, content.Provenance{IsLocal: true})
	require.NoError(t, err)
	putFragmentIn(st, "fresh", "marker", "ONLY-V2-MARKER")
	require.NoError(t, st.PutRootFile(context.Background(), "fresh", DirectoryFormManifest, []byte("name: fresh\nversion: 2.0.0\n")))

	loc, b := locate(t, fsys, "/bundles", "fresh")
	assert.Equal(t, paths.LayoutV2, loc.Layout)
	assert.Empty(t, loc.AlsoIn)
	assert.Equal(t, "ONLY-V2-MARKER", b.Fragments["marker"].Content)
}

// TestRead_V2BundlesAreNotAlsoFoundUnderALayoutQualifiedName. The v1 root
// CONTAINS the v2 root today, so without an explicit exclusion the v1 walk also
// finds every tree and offers it a second time as "v2/<name>" — a second
// resolution identity for one bundle, which is how a rejection recorded against
// one route fails to withhold the other.
func TestRead_V2BundlesAreNotAlsoFoundUnderALayoutQualifiedName(t *testing.T) {
	fsys := stageBothLayouts(t, "/bundles", "vault", "V1-BODY-MARKER", "V2-BODY-MARKER")
	reads, err := NewProjectReader(fsys, []string{"/bundles"}).Read(context.Background())
	require.NoError(t, err)

	refs := make([]string, 0, len(reads))
	for _, r := range reads {
		refs = append(refs, r.ref)
	}
	assert.Equal(t, []string{"vault"}, refs,
		"one bundle name, one read — the v2 tree must not also surface as v2/vault")
}

// TestSearchDirPrecedenceStillDominatesLayout: layout preference is a TIEBREAK
// WITHIN one search directory, not a second axis competing with the search
// path. An earlier directory still wins outright, which is the precedence this
// loader has always had.
func TestSearchDirPrecedenceStillDominatesLayout(t *testing.T) {
	fsys := afero.NewMemMapFs()
	require.NoError(t, fsys.MkdirAll("/first", 0o755))
	writeV1(t, fsys, "/first", "vault", "FIRST-DIR-V1-MARKER")

	second := paths.BundlesLayoutRoot("/second", paths.LayoutV2)
	require.NoError(t, fsys.MkdirAll(second, 0o755))
	st, err := content.NewTreeStore(fsys, second, content.Provenance{IsLocal: true})
	require.NoError(t, err)
	putFragmentIn(st, "vault", "marker", "SECOND-DIR-V2-MARKER")
	require.NoError(t, st.PutRootFile(context.Background(), "vault", DirectoryFormManifest, []byte("name: vault\nversion: 2.0.0\n")))

	cat := NewLoader(NewProjectReader(fsys, []string{"/first", "/second"})).Catalog()
	loc, err := cat.Locate("vault")
	require.NoError(t, err)
	assert.Equal(t, paths.LayoutV1, loc.Layout,
		"the first search directory wins even though the second holds a v2 tree")
	assert.Equal(t, filepath.Join(paths.BundlesLayoutRoot("/first", paths.LayoutV1), "vault.yaml"), loc.Path)
}

// TestFind_DelegatesToLocate keeps the two answers from drifting: Find is
// Locate with the layout discarded, and a second resolution body is exactly how
// they would come to disagree.
func TestFind_DelegatesToLocate(t *testing.T) {
	fsys := stageBothLayouts(t, "/bundles", "vault", "V1-BODY-MARKER", "V2-BODY-MARKER")
	cat := NewLoader(NewProjectReader(fsys, []string{"/bundles"})).Catalog()
	loc, err := cat.Locate("vault")
	require.NoError(t, err)
	path, ferr := cat.Find("vault")
	require.NoError(t, ferr)
	assert.Equal(t, loc.Path, path)
}
