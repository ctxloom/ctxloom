package bundles

import (
	"context"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/content"
	"github.com/ctxloom/ctxloom/internal/core/ident"
)

// repoTree stages a TREE-form bundle named leaf and returns it as the TreeFS a
// repoFSReader reads through: a store rooted at the bundle's PARENT, so the
// reader's own isTreeForm check sees leaf as a directory.
//
// Every repofs fixture goes through here because a bundle is now always a tree
// — the single-document form is refused rather than read — and a fixture that
// still staged a flat "<leaf>.yaml" would be asserting against a shape no
// publisher can publish and no reader will accept.
//
// The items are written with content.Writer, the same object the
// publisher uses, so the tree's layout cannot drift from the product's.
func repoTree(t *testing.T, leaf, envelope string, frags map[string]string) TreeFS {
	t.Helper()
	fsys, root := stageRepoTree(t, leaf, envelope, frags)
	return openRepoTree(t, fsys, root)
}

// repoTreeRoot is the directory the staged tree's PARENT sits at — the root a
// repofs reader is handed, inside which leaf is the bundle's own directory.
const repoTreeRoot = "/pinned"

// repoTreeURL is the publisher repository every staged fixture claims to have
// come from. A tree read opens a content store, and content.Provenance REFUSES
// to default a remote origin — so a repofs fixture must state one, exactly as
// the production caller does.
const repoTreeURL = "https://example.test/repo"

// stageRepoTree writes the tree and returns the filesystem it lives on.
func stageRepoTree(t *testing.T, leaf, envelope string, frags map[string]string) (afero.Fs, string) {
	t.Helper()
	fsys := afero.NewMemMapFs()
	require.NoError(t, fsys.MkdirAll(repoTreeRoot, 0o755))
	st, err := content.NewTreeStore(fsys, repoTreeRoot, content.Provenance{IsLocal: true})
	require.NoError(t, err)
	for name, body := range frags {
		require.NoError(t, st.Put(context.Background(),
			ident.Ref{Bundle: leaf, Kind: ident.KindFragment, Name: name},
			ident.FormRaw,
			content.Fragment{Name: name, ItemMeta: content.ItemMeta{Body: body}}))
	}
	require.NoError(t, st.PutRootFile(context.Background(), content.BundleID(leaf), DirectoryFormManifest, []byte(envelope)))
	return fsys, repoTreeRoot
}

// openRepoTree serves a staged tree as the TreeFS a repofs reader reads through.
func openRepoTree(t *testing.T, fsys afero.Fs, root string) TreeFS {
	t.Helper()
	tfs, err := content.NewAferoTreeFS(fsys, root)
	require.NoError(t, err)
	return tfs
}

// staticReader is the in-package seam a test uses to hand the loader content it
// did not have to write to a filesystem.
//
// It lives in a _test.go file ON PURPOSE. Its constructors mint provenance and
// a trust context, which is exactly what no exported constructor may do — a
// caller anywhere in the tree that could ask for "local, please" would be a
// trust bypass with a struct literal for a weapon. Keeping it here means it is
// compiled only into this package's tests and is unreachable from anywhere
// else.
type staticReader struct{ reads []BundleRead }

func (r staticReader) Read(context.Context) ([]BundleRead, error) { return r.reads, nil }

// seedLocal presents already-parsed bundles as local project content, keyed by
// resolution ref — the shape the retired WithSeededBundles option produced.
//
// Local, not remote, deliberately: a test that wants REMOTE content should go
// through NewRepoFSReader over real bytes, because the remote rows are the ones
// where provenance and locality differ from a project bundle's.
func seedLocal(seeded map[string]*Bundle) Reader {
	const (
		prov = ProvenanceProject
		tctx = LocalityLocal
	)
	var reads []BundleRead
	for ref, b := range seeded {
		if b == nil {
			continue
		}
		// The seed key is the bundle's resolution identity; a bundle that does
		// not carry its own name would compose broken item names ("/<item>"),
		// so backfill from the key. sourceRef/sourceRefTyped are left UNSET
		// here so newRead's only-if-empty fallback stamps both together
		// (string AND typed) from the same ref, exactly as it does for a real
		// localFSReader project bundle — setting sourceRef directly here, as
		// this used to, pre-empted that fallback and left sourceRefTyped
		// permanently zero, which is this test double's own version of the
		// silent-withholding gap loader_version.go's bundleAtVersion had.
		if b.Name == "" {
			b.Name = ref
		}
		reads = append(reads, newRead(ref, b, prov, tctx))
	}
	return staticReader{reads: reads}
}

// loadoutProbe is a CompanionProber over a fixed set of loadouts and no
// candidates — the shape a test that is about what a loadout CONTRIBUTES
// wants. A test about what a companion FAILS to contribute builds a
// CompanionProbe with Candidates itself.
func loadoutProbe(los ...CompanionLoadout) CompanionProber {
	return func(context.Context) (CompanionProbe, error) {
		return CompanionProbe{Loadouts: los}, nil
	}
}
