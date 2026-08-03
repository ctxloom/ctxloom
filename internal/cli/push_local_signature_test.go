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
// They are CHARACTERIZATION, not specification: two of them pin behaviour that
// is arguably wrong (see each test's own note). Pinning it is the point — the
// gap is invisible today, and a test that fails when someone fixes it is a
// cheaper alarm than a user discovering their published bundle lost its
// signature.

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

// FOUND GAP, characterized not fixed. `ctxloom bundle move --to <remote>` reads
// the sibling `.sig`, proves it covers the bytes, and publishes the pair
// (operations.moveToRemote). `ctxloom bundle push` never looks at the sidecar
// at all: pushBundleCfg populates PushBundleRequest.Signer from --sign, and
// leaves PushBundleRequest.Signature nil unconditionally.
//
// The consequence is a SILENT trust downgrade at exactly the moment trust
// starts mattering: a bundle the author signed is published without its
// signature, the command exits 0, and the output says nothing. Consumers see
// unsigned content and route it to review; nobody is told a signature existed.
//
// Two commands that both promote the same bundle to the same remote disagree
// about whether its signature travels. Which one is right is a product
// decision — this test only makes the disagreement visible.
func TestPushBundleCfg_LocallySignedBundle_PlainPushDropsTheSignature(t *testing.T) {
	cfg, pub, mgr := pushSignTestSetup(t)
	discoverer, _ := discovererWithSoleAgentIdentity(t)
	armored := signLocalBundleOnDisk(t, cfg)
	require.FileExists(t, localSigPath(cfg), "precondition: the bundle is signed on disk")

	cmd, out := testCmd()
	require.NoError(t, pushBundleCfg(cmd, cfg, discoverer, mgr, "for-push", "", false, "", false, false))

	_, published := pub.files[".ctxloom/content/bundles/for-push.yaml"]
	require.True(t, published, "the bundle itself is published")
	_, sigPublished := pub.files[".ctxloom/content/bundles/for-push.yaml.sig"]
	assert.False(t, sigPublished,
		"CHARACTERIZED: a valid local signature is silently dropped by `bundle push` (move carries it)")
	assert.NotContains(t, out.String(), "Signed: yes")
	assert.NotContains(t, out.String(), "signature",
		"CHARACTERIZED: nothing tells the author their signature did not travel")

	// The local signature is still on disk and still valid — the loss is
	// entirely at the destination, which is why nobody notices locally.
	onDisk, err := os.ReadFile(localSigPath(cfg))
	require.NoError(t, err)
	assert.Equal(t, armored, onDisk)
}

// The stale variant of the same gap. `bundle move --to <remote>` REFUSES a
// bundle edited after signing (operations.TestMoveBundle_ToRemote_
// StaleSignature_RefusesAndPublishesNothing pins that, and it is correct:
// publishing a trusted key over non-matching bytes hands every consumer a
// tamper alarm for an attack that never happened).
//
// `bundle push` never reads the sidecar, so it neither refuses nor carries —
// it publishes the edited bytes unsigned. That is SAFE (no broken pair reaches
// anyone) but silent: the author's stale signature is exactly the signal that
// they published something they had not re-reviewed.
func TestPushBundleCfg_StaleLocalSignature_PlainPushPublishesUnsignedWithoutComment(t *testing.T) {
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
	require.NoError(t, pushBundleCfg(cmd, cfg, discoverer, mgr, "for-push", "", false, "", false, false),
		"CHARACTERIZED: push does not refuse a bundle whose local signature is stale")

	got, published := pub.files[".ctxloom/content/bundles/for-push.yaml"]
	require.True(t, published)
	assert.Equal(t, edited, got, "the edited bytes are published")
	_, sigPublished := pub.files[".ctxloom/content/bundles/for-push.yaml.sig"]
	assert.False(t, sigPublished, "no broken pair reaches consumers — the stale signature is simply not carried")
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
