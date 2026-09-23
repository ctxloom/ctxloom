package bundles

import (
	"context"
	"errors"
	"testing"

	"github.com/Masterminds/semver/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/content"
	"github.com/ctxloom/ctxloom/internal/adapters/content/attest"
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
