package cli

import (
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/signing"
)

// PROMOTION is the moment a bundle leaves the tree that vouches for it: a
// signature stops being metadata and becomes the only thing a consumer has.
// These tests establish what `bundle push` actually does with a signature that
// is already sitting beside the bundle on disk — the state `ctxloom bundle
// sign` leaves behind.
//
// They were CHARACTERIZATION when written — two of them pinned a `push` that
// dropped the author's signature on the floor. That gap is now closed: a
// signature belongs to the BUNDLE, so every publishing path carries a valid
// sidecar and refuses a stale one. These tests pin the OUTCOME a user sees
// (what reached the remote, what the command said, what the refusal tells them
// to do); the full sidecar x flags x sign.default grid, and its equality with
// `bundle move`, lives in publish_carry_matrix_test.go.

// localSigPath returns the on-disk path of the "for-push" bundle's detached
// signature sibling in a pushSignTestSetup project.
func localSigPath(cfg *config.Config) string {
	return filepath.Join(paths.LocalBundlesPath(cfg.GetAppPaths()[0]), "for-push.yaml.sig")
}

// signLocalBundleOnDisk signs the "for-push" bundle's exact current bytes and
// writes the sibling `.sig`, exactly as `ctxloom bundle sign for-push` does.
func signLocalBundleOnDisk(t *testing.T, cfg *config.Config) []byte {
	t.Helper()
	path := filepath.Join(paths.LocalBundlesPath(cfg.GetAppPaths()[0]), "for-push.yaml")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := ssh.NewSignerFromSigner(priv)
	require.NoError(t, err)
	armored, err := signing.Sign(data, signer, signing.NamespacePublish)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path+".sig", armored, 0o644))
	return armored
}

// THE GAP THAT WAS: `ctxloom bundle move --to <remote>` read the sibling
// `.sig`, proved it covered the bytes and published the pair, while `ctxloom
// bundle push` never looked at the sidecar at all — so `ctxloom bundle sign foo
// && ctxloom bundle push foo` published UNSIGNED, exit 0, output silent, and
// the signature the author deliberately made was irrelevant.
//
// Now both carry. What this test adds over the matrix row of the same shape is
// the USER-VISIBLE half: the command says "Signed: yes", the bytes that reached
// the remote verify under a real trust root, and the local sidecar was not
// disturbed by the publish.
func TestPushBundleCfg_LocallySignedBundle_PlainPushCarriesTheSignature(t *testing.T) {
	cfg, pub, mgr := pushSignTestSetup(t)
	discoverer, _ := discovererWithSoleAgentIdentity(t)
	armored := signLocalBundleOnDisk(t, cfg)
	require.FileExists(t, localSigPath(cfg), "precondition: the bundle is signed on disk")

	cmd, out := testCmd()
	require.NoError(t, pushBundleCfg(cmd, cfg, discoverer, mgr, "for-push", "", false, "", false, false))

	main, published := pub.files[".ctxloom/content/bundles/for-push.yaml"]
	require.True(t, published, "the bundle itself is published")
	sig, sigPublished := pub.files[".ctxloom/content/bundles/for-push.yaml.sig"]
	require.True(t, sigPublished, "the author's signature travels with the bundle")
	assert.Equal(t, armored, sig, "carried byte-for-byte, never re-signed")
	assert.NoError(t, signing.CoversBytes(main, sig, signing.NamespacePublish),
		"the published pair verifies over the published bytes")
	assert.Contains(t, out.String(), "Signed: yes", "and the author is told it travelled")

	// A publish is not an edit: the local sidecar is exactly as `bundle sign`
	// left it.
	onDisk, err := os.ReadFile(localSigPath(cfg))
	require.NoError(t, err)
	assert.Equal(t, armored, onDisk)
}

