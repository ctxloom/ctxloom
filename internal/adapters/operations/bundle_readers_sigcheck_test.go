package operations

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/content"
	"github.com/ctxloom/ctxloom/internal/adapters/content/attest"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/composite/compositetest"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// Under --disable-sig-check the owner ruled that an installed signed tree
// edited after signing is ACCEPTED. The reader does not decide that: it carries
// the tree as a read whose signature is INVALID, so the generation's gate — the
// same trust option — admits it under a reason of its own. An enforced
// generation's reader still refuses it outright (bundle_readers_tree_test.go).

// waiveGeneration binds a waived Trust to cfg, as an Owner opened
// WithoutSignatureCheck would.
func waiveGeneration(t *testing.T, cfg interface {
	BindTrustForTesting(composite.Trust)
}) {
	t.Helper()
	root, records, retraction := compositetest.Ports()
	waived, err := composite.NewTrust(root, records, retraction, composite.WithoutSignatureCheck())
	require.NoError(t, err)
	cfg.BindTrustForTesting(waived)
}

func TestLoadTreeBundle_UnderTheWaiverAnEditedSignedTreeIsCarriedAsInvalid(t *testing.T) {
	ctx := context.Background()
	c, store, tree, fsys := stageInstalledTree(t)
	signer, pub := treeTestSigner(t)
	require.NoError(t, attest.SignBundle(ctx, store, tree, treeRelease(t, tree), signer))
	editInstalledFragment(t, fsys)
	waiveGeneration(t, c)

	b, read, err := readTreeBundle(t, c, ctx, treeCanonical, treeEntry(), treeTrustRoot("trent@acme.test", pub))

	require.NoError(t, err, "the waived generation carries the edited tree to its gate")
	assert.Equal(t, bundles.SignatureInvalid, read.Signature(), "its signature does not cover these bytes, and the read says so")
	assert.Equal(t, bundles.SignerTrusted, read.Signer(), "a key this machine trusts signed what it was before the edit")
	assert.NotEmpty(t, read.SignatureDetail(), "the mismatch is named")
	assert.Empty(t, b.Signer(), "an edited tree carries no publisher identity")
	assert.Equal(t, "SUBSTITUTED", b.Fragments["house-style"].Content, "the installed bytes are what is read")
}

func TestLoadTreeBundle_UnderTheWaiverAFileAddedAfterSigningIsCarriedAsInvalid(t *testing.T) {
	ctx := context.Background()
	c, store, tree, fsys := stageInstalledTree(t)
	signer, pub := treeTestSigner(t)
	require.NoError(t, attest.SignBundle(ctx, store, tree, treeRelease(t, tree), signer))
	dir, err := treeBundleDir(treeBase, treeCanonical)
	require.NoError(t, err)
	testsupport.WriteFile(t, fsys, filepath.Join(dir, "SMUGGLED.txt"), []byte("x"), 0o644)
	waiveGeneration(t, c)

	_, read, err := readTreeBundle(t, c, ctx, treeCanonical, treeEntry(), treeTrustRoot("trent@acme.test", pub))

	require.NoError(t, err)
	assert.Equal(t, bundles.SignatureInvalid, read.Signature())
}

func TestLoadTreeBundle_UnderTheWaiverAnEditedManifestIsCarriedAsInvalid(t *testing.T) {
	ctx := context.Background()
	c, store, tree, fsys := stageInstalledTree(t)
	signer, pub := treeTestSigner(t)
	require.NoError(t, attest.SignBundle(ctx, store, tree, treeRelease(t, tree), signer))
	dir, err := treeBundleDir(treeBase, treeCanonical)
	require.NoError(t, err)
	p := filepath.Join(dir, content.ManifestPath)
	raw, err := afero.ReadFile(fsys, p)
	require.NoError(t, err)
	testsupport.WriteFile(t, fsys, p, append(raw, []byte("# a local note\n")...), 0o644)
	waiveGeneration(t, c)

	_, read, err := readTreeBundle(t, c, ctx, treeCanonical, treeEntry(), treeTrustRoot("trent@acme.test", pub))

	require.NoError(t, err, "a manifest edited after signing is an edit too")
	assert.Equal(t, bundles.SignatureInvalid, read.Signature())
}

// A manifest in the RETIRED format is not an edit: the publisher signed it that
// way and re-pulling fetches the same bytes. The waiver says nothing about it.
func TestLoadTreeBundle_UnderTheWaiverASupersededManifestIsStillWithheld(t *testing.T) {
	ctx := context.Background()
	c, store, tree, fsys := stageInstalledTree(t)
	signer, pub := treeTestSigner(t)
	require.NoError(t, attest.SignBundle(ctx, store, tree, treeRelease(t, tree), signer))
	rewriteManifestMarker(t, fsys, content.DigestVersionMarker)
	waiveGeneration(t, c)

	_, _, err := readTreeBundle(t, c, ctx, treeCanonical, treeEntry(), treeTrustRoot("trent@acme.test", pub))

	require.ErrorIs(t, err, bundles.ErrTreeBundleWithheld)
	assert.ErrorIs(t, err, content.ErrManifestSuperseded)
}
