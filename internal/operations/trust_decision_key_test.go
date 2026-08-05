package operations

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/ctxloom/ctxloom/internal/signing"
	"github.com/ctxloom/ctxloom/internal/signing/allowedsigners"
	"github.com/ctxloom/ctxloom/internal/signing/countersign"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// The write-side namespace gate (taskloom tiny-bankbook).
//
// A countersignature is honoured only when its signer is trusted for the
// assertion's namespace — VerifyCountersignature asks TrustedForNamespace
// before it verifies a byte, and reports a flat "not countersigned" when the
// answer is no. Until this gate existed, the WRITE side never asked: `ctxloom
// trust accept` with an ordinary ssh-agent key that nobody had granted the
// approve namespace wrote a well-formed record, printed "Approved … signed by
// SHA256:…", exited 0, and left the item withheld forever.
//
// Every test here asserts the STORE, not just the error: "refused" and
// "refused after writing the useless record" are different outcomes and only
// one of them is a fix. The store is asked whether it holds ANY record at all
// (storeFileCount over the backing fs), rather than through the verified
// lookup — that lookup answers "no" for an untrusted-key record too, so it
// cannot tell the two apart.

// untrustedSigner mints a fresh key that appears in NO trust root — the
// ordinary "a key is in my ssh-agent and nobody has granted it anything" shape.
func untrustedSigner(t *testing.T) ssh.Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := ssh.NewSignerFromSigner(priv)
	require.NoError(t, err)
	return signer
}

// storeFileCount counts what a countersignature store directory actually
// holds. Zero is the assertion "nothing was recorded" in its strongest form:
// no .sig, no .unsigned, no sidecar index.
func storeFileCount(t *testing.T, fs afero.Fs, dir string) int {
	t.Helper()
	entries, err := afero.ReadDir(fs, dir)
	if err != nil {
		return 0 // never created
	}
	return len(entries)
}

func TestSetItemTrust_RefusesAKeyUntrustedForApprove(t *testing.T) {
	loader, _ := seededLoader(t)
	fx := newTrustFixture(t)
	rogue := untrustedSigner(t)

	_, err := SetItemTrust(nil, SetItemTrustRequest{
		Ref:       "https://github.com/acme/repo@bundles/tooling#fragments/solid",
		Signer:    rogue,
		Root:      fx.root, // trusts fx.signer for approve+reject, and nothing else
		UserStore: fx.user,
		Loader:    loader,
	})

	require.Error(t, err, "accepting with a key nobody trusts for approve must FAIL, not report success")
	assert.ErrorIs(t, err, ErrReviewKeyUntrusted)

	// The message a human acts on. Three independent parts, because a refusal
	// missing any one of them leaves the user stuck: which key, which
	// namespace, and the command that grants it.
	msg := err.Error()
	assert.Contains(t, msg, ssh.FingerprintSHA256(rogue.PublicKey()), "the refusal must name the key it refused")
	assert.Contains(t, msg, signing.NamespaceApprove, "the refusal must name the namespace the key lacks")
	assert.Contains(t, msg, "ctxloom trust signer create", "the refusal must say how to trust the key")

	// THE LOAD-BEARING ASSERTION. A refusal that wrote the record anyway
	// leaves exactly the artifact the bug produced.
	assert.Zero(t, storeFileCount(t, fx.fs(), userApprovalsDir),
		"the refused approval must leave the store untouched — a record nothing honours is the failure being fixed")

	// And the item is still pending, by the same decision function the gate
	// serves: the refusal changed no exposure.
	tref := trust.Ref{RepoURL: trustRepo, Bundle: "tooling", Kind: trust.KindFragment, Name: "solid"}
	got, gerr := EffectiveTrust(nil, EffectiveTrustRequest{Ref: tref, Payload: pbytes("solid body"), Form: rawForm, Records: fx.records()})
	require.NoError(t, gerr)
	assert.Equal(t, trust.Deny, got.Decision)
	assert.Equal(t, trust.SourcePending, got.Source)
}

// The REJECT half. Same silent no-op, one direction worse: a user told content
// is blocked when it is not acts on a guarantee they do not have.
func TestSetBlacklist_RefusesAKeyUntrustedForReject(t *testing.T) {
	loader, _ := seededLoader(t)
	fx := newTrustFixture(t)
	rogue := untrustedSigner(t)

	_, err := SetBlacklist(nil, SetBlacklistRequest{
		Ref:       "https://github.com/acme/repo@bundles/tooling#fragments/solid",
		Signer:    rogue,
		Root:      fx.root,
		UserStore: fx.user,
		Loader:    loader,
	})

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrReviewKeyUntrusted)
	assert.Contains(t, err.Error(), signing.NamespaceReject, "a rejection must name the REJECT namespace, not approve")
	assert.Zero(t, storeFileCount(t, fx.fs(), userApprovalsDir),
		"neither the sticky ref block nor the content block may be written by a key that cannot make them")
}

