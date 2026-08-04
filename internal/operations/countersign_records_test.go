package operations

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/ctxloom/ctxloom/internal/signing"
	"github.com/ctxloom/ctxloom/internal/signing/allowedsigners"
	"github.com/ctxloom/ctxloom/internal/signing/countersign"
	"github.com/ctxloom/ctxloom/internal/trust"
)

func newKeypair(t *testing.T) (ssh.Signer, ssh.PublicKey) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := ssh.NewSignerFromSigner(priv)
	require.NoError(t, err)
	return signer, signer.PublicKey()
}

// TestCountersignRecords_PersonalRejectBeatsProjectApprove is the required S6
// trap test for cross-store precedence (spec §9.2's composition table): the
// PROJECT store carries a valid, trusted APPROVE countersignature for an item
// (e.g. a team lead's inherited blessing); the USER's own PERSONAL store
// carries a valid, trusted REJECT for the exact same item. The union must
// deny — a personal rejection beats an inherited approval, ALWAYS, because
// rejection is checked first (step 1) across BOTH stores with no store-level
// precedence at all; a signature is a signature no matter which store holds
// it, and the DECISION FUNCTION's step order is where "rejection wins" lives.
func TestCountersignRecords_PersonalRejectBeatsProjectApprove(t *testing.T) {
	fs := afero.NewMemMapFs()

	// Two DIFFERENT keys: a team lead's key (trusted for approve, writes to
	// the project store) and the developer's own key (trusted for reject,
	// writes to their personal user store) — modeling the real scenario the
	// spec describes, not merely one key signing both.
	leadSigner, leadPub := newKeypair(t)
	devSigner, devPub := newKeypair(t)

	root := allowedsigners.NewStore(
		allowedsigners.Entry{Principals: []string{"lead@team.example"}, Namespaces: []string{signing.NamespaceApprove}, PublicKey: leadPub},
		allowedsigners.Entry{Principals: []string{"dev@example.com"}, Namespaces: []string{signing.NamespaceReject}, PublicKey: devPub},
	)

	userStore := countersign.NewStore("/user-approvals", fs)
	projectStore := countersign.NewStore("/project-approvals", fs)

	ref := trust.Ref{RepoURL: trustRepo, Bundle: "tooling", Kind: trust.KindFragment, Name: "shared"}
	payload := []byte("shared fragment body")

	// The lead approved it, in the PROJECT store, over the raw form.
	require.NoError(t, projectStore.WriteApprove(countersignRef(ref), signing.AttestFragmentRaw, payload, leadSigner))
	// The developer independently rejected the SAME item's ref, in their OWN
	// personal USER store.
	require.NoError(t, userStore.WriteRefReject(countersignRef(ref), devSigner))

	records := countersignRecords{user: userStore, project: projectStore, root: root}

	// Sanity: in isolation, the project approval alone WOULD allow.
	assert.True(t, records.Approved(ref, payload, string(signing.FormRaw)),
		"the project store's approval must itself verify (sanity check)")

	// But the union's Rejected() must ALSO be true — and per the decision
	// function's step order (checked in EffectiveTrust, step 1 before step
	// 5), rejection wins overall.
	assert.True(t, records.Rejected(ref, payload),
		"a personal rejection in the user store must be found even though the project store approves")

	res, err := EffectiveTrust(nil, EffectiveTrustRequest{
		Ref: ref, Payload: payload, Form: string(signing.FormRaw), Records: records,
		Posture: postureCtxOf(ref), Provenance: postureProvOf(ref),
	})
	require.NoError(t, err)
	assert.Equal(t, trust.Deny, res.Decision, "personal rejection must beat an inherited project approval")
	assert.Equal(t, trust.SourceRejected, res.Source)
}

// TestCountersignRecords_ProjectRejectBeatsPersonalApprove is the symmetric
// case (spec §9.2 table, second row): a rejection anywhere, from any trusted
// key, wins — an org must be able to blacklist content its developers cannot
// un-blacklist locally by approving it.
func TestCountersignRecords_ProjectRejectBeatsPersonalApprove(t *testing.T) {
	fs := afero.NewMemMapFs()
	leadSigner, leadPub := newKeypair(t)
	devSigner, devPub := newKeypair(t)

	root := allowedsigners.NewStore(
		allowedsigners.Entry{Principals: []string{"lead@team.example"}, Namespaces: []string{signing.NamespaceReject}, PublicKey: leadPub},
		allowedsigners.Entry{Principals: []string{"dev@example.com"}, Namespaces: []string{signing.NamespaceApprove}, PublicKey: devPub},
	)

	userStore := countersign.NewStore("/user-approvals", fs)
	projectStore := countersign.NewStore("/project-approvals", fs)

	ref := trust.Ref{RepoURL: trustRepo, Bundle: "tooling", Kind: trust.KindFragment, Name: "blocked-org-wide"}
	payload := []byte("org-blocked body")

	require.NoError(t, projectStore.WriteRefReject(countersignRef(ref), leadSigner))
	require.NoError(t, userStore.WriteApprove(countersignRef(ref), signing.AttestFragmentRaw, payload, devSigner))

	records := countersignRecords{user: userStore, project: projectStore, root: root}

	res, err := EffectiveTrust(nil, EffectiveTrustRequest{
		Ref: ref, Payload: payload, Form: string(signing.FormRaw), Records: records,
		Posture: postureCtxOf(ref), Provenance: postureProvOf(ref),
	})
	require.NoError(t, err)
	assert.Equal(t, trust.Deny, res.Decision, "an org-wide rejection must beat a developer's own local approval")
	assert.Equal(t, trust.SourceRejected, res.Source)
}

// TestCountersignRecords_UntrustedKeyCountersigIsNotApproval proves an
// approve countersignature by a key NOT in the trust root is inert — the
// signature verifies cryptographically (right bytes, right namespace) but
// the signer is simply not trusted, so it resolves pending, never allow.
func TestCountersignRecords_UntrustedKeyCountersigIsNotApproval(t *testing.T) {
	fs := afero.NewMemMapFs()
	signer, _ := newKeypair(t)
	_, otherPub := newKeypair(t) // a DIFFERENT key is the one actually trusted

	root := allowedsigners.NewStore(
		allowedsigners.Entry{Principals: []string{"someone-else@example.com"}, Namespaces: []string{signing.NamespaceApprove}, PublicKey: otherPub},
	)
	userStore := countersign.NewStore("/user-approvals", fs)

	ref := trust.Ref{RepoURL: trustRepo, Bundle: "tooling", Kind: trust.KindFragment, Name: "x"}
	payload := []byte("body")
	require.NoError(t, userStore.WriteApprove(countersignRef(ref), signing.AttestFragmentRaw, payload, signer))

	records := countersignRecords{user: userStore, project: countersign.NewStore("/project-approvals", fs), root: root}

	res, err := EffectiveTrust(nil, EffectiveTrustRequest{
		Ref: ref, Payload: payload, Form: string(signing.FormRaw), Records: records,
		Posture: postureCtxOf(ref), Provenance: postureProvOf(ref),
	})
	require.NoError(t, err)
	assert.Equal(t, trust.Deny, res.Decision, "a countersignature by an untrusted key must not allow")
	assert.Equal(t, trust.SourcePending, res.Source)
}
