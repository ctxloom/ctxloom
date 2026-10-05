package bundletree

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"path"
	"strings"
	"testing"

	"github.com/Masterminds/semver/v3"
	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/ctxloom/ctxloom/internal/adapters/content"
	"github.com/ctxloom/ctxloom/internal/adapters/content/attest"
	"github.com/ctxloom/ctxloom/internal/adapters/signing"
	"github.com/ctxloom/ctxloom/internal/adapters/signing/allowedsigners"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/release"
)

// Signing is what happens to a fixture tree between being written and being
// read: the signature facts on the read are whatever a production reader
// establishes over the result, never stated by the fixture.
type Signing int

const (
	// Unsigned leaves the tree with no manifest.
	Unsigned Signing = iota
	// SignedByTrustedKey signs the tree with a key the reader's trust root
	// authorizes to publish as Publisher.
	SignedByTrustedKey
	// SignedByUntrustedKey signs the tree with a key the reader does not trust.
	SignedByUntrustedKey
	// EditedAfterTrustedSigning is SignedByTrustedKey followed by an edit to
	// an item file the manifest covers.
	EditedAfterTrustedSigning
	// EditedAfterUntrustedSigning is SignedByUntrustedKey followed by the
	// same edit.
	EditedAfterUntrustedSigning
)

// Publisher is the principal a SignedByTrustedKey tree is trusted to publish as.
const Publisher = "publisher@example.test"

// editedFragment is the item an Edited* fixture adds and then alters, so the
// edit never depends on what the caller's bundle happens to carry.
const editedFragment = "signed-then-edited"

func (s Signing) signed() bool  { return s != Unsigned }
func (s Signing) trusted() bool { return s == SignedByTrustedKey || s == EditedAfterTrustedSigning }
func (s Signing) edited() bool {
	return s == EditedAfterTrustedSigning || s == EditedAfterUntrustedSigning
}

// ProjectRead writes b as this project's bundle name and returns the read the
// project reader establishes for it.
func ProjectRead(t testing.TB, name string, b *bundles.Bundle, s Signing) bundles.BundleRead {
	t.Helper()
	fsys := afero.NewMemMapFs()
	opts := stage(t, fsys, paths.BundlesLayoutRoot("/bundles", paths.LayoutV2), name, b, s)
	return only(t, bundles.NewProjectReader(fsys, []string{"/bundles"}, opts...))
}

// RemoteRead writes b as the pinned tree for the canonical ref
// ("<repo url>@<path>") and returns the read the repofs reader establishes
// for it. An edited tree is read the way a waived generation reads one
// (bundles.WithEditedTreesCarried); an enforced reader refuses it outright.
func RemoteRead(t testing.TB, ref string, b *bundles.Bundle, s Signing) bundles.BundleRead {
	t.Helper()
	url, bundlePath, ok := strings.Cut(ref, "@")
	require.True(t, ok, "bundletree: %q is not a canonical <url>@<path> ref", ref)
	const root = "/pinned"
	fsys := afero.NewMemMapFs()
	opts := append(stage(t, fsys, root, path.Base(bundlePath), b, s), bundles.WithRepoURL(url))
	if s.edited() {
		opts = append(opts, bundles.WithEditedTreesCarried())
	}
	tree, err := content.NewAferoTreeFS(fsys, root)
	require.NoError(t, err)
	return only(t, bundles.NewRepoFSReader(tree, ref, opts...))
}

func only(t testing.TB, r bundles.Reader) bundles.BundleRead {
	t.Helper()
	reads, err := r.Read(context.Background())
	require.NoError(t, err)
	require.Len(t, reads, 1, "bundletree: the fixture must read back as exactly one bundle")
	return reads[0]
}

// stage writes b as <root>/<name>/, applies s, and returns the reader options
// that make s's trust real (the trust root, for a trusted key).
func stage(t testing.TB, fsys afero.Fs, root, name string, b *bundles.Bundle, s Signing) []bundles.ReaderOption {
	t.Helper()
	staged := *b
	if staged.Version == "" {
		staged.Version = "1.0.0"
	}
	if s.edited() {
		frags := make(map[string]bundles.BundleFragment, len(b.Fragments)+1)
		for k, v := range b.Fragments {
			frags[k] = v
		}
		frags[editedFragment] = bundles.BundleFragment{ItemBody: bundles.ItemBody{Content: "as signed"}}
		staged.Fragments = frags
	}
	WriteBundle(t, fsys, root, name, &staged)
	if !s.signed() {
		return nil
	}

	signer, trustRoot := publisherKey(t)
	ctx := context.Background()
	st, err := content.NewTreeStore(fsys, root, content.Provenance{IsLocal: true})
	require.NoError(t, err)
	tree, err := st.Open(ctx, content.BundleID(name))
	require.NoError(t, err)
	require.NoError(t, attest.SignBundle(ctx, st, tree, treeRelease(t, tree), signer))

	if s.edited() {
		file := path.Join(root, name, "fragments", editedFragment+".md")
		before, err := afero.ReadFile(fsys, file)
		require.NoError(t, err, "bundletree: the edited fixture must have an item file to edit")
		require.NoError(t, afero.WriteFile(fsys, file, append(before, []byte("\nedited after signing\n")...), 0o644))
	}
	if s.trusted() {
		return []bundles.ReaderOption{bundles.WithTrustRoot(trustRoot)}
	}
	return nil
}

// publisherKey mints a throwaway signing key and the trust root that
// authorizes it to publish as Publisher.
func publisherKey(t testing.TB) (ssh.Signer, *allowedsigners.Store) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := ssh.NewSignerFromSigner(priv)
	require.NoError(t, err)
	sshPub, err := ssh.NewPublicKey(pub)
	require.NoError(t, err)
	return signer, allowedsigners.NewStore(allowedsigners.Entry{
		Principals: []string{Publisher},
		Namespaces: []string{signing.NamespacePublish},
		PublicKey:  sshPub,
	})
}

// treeRelease is the release a publisher signs tree under: its id and the
// version its envelope declares, which a reader requires to match.
func treeRelease(t testing.TB, tree content.Bundle) release.Release {
	t.Helper()
	raw, err := tree.ReadFile(context.Background(), bundles.DirectoryFormManifest)
	require.NoError(t, err)
	env, err := bundles.ParseBundle(raw)
	require.NoError(t, err)
	v, err := semver.StrictNewVersion(env.Version)
	require.NoError(t, err, "bundletree: a signed fixture's version must be strict semver")
	return release.Release{Name: string(tree.ID()), Version: v}
}
