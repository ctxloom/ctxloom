package bundles

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Masterminds/semver/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/content"
	"github.com/ctxloom/ctxloom/internal/adapters/content/attest"
	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/core/release"
)

// stageVersionedTree stages an unsigned tree whose bundle.yaml declares
// version, and returns its store and bundle for the caller to sign.
func stageVersionedTree(t *testing.T, version string) (*content.TreeStore, content.Bundle) {
	t.Helper()
	fsys, root := stageRepoTree(t, "kit", "version: "+version+"\n", map[string]string{"a": "body\n"}, nil)
	st, err := content.NewTreeStore(fsys, root, content.Provenance{IsLocal: true})
	require.NoError(t, err)
	b, err := st.Open(context.Background(), "kit")
	require.NoError(t, err)
	return st, b
}

func TestVerifyRemoteTree_ReturnsTheSignedReleaseAndPublisher(t *testing.T) {
	st, b := stageVersionedTree(t, "1.4.0")
	signer, root, _ := treeSignerFor(t, "pub@example.test")
	require.NoError(t, attest.SignBundle(context.Background(), st, b, treeRelease(t, b), signer))

	got, err := verifyRemoteTree(context.Background(), b, root, "bundles/kit", "abc")
	require.NoError(t, err)
	assert.Equal(t, "pub@example.test", got.Publisher)
	assert.Equal(t, "kit", got.Release.Name)
	assert.Equal(t, "1.4.0", got.Release.Version.String())
}

// bundle.yaml's version is the authored input and the signed header is what
// the floor is measured in. If the two disagree a consumer would display one
// version and enforce another, so the tree is withheld.
func TestVerifyRemoteTree_RefusesABundleYamlVersionTheSignatureDoesNotCarry(t *testing.T) {
	st, b := stageVersionedTree(t, "1.4.0")
	signer, root, _ := treeSignerFor(t, "pub@example.test")
	rel := release.Release{Name: "kit", Version: semver.MustParse("2.0.0")}
	require.NoError(t, attest.SignBundle(context.Background(), st, b, rel, signer))

	_, err := verifyRemoteTree(context.Background(), b, root, "bundles/kit", "abc")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrTreeBundleWithheld), "got %v", err)
	assert.Contains(t, err.Error(), "1.4.0")
	assert.Contains(t, err.Error(), "2.0.0")
}

func TestVerifyRemoteTree_UnattestedStillRefusesByName(t *testing.T) {
	_, b := stageVersionedTree(t, "1.4.0")
	_, root, _ := treeSignerFor(t, "pub@example.test")
	_, err := verifyRemoteTree(context.Background(), b, root, "bundles/kit", "abc")
	assert.True(t, errors.Is(err, ErrTreeUnattested), "got %v", err)
}

// fetchedTreeOf reads every file of b into the map a tree fetch produces.
func fetchedTreeOf(t *testing.T, b content.Bundle) map[string]remote.TreeFile {
	t.Helper()
	files, err := b.Files(context.Background())
	require.NoError(t, err)
	out := map[string]remote.TreeFile{}
	for _, f := range files {
		data, err := b.ReadFile(context.Background(), f)
		require.NoError(t, err)
		out[f] = remote.TreeFile{Data: data}
	}
	return out
}

func TestTreeVerifier_ReportsTheSignedReleaseForAPull(t *testing.T) {
	st, b := stageVersionedTree(t, "1.4.0")
	signer, root, _ := treeSignerFor(t, "pub@example.test")
	require.NoError(t, attest.SignBundle(context.Background(), st, b, treeRelease(t, b), signer))

	got, err := TreeVerifier(root)(context.Background(), fetchedTreeOf(t, b), ".ctxloom/content/bundles/v2/kit", "abc", repoTreeURL)
	require.NoError(t, err)
	assert.Equal(t, "pub@example.test", got.Publisher)
	assert.Equal(t, "1.4.0", got.Release.Version.String())
}