// The stale variant. A bundle edited after signing leaves a signature over
// bytes that no longer exist; publishing that pair hands every consumer a
// tamper alarm for an attack that never happened, and publishing the bytes
// alone silently discards the author's own signal that they are about to ship
// something they did not re-review.
//
// `bundle move --to <remote>` has always refused it
// (operations.TestMoveBundle_ToRemote_StaleSignature_RefusesAndPublishesNothing);
// push now refuses identically. The assertion that matters beyond "it errors"
// is that the message names the two ways out — there are exactly two, and a
// refusal that does not say so just blocks the user.
func TestPushBundleCfg_StaleLocalSignature_PlainPushRefusesAndNamesTheRemedy(t *testing.T) {
	cfg, pub, mgr := pushSignTestSetup(t)
	discoverer, _ := discovererWithSoleAgentIdentity(t)
	signLocalBundleOnDisk(t, cfg)

	// The edit that strands the signature.
	bundlePath := filepath.Join(paths.LocalBundlesPath(cfg.GetAppPaths()[0]), "for-push.yaml")
	edited := []byte("version: 2.0.0\nfragments:\n  intro:\n    content: rewritten\n")
	require.NoError(t, os.WriteFile(bundlePath, edited, 0o644))
	sig, err := os.ReadFile(localSigPath(cfg))
	require.NoError(t, err)
	require.Error(t, signing.CoversBytes(edited, sig, signing.NamespacePublish),
		"precondition: the signature must not cover the edited bytes")

	cmd, _ := testCmd()
	err = pushBundleCfg(cmd, cfg, discoverer, mgr, "for-push", "", false, "", false, false)
	require.Error(t, err, "push refuses a bundle whose local signature is stale")
	assert.Contains(t, err.Error(), "no longer covers")
	assert.Contains(t, err.Error(), "ctxloom bundle sign for-push", "remedy 1: re-sign")
	assert.Contains(t, err.Error(), "publish it unsigned", "remedy 2: drop the sidecar")

	assert.Empty(t, pub.files, "a refused push publishes nothing at all")
	assert.FileExists(t, localSigPath(cfg), "and touches nothing on disk")
}

// --sign on a stale sidecar is the one-command way out: it re-signs first, so
// the sidecar covers the current bytes again and the push proceeds. This is
// strictly better than the old mint model, which published a fresh signature
// while leaving the STALE one on disk — local and remote disagreeing about what
// was signed, with nothing saying so.
func TestPushBundleCfg_StaleLocalSignature_SignFlagResignsAndProceeds(t *testing.T) {
	cfg, pub, mgr := pushSignTestSetup(t)
	discoverer, _ := discovererWithSoleAgentIdentity(t)
	stale := signLocalBundleOnDisk(t, cfg)

	bundlePath := filepath.Join(paths.LocalBundlesPath(cfg.GetAppPaths()[0]), "for-push.yaml")
	edited := []byte("version: 2.0.0\nfragments:\n  intro:\n    content: rewritten\n")
	require.NoError(t, os.WriteFile(bundlePath, edited, 0o644))

	cmd, _ := testCmd()
	require.NoError(t, pushBundleCfg(cmd, cfg, discoverer, mgr, "for-push", "", false, "", true, false))

	main := pub.files[".ctxloom/content/bundles/for-push.yaml"]
	sig, ok := pub.files[".ctxloom/content/bundles/for-push.yaml.sig"]
	require.True(t, ok)
	assert.Equal(t, edited, main)
	assert.NoError(t, signing.CoversBytes(main, sig, signing.NamespacePublish))

	// The local sidecar was REPLACED, so the tree and the remote now agree.
	onDisk, err := os.ReadFile(localSigPath(cfg))
	require.NoError(t, err)
	assert.NotEqual(t, stale, onDisk, "the stale sidecar must not survive a --sign push")
	assert.Equal(t, sig, onDisk, "what is on disk is what was published")
}

// The other half, and the one that must NOT change without a decision: an
// UNSIGNED bundle promotes to a remote successfully today, by design.
//
// This is load-bearing. "Promotion requires a valid signature" would make this
// an error — and would thereby delete `--no-sign` (spec §7A.3), the
// `agentkey.NoKeyError` hint that literally reads "To publish unsigned anyway:
// ctxloom bundle push <bundle> --no-sign", and the entire reason
// EffectiveTrust has a pending state and ctxloom has a `review` command:
// docs/trust-model.md is explicit that "third-party unsigned remotes default to
// pending; their content is reviewed like…". Unsigned publishing is not an
// oversight in this codebase; it is the input to the review model.
func TestPushBundleCfg_UnsignedBundle_PromotesSuccessfully(t *testing.T) {
	cfg, pub, mgr := pushSignTestSetup(t)
	discoverer, _ := discovererWithSoleAgentIdentity(t)
	require.NoFileExists(t, localSigPath(cfg), "precondition: nothing is signed")

	cmd, _ := testCmd()
	require.NoError(t, pushBundleCfg(cmd, cfg, discoverer, mgr, "for-push", "", false, "", false, false))

	_, published := pub.files[".ctxloom/content/bundles/for-push.yaml"]
	assert.True(t, published, "promoting unsigned content is supported by design — the consumer reviews it")
	_, sigPublished := pub.files[".ctxloom/content/bundles/for-push.yaml.sig"]
	assert.False(t, sigPublished)
}
