package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/signing"
)

// THE ONE TABLE. ctxloom has two commands that promote a bundle to a remote —
// `bundle push` and `bundle move --to <remote>` — and they disagreed about
// whether the author's signature travels with it. This file puts both paths in
// one matrix (sidecar absent/valid/stale x --sign/--no-sign/neither x
// sign.default true/false) so the disagreement, and its resolution, is one diff
// in one place rather than an inference across two packages.
//
// `bundle move` takes no signing flags at all: it has always CARRIED whatever
// sidecar was on disk and REFUSED a stale one. So its rows vary only in sidecar
// state, and they are the target the push rows converge on: push's "no flags,
// sign.default off" rows now read exactly like the move rows with the same
// sidecar state. That equality IS the unification — check it by eye.
//
// A signature belongs to the BUNDLE, not to the publish. `ctxloom bundle sign`
// is the only producer; publishing carries a valid sidecar and refuses a stale
// one; --sign is sugar for sign-then-publish (it mints the sidecar on disk,
// then carries it); --no-sign publishes bare even when a valid sidecar exists.
//
// Every assertion is on the PAYLOAD the fake publisher recorded and on the
// bytes left on disk, never on a success message: "published, exit 0, no
// signature" is precisely the silent outcome this table exists to make visible.

// --- table vocabulary --------------------------------------------------------

// sidecarState is the state of the bundle's detached `<name>.yaml.sig` sibling
// when the publishing command runs.
type sidecarState int

const (
	// sidecarAbsent: the bundle was never signed.
	sidecarAbsent sidecarState = iota
	// sidecarValid: `ctxloom bundle sign` ran and nothing changed since.
	sidecarValid
	// sidecarStale: signed, then the bundle was edited — the signature covers
	// bytes that no longer exist.
	sidecarStale
)

func (s sidecarState) String() string {
	switch s {
	case sidecarAbsent:
		return "sidecar-absent"
	case sidecarValid:
		return "sidecar-valid"
	case sidecarStale:
		return "sidecar-stale"
	}
	return "sidecar-?"
}

// publishVia names which of the two promoting commands the row exercises.
type publishVia int

const (
	viaPush publishVia = iota
	viaMove
)

func (p publishVia) String() string {
	if p == viaMove {
		return "move"
	}
	return "push"
}

// publishedSigRelation says what the .sig published to the remote IS, relative
// to the sidecar on disk. This is the load-bearing column: "a .sig was
// published" is not the same claim as "the author's signature was published".
type publishedSigRelation int

const (
	// sigNotPublished: no .sig sibling reached the remote.
	sigNotPublished publishedSigRelation = iota
	// sigEqualsPreSidecar: the published .sig is byte-identical to the sidecar
	// that was on disk BEFORE the command — a genuine carry.
	sigEqualsPreSidecar
	// sigEqualsPostSidecar: the published .sig is byte-identical to the sidecar
	// on disk AFTER the command — a mint that persisted, then was carried.
	sigEqualsPostSidecar
	// sigMintedTransiently: a .sig was published that matches NO sidecar on
	// disk, before or after — signed during the push and never written down.
	sigMintedTransiently
)

func (r publishedSigRelation) String() string {
	switch r {
	case sigNotPublished:
		return "no .sig published"
	case sigEqualsPreSidecar:
		return "published .sig == sidecar before (carried)"
	case sigEqualsPostSidecar:
		return "published .sig == sidecar after (minted, persisted, carried)"
	case sigMintedTransiently:
		return "published .sig matches no on-disk sidecar (minted in-flight)"
	}
	return "?"
}

// sidecarOutcome says what the command did to the local `.sig` on disk.
type sidecarOutcome int

const (
	// sidecarStillAbsent: there was none and none was written.
	sidecarStillAbsent sidecarOutcome = iota
	// sidecarUntouched: the sidecar is byte-identical to before.
	sidecarUntouched
	// sidecarWritten: a sidecar exists now that did not before, or differs.
	sidecarWritten
	// sidecarGone: the sidecar was removed (a completed `move` takes it).
	sidecarGone
)

func (o sidecarOutcome) String() string {
	switch o {
	case sidecarStillAbsent:
		return "no sidecar, none written"
	case sidecarUntouched:
		return "sidecar untouched"
	case sidecarWritten:
		return "sidecar written/replaced on disk"
	case sidecarGone:
		return "sidecar removed (moved away)"
	}
	return "?"
}

// carryCase is one row of the matrix.
type carryCase struct {
	name    string
	via     publishVia
	sidecar sidecarState

	// push-only inputs; `bundle move` has no signing flags and no
	// sign.default participation.
	sign        bool
	noSign      bool
	signDefault bool

	// expectations
	wantErrContains  string
	wantBundleSent   bool
	wantSigRelation  publishedSigRelation
	wantSidecar      sidecarOutcome
	wantSourceGone   bool // move only: the source YAML must be removed
	wantSourceIntact bool // move only: a refusal must leave the source alone
}

const (
	remoteBundlePath = ".ctxloom/content/bundles/for-push.yaml"
	remoteSigPath    = ".ctxloom/content/bundles/for-push.yaml.sig"
)

