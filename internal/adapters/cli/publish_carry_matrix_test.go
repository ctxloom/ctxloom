package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/content"
	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/config"
)

// THE ONE TABLE. ctxloom has two commands that promote a bundle to a remote —
// `bundle push` and `bundle move --to <remote>` — and this file puts both in
// one matrix (signature absent/valid/stale x --sign/--no-sign/neither x
// sign.default true/false) so the contract is one diff in one place rather
// than an inference across two packages.
//
// A bundle's ONE signature is its SHA256SUMS manifest and the .sigs/ entry
// over it, and it lives INSIDE the tree: publishing carries the tree as it
// stands, refuses a stale manifest, and --sign is sugar for sign-then-publish
// (`ctxloom bundle sign` writes the manifest entry; the push carries it).
// `bundle move` takes no signing flags: its rows vary only in signature
// state, and push's "no flags, sign.default off" rows read exactly like them.
//
// Every assertion is on the PAYLOAD the fake publisher recorded and on the
// bytes left on disk, never on a success message: "published, exit 0, no
// signature" is precisely the silent outcome this table exists to make
// visible.

// signatureState is the state of the tree's manifest signature when the
// publishing command runs.
type signatureState int

const (
	// signatureAbsent: the bundle was never signed.
	signatureAbsent signatureState = iota
	// signatureValid: `ctxloom bundle sign` ran and nothing changed since.
	signatureValid
	// signatureStale: signed, then the bundle was edited — the manifest no
	// longer covers the files.
	signatureStale
)

func (s signatureState) String() string {
	return [...]string{"signature-absent", "signature-valid", "signature-stale"}[s]
}

type publishVia int

const (
	viaPush publishVia = iota
	viaMove
)

type carryCase struct {
	name string
	via  publishVia
	sig  signatureState

	sign, noSign, signDefault bool

	wantErrIs        error
	wantBundleSent   bool
	wantSigPublished bool // a .sigs/ entry reached the remote
	wantReSigned     bool // the on-disk entry set changed (--sign minted a new one)
	wantSourceGone   bool // move only: the source tree must be removed
	wantSourceIntact bool // move only: a refusal must leave the source alone
}

const remoteTreeRoot = ".ctxloom/content/bundles/v2/for-push"

var editedBundleBytes = []byte("version: 2.0.0\ndescription: rewritten\n")

func localBundlePath(cfg *config.Config) string {
	return filepath.Join(authoredV1(cfg.GetAppPaths()[0]), "for-push", bundles.DirectoryFormManifest)
}

func localSigsDir(cfg *config.Config) string {
	return filepath.Join(authoredV1(cfg.GetAppPaths()[0]), "for-push", content.SigDirName)
}

