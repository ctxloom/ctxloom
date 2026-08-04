package operations

import (
	"crypto/ed25519"
	"crypto/rand"
	"path"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/content"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/signing"
	"github.com/ctxloom/ctxloom/internal/signing/allowedsigners"
)

// seedReaders presents authored bundle VALUES as the pinned content a reader
// reads: one reader per ref, over the bundle's own YAML bytes.
//
// It exists because no exported constructor lets a caller mint a provenance or
// a signer — deliberately, since one that did would be a trust bypass wearing a
// struct literal. So a test that wants content in a loader supplies BYTES and
// lets the reader establish the facts, exactly as a session does.
//
// A seed whose bundle carries a Signer() gets a real one: a throwaway key signs
// those exact bytes and the reader is given a trust root that authorizes that
// key for the wanted principal. The stamp therefore survives the round trip the
// only way it can — by being verified — which is the point of the field being
// unexported and yaml:"-".
//
// THE SEED KEY DECIDES WHICH READER. A canonical ref is pinned REMOTE content
// and gets the repofs reader; a bare bundle name is this project's own content
// and gets the project reader. Posture comes from the reader that produced the
// read, never re-derived from the ref string: a fixture whose reader disagrees
// with its identity is a fixture that cannot exercise the rows that key on the
// difference.
func seedReaders(t *testing.T, seed map[string]*bundles.Bundle) []bundles.Reader {
	t.Helper()
	refs := make([]string, 0, len(seed))
	for ref := range seed {
		refs = append(refs, ref)
	}
	sort.Strings(refs)

	projectFS := afero.NewMemMapFs()
	local := false
	var out []bundles.Reader
	for _, ref := range refs {
		b := seed[ref]
		if b == nil {
			continue
		}
		data, err := yaml.Marshal(b)
		require.NoError(t, err)

		if !remote.IsSelfContainedRef(ref) && !strings.Contains(ref, "@") {
			// A bare name is a bundle in this project's own tree.
			require.NoError(t, afero.WriteFile(projectFS, "/bundles/"+ref+".yaml", data, 0o644))
			local = true
			continue
		}

		files := map[string][]byte{path.Base(ref) + ".yaml": data}
		var opts []bundles.ReaderOption
		if principal := b.Signer(); principal != "" {
			sig, root := signAs(t, data, principal)
			files[path.Base(ref)+".yaml"+bundles.SigSuffix] = sig
			opts = append(opts, bundles.WithTrustRoot(root))
		}
		tree, err := content.NewMapTreeFS(files)
		require.NoError(t, err)
		out = append(out, bundles.NewRepoFSReader(tree, ref, opts...))
	}
	if local {
		out = append(out, bundles.NewProjectReader(projectFS, []string{"/bundles"}))
	}
	return out
}

// seedLoader is seedReaders wired into a loader, for the many tests whose only
// interest is "a loader that can see this content".
func seedLoader(t *testing.T, seed map[string]*bundles.Bundle) *bundles.Loader {
	t.Helper()
	return bundles.NewLoader(seedReaders(t, seed)...)
}

// seedUntrustedSigned presents b as pinned content signed by a key NOTHING on
// this machine trusts, and returns the loader plus the fingerprint of the key
// that made the signature — the display-only value a reviewer compares against
// what the publisher told them out of band.
//
// It signs for real rather than stamping a string, because a fingerprint that
// did not come from a key is not the fact the review surface claims to be
// showing.
func seedUntrustedSigned(t *testing.T, ref string, b *bundles.Bundle) (*bundles.Loader, string) {
	t.Helper()
	data, err := yaml.Marshal(b)
	require.NoError(t, err)
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	sshSigner, err := ssh.NewSignerFromSigner(priv)
	require.NoError(t, err)
	sshPub, err := ssh.NewPublicKey(pub)
	require.NoError(t, err)
	sig, err := signing.Sign(data, sshSigner, signing.NamespacePublish)
	require.NoError(t, err)

	tree, err := content.NewMapTreeFS(map[string][]byte{
		path.Base(ref) + ".yaml":                     data,
		path.Base(ref) + ".yaml" + bundles.SigSuffix: sig,
	})
	require.NoError(t, err)
	// No trust root: nothing here trusts the key, which is the state under test.
	return bundles.NewLoader(bundles.NewRepoFSReader(tree, ref)), ssh.FingerprintSHA256(sshPub)
}

// signAs signs data with a throwaway key and returns that signature alongside a
// trust root that authorizes the key to publish as principal.
func signAs(t *testing.T, data []byte, principal string) ([]byte, signing.TrustRoot) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	sshSigner, err := ssh.NewSignerFromSigner(priv)
	require.NoError(t, err)
	sshPub, err := ssh.NewPublicKey(pub)
	require.NoError(t, err)
	sig, err := signing.Sign(data, sshSigner, signing.NamespacePublish)
	require.NoError(t, err)
	return sig, allowedsigners.NewStore(allowedsigners.Entry{
		Principals: []string{principal},
		Namespaces: []string{signing.NamespacePublish},
		PublicKey:  sshPub,
	})
}

// seedTrustedSigned is seedUntrustedSigned's counterpart: b as pinned content
// signed by a key this machine DOES trust to publish as principal.
func seedTrustedSigned(t *testing.T, ref, principal string, b *bundles.Bundle) *bundles.Loader {
	t.Helper()
	data, err := yaml.Marshal(b)
	require.NoError(t, err)
	sig, root := signAs(t, data, principal)
	tree, err := content.NewMapTreeFS(map[string][]byte{
		path.Base(ref) + ".yaml":                     data,
		path.Base(ref) + ".yaml" + bundles.SigSuffix: sig,
	})
	require.NoError(t, err)
	return bundles.NewLoader(bundles.NewRepoFSReader(tree, ref, bundles.WithTrustRoot(root)))
}

// seedTampered presents b as pinned content whose signature was made by a
// trusted key over DIFFERENT bytes — the spec §10.2 downgrade attempt.
func seedTampered(t *testing.T, ref, principal string, b *bundles.Bundle) *bundles.Loader {
	t.Helper()
	data, err := yaml.Marshal(b)
	require.NoError(t, err)
	sig, root := signAs(t, append(append([]byte{}, data...), []byte("# not these bytes\n")...), principal)
	tree, err := content.NewMapTreeFS(map[string][]byte{
		path.Base(ref) + ".yaml":                     data,
		path.Base(ref) + ".yaml" + bundles.SigSuffix: sig,
	})
	require.NoError(t, err)
	return bundles.NewLoader(bundles.NewRepoFSReader(tree, ref, bundles.WithTrustRoot(root)))
}

// readOf resolves ref through loader to the READ its reader produced — the
// value every trust decision now keys on.
func readOf(t *testing.T, loader *bundles.Loader, ref string) bundles.BundleRead {
	t.Helper()
	read, ok := loader.Read(ref)
	require.True(t, ok, "the fixture bundle %q must resolve", ref)
	return read
}