// editedBundleBytes is the rewrite that strands a signature: the same bundle,
// different bytes.
var editedBundleBytes = []byte("version: 2.0.0\nfragments:\n  intro:\n    content: rewritten\n")

// localBundlePath is the on-disk path of the "for-push" bundle in a
// pushSignTestSetup project.
func localBundlePath(cfg *config.Config) string {
	return filepath.Join(paths.LocalBundlesPath(cfg.GetAppPaths()[0]), "for-push.yaml")
}

// applySidecarState puts the bundle into the requested (bytes, sidecar) state
// and returns the sidecar bytes on disk immediately before the command runs
// (nil when unsigned).
func applySidecarState(t *testing.T, cfg *config.Config, state sidecarState) []byte {
	t.Helper()
	switch state {
	case sidecarAbsent:
		require.NoFileExists(t, localSigPath(cfg))
		return nil
	case sidecarValid:
		pre := signLocalBundleOnDisk(t, cfg)
		return pre
	case sidecarStale:
		signLocalBundleOnDisk(t, cfg)
		require.NoError(t, os.WriteFile(localBundlePath(cfg), editedBundleBytes, 0o644))
		pre, err := os.ReadFile(localSigPath(cfg))
		require.NoError(t, err)
		require.Error(t, signing.CoversBytes(editedBundleBytes, pre, signing.NamespacePublish),
			"precondition: the sidecar must not cover the edited bytes")
		return pre
	}
	t.Fatalf("unknown sidecar state %v", state)
	return nil
}

// readSidecar returns the sidecar bytes on disk, or nil when there is none.
func readSidecar(t *testing.T, cfg *config.Config) []byte {
	t.Helper()
	data, err := os.ReadFile(localSigPath(cfg))
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	return data
}

// classifySidecar reports what happened to the local sidecar.
func classifySidecar(pre, post []byte, sourceGone bool) sidecarOutcome {
	switch {
	case post == nil && pre != nil && sourceGone:
		return sidecarGone
	case post == nil:
		return sidecarStillAbsent
	case pre != nil && string(pre) == string(post):
		return sidecarUntouched
	default:
		return sidecarWritten
	}
}

// classifyPublishedSig reports what the .sig that reached the remote actually
// is, relative to the sidecars on disk.
func classifyPublishedSig(published, pre, post []byte) publishedSigRelation {
	switch {
	case published == nil:
		return sigNotPublished
	case pre != nil && string(published) == string(pre):
		return sigEqualsPreSidecar
	case post != nil && string(published) == string(post):
		return sigEqualsPostSidecar
	default:
		return sigMintedTransiently
	}
}

// runCarryCase executes one row and asserts every column.
func runCarryCase(t *testing.T, tc carryCase) {
	t.Helper()
	cfg, pub, mgr := pushSignTestSetup(t)
	if tc.signDefault {
		f := cfg.ToFixture()
		f.Settings.Sign = &config.SignConfig{Default: true}
		cfg = config.NewFixture(f)
	}
	discoverer, _ := discovererWithSoleAgentIdentity(t)

	pre := applySidecarState(t, cfg, tc.sidecar)
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

	if tc.wantErrContains != "" {
		require.Error(t, runErr)
		assert.Contains(t, runErr.Error(), tc.wantErrContains)
	} else {
		require.NoError(t, runErr)
	}

	sentBundle, bundleSent := pub.files[remoteBundlePath]
	assert.Equal(t, tc.wantBundleSent, bundleSent, "bundle published?")
	if tc.wantBundleSent {
		assert.Equal(t, bundleBytesBefore, sentBundle,
			"the published bytes are the local file's bytes, verbatim")
	}

	var sentSig []byte
	if s, ok := pub.files[remoteSigPath]; ok {
		sentSig = s
	}
	post := readSidecar(t, cfg)
	sourceGone := !fileExists(localBundlePath(cfg))

	assert.Equal(t, tc.wantSigRelation.String(), classifyPublishedSig(sentSig, pre, post).String(),
		"what reached the remote as a signature")
	assert.Equal(t, tc.wantSidecar.String(), classifySidecar(pre, post, sourceGone).String(),
		"what happened to the local sidecar")

	if tc.wantSourceGone {
		assert.True(t, sourceGone, "a completed move removes the source")
	}
	if tc.wantSourceIntact {
		assert.False(t, sourceGone, "a refused move leaves the source in place")
	}

	// A published signature must actually verify over the published bytes.
	// Publishing a pair that does not match is the tamper alarm every guard in
	// this area exists to prevent, and "a .sig was published" alone would not
	// catch it.
	if sentSig != nil {
		assert.NoError(t, signing.CoversBytes(sentBundle, sentSig, signing.NamespacePublish),
			"a published signature must cover the published bytes")
	}
}

// TestPublishCarryMatrix is the whole table. Read the want* columns top to
// bottom to see what each path does; read `push` rows against the `move` rows
// with the same sidecar state to see whether the two agree.
func TestPublishCarryMatrix(t *testing.T) {
	for _, tc := range publishCarryCases() {
		t.Run(tc.name, func(t *testing.T) { runCarryCase(t, tc) })
	}
}

