package bundles

import (
	"context"
	"path"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/ctxloom/ctxloom/internal/adapters/content"
	"github.com/ctxloom/ctxloom/internal/adapters/content/attest"
	"github.com/ctxloom/ctxloom/internal/adapters/signing"
	"github.com/ctxloom/ctxloom/internal/core/trust"
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
// signer, when non-nil, writes and signs SHA256SUMS over the finished tree, so
// "signed" is real crypto over the real manifest rather than a fixture
// convention. The items are written with content.Writer, the same object the
// publisher uses, so the tree's layout cannot drift from the product's.
func repoTree(t *testing.T, leaf, envelope string, frags map[string]string, signer ssh.Signer) TreeFS {
	t.Helper()
	fsys, root := stageRepoTree(t, leaf, envelope, frags, signer)
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

// stageRepoTree writes the tree and returns the filesystem it lives on, so a
// caller that needs to disturb the bytes AFTER signing can reach them.
func stageRepoTree(t *testing.T, leaf, envelope string, frags map[string]string, signer ssh.Signer) (afero.Fs, string) {
	t.Helper()
	fsys := afero.NewMemMapFs()
	require.NoError(t, fsys.MkdirAll(repoTreeRoot, 0o755))
	st, err := content.NewTreeStore(fsys, repoTreeRoot, content.Provenance{IsLocal: true})
	require.NoError(t, err)
	for name, body := range frags {
		require.NoError(t, st.Put(context.Background(),
			trust.Ref{Bundle: leaf, Kind: trust.KindFragment, Name: name},
			signing.FormRaw,
			content.Fragment{Name: name, ItemMeta: content.ItemMeta{Body: body}}))
	}
	require.NoError(t, st.PutRootFile(context.Background(), content.BundleID(leaf), DirectoryFormManifest, []byte(envelope)))
	if signer != nil {
		b, err := st.Open(context.Background(), content.BundleID(leaf))
		require.NoError(t, err)
		require.NoError(t, attest.SignBundle(context.Background(), st, b, signer))
	}
	return fsys, repoTreeRoot
}

// openRepoTree serves a staged tree as the TreeFS a repofs reader reads through.
func openRepoTree(t *testing.T, fsys afero.Fs, root string) TreeFS {
	t.Helper()
	tfs, err := content.NewAferoTreeFS(fsys, root)
	require.NoError(t, err)
	return tfs
}

// repoTreeTamperedAfterSigning stages a SIGNED tree and then edits one item
// file, leaving the manifest and its signature untouched: the publisher's key
// still verifies over the manifest, and the manifest no longer describes the
// tree. That is the two-axis case — trusted key, moved bytes.
//
// It REQUIRES that it found a file to edit. A tamper helper that silently
// tampered with nothing would make every assertion built on it vacuous, which
// is the exact failure mode this package has already been bitten by.
func repoTreeTamperedAfterSigning(t *testing.T, leaf, envelope string, frags map[string]string, signer ssh.Signer) TreeFS {
	t.Helper()
	fsys, root := stageRepoTree(t, leaf, envelope, frags, signer)
	dir := path.Join(root, leaf, "fragments")
	entries, err := afero.ReadDir(fsys, dir)
	require.NoError(t, err)
	require.NotEmpty(t, entries, "the fixture must have staged a fragment to tamper with")
	target := path.Join(dir, entries[0].Name())
	before, err := afero.ReadFile(fsys, target)
	require.NoError(t, err)
	require.NoError(t, afero.WriteFile(fsys, target, append(before, []byte("\nsubstituted\n")...), 0o644))
	return openRepoTree(t, fsys, root)
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
// where the signature facts decide anything.
func seedLocal(seeded map[string]*Bundle) Reader {
	const (
		prov = ProvenanceProject
		tctx = TrustCtxLocal
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
		reads = append(reads, newRead(ref, b, prov, tctx,
			signatureFacts{signature: SignatureNone, signer: SignerNone}))
	}
	return staticReader{reads: reads}
}

// seedRemote presents already-parsed bundles as REMOTE (pinned, repofs-read)
// content, one real repoFSReader per seed entry, keyed by canonical ref
// ("https://…@bundles/<name>"). Unlike seedLocal — TrustCtxLocal by design,
// documented as the wrong tool when a test's premise is specifically about
// remote-vs-local trust identity — this goes through the REAL reader, so a
// bundle's typed SourceRef is minted through the actual class minter
// (ClassGit/ClassFile via canonicalBundleRefTyped), not forced through
// LocalRef the way seedLocal's newRead fallback would.
func seedRemote(t *testing.T, seeded map[string]*Bundle) []Reader {
	t.Helper()
	var readers []Reader
	for ref, b := range seeded {
		if b == nil {
			continue
		}
		if b.Name == "" {
			b.Name = ref
		}
		// A seed becomes a TREE, because that is the only form a repofs reader
		// accepts. Fragments are the only kind any seed has ever carried, and
		// an unhandled kind FAILS here rather than being dropped: a seed whose
		// commands silently vanished would make whatever it was seeded for pass
		// while testing nothing.
		if len(b.Commands) > 0 || len(b.Skills) > 0 || len(b.MCP) > 0 || len(b.Profiles) > 0 || b.Hooks.HasAny() {
			t.Fatalf("seedRemote: %q carries a kind this helper does not stage as tree items; teach it that kind rather than losing them", ref)
		}
		frags := map[string]string{}
		for name, f := range b.Fragments {
			frags[name] = f.Content
		}
		leaf := path.Base(strings.TrimSuffix(ref, "/"))
		envelope := "name: " + b.Name + "\nversion: 1.0.0\n"
		// The repo URL is the canonical ref's own prefix, so a seed claims the
		// origin it names rather than a fixture constant that could disagree
		// with the ref the gate keys trust by.
		repoURL, _, found := strings.Cut(ref, "@")
		if !found {
			t.Fatalf("seedRemote: %q is not a canonical remote ref, so it has no repo URL to claim", ref)
		}
		readers = append(readers, NewRepoFSReader(repoTree(t, leaf, envelope, frags, nil), ref, WithRepoURL(repoURL)))
	}
	return readers
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