// Unattested content is pinnable — it takes the review path at exposure — so
// the verifier admits it with no floor rather than refusing it.
func TestTreeVerifier_AdmitsUnattestedWithNoFloor(t *testing.T) {
	_, b := stageVersionedTree(t, "1.4.0")
	_, root, _ := treeSignerFor(t, "pub@example.test")
	got, err := TreeVerifier(root)(context.Background(), fetchedTreeOf(t, b), ".ctxloom/content/bundles/v2/kit", "abc", repoTreeURL)
	require.NoError(t, err)
	assert.Empty(t, got.Publisher)
	assert.Nil(t, got.Release.Version)
}

// Attack (c) at pull time: kit's signed tree served at another bundle's path.
func TestTreeVerifier_RefusesASignedTreeServedUnderAnotherBundlesPath(t *testing.T) {
	st, b := stageVersionedTree(t, "1.4.0")
	signer, root, _ := treeSignerFor(t, "pub@example.test")
	require.NoError(t, attest.SignBundle(context.Background(), st, b, treeRelease(t, b), signer))

	_, err := TreeVerifier(root)(context.Background(), fetchedTreeOf(t, b), ".ctxloom/content/bundles/v2/impostor", "abc", repoTreeURL)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrTreeBundleWithheld), "got %v", err)
	assert.Contains(t, err.Error(), `signed as "kit"`)
}

// manifestAndSigs is what the retraction check fetches from a tip: the
// SHA256SUMS and the .sigs/ entries filed against it, by file name.
func manifestAndSigs(t *testing.T, b content.Bundle) ([]byte, map[string][]byte) {
	t.Helper()
	raw, err := b.ReadFile(context.Background(), content.ManifestPath)
	require.NoError(t, err)
	sigs := map[string][]byte{}
	files, err := b.Files(context.Background())
	require.NoError(t, err)
	for _, f := range files {
		if name, ok := strings.CutPrefix(f, content.SigDirName+"/"); ok {
			data, err := b.ReadFile(context.Background(), f)
			require.NoError(t, err)
			sigs[name] = data
		}
	}
	return raw, sigs
}

func TestManifestVerifier_ASignedTipNamesItsPublisherAndRelease(t *testing.T) {
	st, b := stageVersionedTree(t, "1.4.0")
	signer, root, _ := treeSignerFor(t, "pub@example.test")
	rel := treeRelease(t, b)
	rel.Retracts = []release.Retraction{{Version: semver.MustParse("1.1.0"), Reason: "leaked token"}}
	require.NoError(t, attest.SignBundle(context.Background(), st, b, rel, signer))
	raw, sigs := manifestAndSigs(t, b)

	got, err := ManifestVerifier(root)(raw, sigs)
	require.NoError(t, err)
	assert.Equal(t, "pub@example.test", got.Publisher)
	assert.Equal(t, "kit", got.Release.Name)
	retracted, why := got.Release.Retracted(semver.MustParse("1.1.0"))
	assert.True(t, retracted)
	assert.Equal(t, "leaked token", why)
}

func TestManifestVerifier_UnsignedOrUntrustedSaysNothing(t *testing.T) {
	st, b := stageVersionedTree(t, "1.4.0")
	signer, _, _ := treeSignerFor(t, "pub@example.test")
	_, otherRoot, _ := treeSignerFor(t, "someone-else@example.test")
	require.NoError(t, attest.SignBundle(context.Background(), st, b, treeRelease(t, b), signer))
	raw, sigs := manifestAndSigs(t, b)

	got, err := ManifestVerifier(otherRoot)(raw, sigs)
	require.NoError(t, err)
	assert.Empty(t, got.Publisher)

	got, err = ManifestVerifier(otherRoot)(raw, nil)
	require.NoError(t, err)
	assert.Empty(t, got.Publisher)
}

func TestManifestVerifier_EditedBytesAreRefused(t *testing.T) {
	st, b := stageVersionedTree(t, "1.4.0")
	signer, root, _ := treeSignerFor(t, "pub@example.test")
	require.NoError(t, attest.SignBundle(context.Background(), st, b, treeRelease(t, b), signer))
	raw, sigs := manifestAndSigs(t, b)
	edited := []byte(strings.Replace(string(raw), "# version: 1.4.0", "# version: 9.4.0", 1))

	_, err := ManifestVerifier(root)(edited, sigs)
	require.Error(t, err, "a trusted key's signature over other bytes is a tamper, never a clean answer")
}