func publishCarryCases() []carryCase {
	return []carryCase{
		// --- push, no sidecar on disk ---------------------------------------
		{
			name: "push/absent/no-flags/default-off", via: viaPush, sidecar: sidecarAbsent,
			wantBundleSent: true, wantSigRelation: sigNotPublished, wantSidecar: sidecarStillAbsent,
		},
		{
			// The sugar: sign, THEN publish. The sidecar it mints is the same
			// artifact `ctxloom bundle sign` writes, and it stays on disk — so
			// what shipped is verifiable at rest afterwards.
			name: "push/absent/sign/default-off", via: viaPush, sidecar: sidecarAbsent, sign: true,
			wantBundleSent: true, wantSigRelation: sigEqualsPostSidecar, wantSidecar: sidecarWritten,
		},
		{
			name: "push/absent/no-flags/default-on", via: viaPush, sidecar: sidecarAbsent, signDefault: true,
			wantBundleSent: true, wantSigRelation: sigEqualsPostSidecar, wantSidecar: sidecarWritten,
		},
		{
			name: "push/absent/no-sign/default-on", via: viaPush, sidecar: sidecarAbsent, noSign: true, signDefault: true,
			wantBundleSent: true, wantSigRelation: sigNotPublished, wantSidecar: sidecarStillAbsent,
		},

		// --- push, a VALID sidecar sits beside the bundle --------------------
		{
			// THE FIX, and identical to move/valid: `bundle sign foo &&
			// bundle push foo` publishes the signature the author made.
			name: "push/valid/no-flags/default-off", via: viaPush, sidecar: sidecarValid,
			wantBundleSent: true, wantSigRelation: sigEqualsPreSidecar, wantSidecar: sidecarUntouched,
		},
		{
			// --sign RE-signs rather than carrying what is there: it is an
			// explicit instruction to sign, and the key it signs with (sign.key
			// / --key / agent) may not be the one that made the old sidecar.
			name: "push/valid/sign/default-off", via: viaPush, sidecar: sidecarValid, sign: true,
			wantBundleSent: true, wantSigRelation: sigEqualsPostSidecar, wantSidecar: sidecarWritten,
		},
		{
			name: "push/valid/no-flags/default-on", via: viaPush, sidecar: sidecarValid, signDefault: true,
			wantBundleSent: true, wantSigRelation: sigEqualsPostSidecar, wantSidecar: sidecarWritten,
		},
		{
			// --no-sign means publish BARE, even though a perfectly good
			// signature is sitting right there. The escape hatch survives.
			name: "push/valid/no-sign/default-on", via: viaPush, sidecar: sidecarValid, noSign: true, signDefault: true,
			wantBundleSent: true, wantSigRelation: sigNotPublished, wantSidecar: sidecarUntouched,
		},

		// --- push, a STALE sidecar (bundle edited after signing) -------------
		{
			// Identical to move/stale. A signature over bytes that no longer
			// exist is the author's own signal that they are about to ship
			// something they did not re-review; move has always stopped there
			// and push now does too.
			name: "push/stale/no-flags/default-off", via: viaPush, sidecar: sidecarStale,
			wantErrContains: "no longer covers",
			wantBundleSent:  false, wantSigRelation: sigNotPublished, wantSidecar: sidecarUntouched,
		},
		{
			// --sign re-signs FIRST, so the stale sidecar is replaced by one
			// that covers the current bytes and the push proceeds — and local
			// and remote now agree, which the mint model never achieved.
			name: "push/stale/sign/default-off", via: viaPush, sidecar: sidecarStale, sign: true,
			wantBundleSent: true, wantSigRelation: sigEqualsPostSidecar, wantSidecar: sidecarWritten,
		},
		{
			name: "push/stale/no-flags/default-on", via: viaPush, sidecar: sidecarStale, signDefault: true,
			wantBundleSent: true, wantSigRelation: sigEqualsPostSidecar, wantSidecar: sidecarWritten,
		},
		{
			// --no-sign does not carry, so there is no pair to be stale.
			name: "push/stale/no-sign/default-on", via: viaPush, sidecar: sidecarStale, noSign: true, signDefault: true,
			wantBundleSent: true, wantSigRelation: sigNotPublished, wantSidecar: sidecarUntouched,
		},

		// --- move: the reference behaviour push converges on ------------------
		{
			name: "move/absent", via: viaMove, sidecar: sidecarAbsent,
			wantBundleSent: true, wantSigRelation: sigNotPublished, wantSidecar: sidecarStillAbsent,
			wantSourceGone: true,
		},
		{
			name: "move/valid", via: viaMove, sidecar: sidecarValid,
			wantBundleSent: true, wantSigRelation: sigEqualsPreSidecar, wantSidecar: sidecarGone,
			wantSourceGone: true,
		},
		{
			name: "move/stale", via: viaMove, sidecar: sidecarStale,
			wantErrContains: "no longer covers",
			wantBundleSent:  false, wantSigRelation: sigNotPublished, wantSidecar: sidecarUntouched,
			wantSourceIntact: true,
		},
	}
}