// sigEntries lists the tree's .sigs/ entry names on disk; nil when unsigned.
func sigEntries(t *testing.T, cfg *config.Config) []string {
	t.Helper()
	entries, err := os.ReadDir(localSigsDir(cfg))
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// signLocalBundleOnDisk signs the "for-push" tree exactly as `ctxloom bundle
// sign for-push` does: through its manifest entry, with the sole agent key.
func signLocalBundleOnDisk(t *testing.T, cfg *config.Config) {
	t.Helper()
	_, signer := discovererWithSoleAgentIdentity(t)
	_, err := operations.SignBundleFile(cfg, operations.SignBundleRequest{
		Target: operations.SignTarget{BundleName: "for-push"},
		Signer: signer,
	})
	require.NoError(t, err)
}

func applySignatureState(t *testing.T, cfg *config.Config, state signatureState) {
	t.Helper()
	switch state {
	case signatureAbsent:
		require.Nil(t, sigEntries(t, cfg))
	case signatureValid:
		signLocalBundleOnDisk(t, cfg)
	case signatureStale:
		signLocalBundleOnDisk(t, cfg)
		require.NoError(t, os.WriteFile(localBundlePath(cfg), editedBundleBytes, 0o644))
	}
}

func runCarryCase(t *testing.T, tc carryCase) {
	t.Helper()
	cfg, pub, mgr := pushSignTestSetup(t)
	if tc.signDefault {
		f := cfg.ToFixture()
		f.Settings.Sign = &config.SignConfig{Default: true}
		cfg = config.NewFixture(f)
	}
	discoverer, _ := discovererWithSoleAgentIdentity(t)
	applySignatureState(t, cfg, tc.sig)
	entriesBefore := sigEntries(t, cfg)
	bundleBytesBefore, err := os.ReadFile(localBundlePath(cfg))
	require.NoError(t, err)

	var runErr error
	switch tc.via {
	case viaPush:
		cmd, _ := testCmd()
		runErr = pushBundleCfg(cmd, cfg, discoverer, mgr, "for-push", "", false, "", tc.sign, tc.noSign)
	case viaMove:
		_, runErr = operations.MoveBundle(context.Background(), cfg, operations.MoveBundleRequest{
			Name: "for-push", To: "personal", PublishManager: mgr,
		})
	}

	if tc.wantErrIs != nil {
		require.ErrorIs(t, runErr, tc.wantErrIs)
	} else {
		require.NoError(t, runErr)
	}

	sentBundle, bundleSent := pub.files[remoteTreeRoot+"/"+bundles.DirectoryFormManifest]
	assert.Equal(t, tc.wantBundleSent, bundleSent, "bundle published?")
	if tc.wantBundleSent {
		assert.Equal(t, bundleBytesBefore, sentBundle, "the published bytes are the local file's bytes, verbatim")
	}
	assert.Equal(t, tc.wantSigPublished, anySigPublished(pub.files), "a .sigs/ entry reached the remote?")
	assertCarrySource(t, tc, cfg, entriesBefore)
}

// anySigPublished reports whether any .sigs/ entry reached the remote.
func anySigPublished(files map[string][]byte) bool {
	for path := range files {
		if strings.HasPrefix(path, remoteTreeRoot+"/"+content.SigDirName+"/") {
			return true
		}
	}
	return false
}

// assertCarrySource checks the local source: re-signed or not while it
// stands, removed by a completed move, left by a refused one.
func assertCarrySource(t *testing.T, tc carryCase, cfg *config.Config, entriesBefore []string) {
	t.Helper()
	srcInfo, srcErr := os.Stat(localBundlePath(cfg))
	sourceGone := srcErr != nil || !srcInfo.Mode().IsRegular()
	if !sourceGone {
		reSigned := !assert.ObjectsAreEqual(entriesBefore, sigEntries(t, cfg))
		assert.Equal(t, tc.wantReSigned, reSigned, "the on-disk signature entries changed?")
	}
	if tc.wantSourceGone {
		assert.True(t, sourceGone, "a completed move removes the source")
	}
	if tc.wantSourceIntact {
		assert.False(t, sourceGone, "a refused move leaves the source in place")
	}
}

func TestPublishCarryMatrix(t *testing.T) {
	for _, tc := range publishCarryCases() {
		t.Run(tc.name, func(t *testing.T) { runCarryCase(t, tc) })
	}
}

func publishCarryCases() []carryCase {
	return []carryCase{
		// --- push, no flags, sign.default off: reads exactly like move ---
		{name: "push/absent/no-flags/default-off", via: viaPush, sig: signatureAbsent, wantBundleSent: true},
		{name: "push/valid/no-flags/default-off", via: viaPush, sig: signatureValid, wantBundleSent: true, wantSigPublished: true},
		{name: "push/stale/no-flags/default-off", via: viaPush, sig: signatureStale, wantErrIs: operations.ErrStaleSignature},

		// --- push --sign: sign-then-publish; a stale manifest is re-signed ---
		{name: "push/absent/--sign", via: viaPush, sig: signatureAbsent, sign: true, wantBundleSent: true, wantSigPublished: true, wantReSigned: true},
		{name: "push/valid/--sign", via: viaPush, sig: signatureValid, sign: true, wantBundleSent: true, wantSigPublished: true, wantReSigned: true},
		{name: "push/stale/--sign", via: viaPush, sig: signatureStale, sign: true, wantBundleSent: true, wantSigPublished: true, wantReSigned: true},

		// --- push --no-sign: never signs; the tree still travels as it stands ---
		{name: "push/absent/--no-sign", via: viaPush, sig: signatureAbsent, noSign: true, wantBundleSent: true},
		{name: "push/valid/--no-sign", via: viaPush, sig: signatureValid, noSign: true, wantBundleSent: true, wantSigPublished: true},
		{name: "push/stale/--no-sign", via: viaPush, sig: signatureStale, noSign: true, wantErrIs: operations.ErrStaleSignature},

		// --- push, sign.default on: --sign unless --no-sign ---
		{name: "push/absent/default-on", via: viaPush, sig: signatureAbsent, signDefault: true, wantBundleSent: true, wantSigPublished: true, wantReSigned: true},
		{name: "push/stale/default-on", via: viaPush, sig: signatureStale, signDefault: true, wantBundleSent: true, wantSigPublished: true, wantReSigned: true},
		{name: "push/stale/default-on/--no-sign", via: viaPush, sig: signatureStale, signDefault: true, noSign: true, wantErrIs: operations.ErrStaleSignature},

		// --- move: no flags; the target push's no-flags rows converge on ---
		{name: "move/absent", via: viaMove, sig: signatureAbsent, wantBundleSent: true, wantSourceGone: true},
		{name: "move/valid", via: viaMove, sig: signatureValid, wantBundleSent: true, wantSigPublished: true, wantSourceGone: true},
		{name: "move/stale", via: viaMove, sig: signatureStale, wantErrIs: operations.ErrStaleSignature, wantSourceIntact: true},
	}
}