// A key trusted for ONE of the two namespaces is not trusted for the other:
// the grants are independent, and the gate must read the one the decision
// actually needs rather than "is this key known at all".
func TestReviewDecision_ApproveGrantDoesNotAuthorizeReject(t *testing.T) {
	loader, _ := seededLoader(t)
	fs := afero.NewMemMapFs()

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := ssh.NewSignerFromSigner(priv)
	require.NoError(t, err)

	approveOnly := allowedsigners.NewStore(allowedsigners.Entry{
		Principals: []string{"reviewer@example.com"},
		Namespaces: []string{signing.NamespaceApprove},
		PublicKey:  signer.PublicKey(),
	})
	store := countersign.NewStore(userApprovalsDir, fs)
	ref := "https://github.com/acme/repo@bundles/tooling#fragments/solid"

	_, err = SetItemTrust(nil, SetItemTrustRequest{Ref: ref, Signer: signer, Root: approveOnly, UserStore: store, Loader: loader})
	require.NoError(t, err, "the approve grant authorizes an approval")
	assert.NotZero(t, storeFileCount(t, fs, userApprovalsDir))

	_, err = SetBlacklist(nil, SetBlacklistRequest{Ref: ref, Signer: signer, Root: approveOnly, UserStore: store, Loader: loader})
	require.Error(t, err, "the same key, with no reject grant, may not record a rejection")
	assert.ErrorIs(t, err, ErrReviewKeyUntrusted)
}

// FAIL CLOSED. Every way of failing to ESTABLISH that a key may decide here
// must refuse, never accept: no trust root at all, and an assertion outside
// the closed approve/reject vocabulary (which has no namespace, so no key can
// ever be authorized to make it).
func TestRequireTrustedForAssertion_FailsClosed(t *testing.T) {
	signer := untrustedSigner(t)

	t.Run("no trust root trusts no key", func(t *testing.T) {
		err := requireTrustedForAssertion(nil, signer.PublicKey(), signing.AssertionApprove)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrReviewKeyUntrusted)
	})

	t.Run("an assertion with no namespace authorizes nothing", func(t *testing.T) {
		root := allowedsigners.NewStore(allowedsigners.Entry{
			Principals: []string{"anyone@example.com"},
			Namespaces: []string{signing.NamespaceApprove, signing.NamespaceReject},
			PublicKey:  signer.PublicKey(),
		})
		err := requireTrustedForAssertion(root, signer.PublicKey(), signing.Assertion("countersign"))
		require.Error(t, err, "a key trusted for everything still cannot make an assertion no namespace covers")
		assert.ErrorIs(t, err, ErrReviewKeyUntrusted)
	})

	t.Run("a nil public key cannot be checked, so it is refused", func(t *testing.T) {
		err := requireTrustedForAssertion(allowedsigners.NewStore(), nil, signing.AssertionApprove)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrReviewKeyUntrusted)
	})
}

// The UNSIGNED degraded path (spec §9.5) is untouched: it has no key, so
// there is no namespace question to ask, and its records are honoured by
// HasUnsignedApprove/HasUnsignedRefReject, which consult no trust root at all.
// resolveDecisionSigner must therefore let it through even when the root
// trusts nothing — which is the only root an unsigned session has.
func TestResolveDecisionSigner_UnsignedPathIsNotGated(t *testing.T) {
	signer, unsigned, err := resolveDecisionSigner(nil, nil, false, allowedsigners.NewStore(), signing.AssertionApprove)
	if err != nil {
		// A developer machine with a resolvable ssh-agent key legitimately
		// takes the SIGNED path here, and that key is not in the empty root
		// passed above — so a refusal is the correct answer, not a failure.
		// Assert it is THAT refusal and stop; the unsigned arm is what this
		// test is about and it is unreachable on such a host.
		assert.ErrorIs(t, err, ErrReviewKeyUntrusted)
		t.Skip("this host resolves a signing key, so the unsigned arm is unreachable here")
	}
	require.True(t, unsigned, "no key resolved must mean the unsigned degraded path, not a silent signed one")
	assert.Nil(t, signer)
}

// The writer and the reader must consult the SAME trust root, or the writer
// records decisions the reader cannot honour — which is the entire bug. This
// pins the resolution order they share.
func TestReviewTrustRoot_InjectedWinsAndNilNeverTrusts(t *testing.T) {
	injected := allowedsigners.NewStore()
	assert.Same(t, injected, reviewTrustRoot(nil, injected))

	root := reviewTrustRoot(nil, nil)
	require.NotNil(t, root, "a nil cfg must yield an EMPTY root, never a nil one a caller might read as 'skip the check'")
	signer := untrustedSigner(t)
	assert.False(t, root.TrustedForNamespace(signer.PublicKey(), signing.NamespaceApprove, time.Now()).Trusted)
}
