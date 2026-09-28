package countersign

import (
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/signing"
	"github.com/ctxloom/ctxloom/internal/core/trust"
)

// TestRecords_Rejected_PinsEveryRejectionPath pins Rejected across both
// stores: a signed ref-level rejection in either store rejects; an UNSIGNED
// ref-level marker rejects only from the user store; a signed content
// rejection under any form the kind signs in rejects those bytes under any
// ref (an unaddressable one included); an unsigned content marker rejects
// only from the user store; an empty payload is never content-rejected; and
// an untrusted signer rejects nothing.
func TestRecords_Rejected_PinsEveryRejectionPath(t *testing.T) {
	ref := trust.Ref{IsLocal: true, Bundle: "tooling", Kind: trust.KindFragment, Name: "x"}
	refStr, err := CountersignRef(ref)
	require.NoError(t, err)
	unaddressable := trust.Ref{Kind: trust.KindFragment, Name: "x"}
	_, err = CountersignRef(unaddressable)
	require.Error(t, err, "the fixture's unaddressable ref must really fail to convert")

	payload := []byte("body")
	signer, pub := testSigner(t)
	trusted := rootTrusting("lead@team.example", pub, signing.NamespaceReject)
	_, strangerPub := testSigner(t)
	untrusted := rootTrusting("stranger@x.example", strangerPub, signing.NamespaceReject)

	type setup func(t *testing.T, user, project *Store)
	cases := []struct {
		name    string
		plant   setup
		root    trust.TrustRoot
		ref     trust.Ref
		payload []byte
		want    bool
	}{
		{"nothing recorded", func(*testing.T, *Store, *Store) {}, trusted, ref, payload, false},
		{"signed ref reject, user store", func(t *testing.T, u, _ *Store) { require.NoError(t, u.WriteRefReject(refStr, signer)) }, trusted, ref, payload, true},
		{"signed ref reject, project store", func(t *testing.T, _, p *Store) { require.NoError(t, p.WriteRefReject(refStr, signer)) }, trusted, ref, payload, true},
		{"signed ref reject, untrusted signer", func(t *testing.T, u, _ *Store) { require.NoError(t, u.WriteRefReject(refStr, signer)) }, untrusted, ref, payload, false},
		{"unsigned ref reject, user store", func(t *testing.T, u, _ *Store) { require.NoError(t, u.WriteUnsignedRefReject(refStr)) }, trusted, ref, payload, true},
		{"unsigned ref reject, project store", func(t *testing.T, _, p *Store) { require.NoError(t, p.WriteUnsignedRefReject(refStr)) }, trusted, ref, payload, false},
		{"signed content reject, raw form", func(t *testing.T, u, _ *Store) {
			require.NoError(t, u.WriteContentReject(signing.AttestFragmentRaw, payload, signer))
		}, trusted, ref, payload, true},
		{"signed content reject, distilled form, project store", func(t *testing.T, _, p *Store) {
			require.NoError(t, p.WriteContentReject(signing.AttestFragmentDistilled, payload, signer))
		}, trusted, ref, payload, true},
		{"signed content reject, other bytes", func(t *testing.T, u, _ *Store) {
			require.NoError(t, u.WriteContentReject(signing.AttestFragmentRaw, []byte("other"), signer))
		}, trusted, ref, payload, false},
		{"signed content reject, empty payload asked", func(t *testing.T, u, _ *Store) {
			require.NoError(t, u.WriteContentReject(signing.AttestFragmentRaw, payload, signer))
		}, trusted, ref, nil, false},
		{"unsigned content reject, user store", func(t *testing.T, u, _ *Store) {
			require.NoError(t, u.WriteUnsignedContentReject(signing.AttestFragmentRaw, payload))
		}, trusted, ref, payload, true},
		{"unsigned content reject, project store", func(t *testing.T, _, p *Store) {
			require.NoError(t, p.WriteUnsignedContentReject(signing.AttestFragmentRaw, payload))
		}, trusted, ref, payload, false},
		{"unaddressable ref, signed content reject", func(t *testing.T, u, _ *Store) {
			require.NoError(t, u.WriteContentReject(signing.AttestFragmentRaw, payload, signer))
		}, trusted, unaddressable, payload, true},
		{"unaddressable ref, only a ref reject", func(t *testing.T, u, _ *Store) {
			require.NoError(t, u.WriteUnsignedRefReject(refStr))
			require.NoError(t, u.WriteRefReject(refStr, signer))
		}, trusted, unaddressable, payload, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			user := NewStore("/user", afero.NewMemMapFs())
			project := NewStore("/project", afero.NewMemMapFs())
			tc.plant(t, user, project)
			c := Records{user: user, project: project, root: tc.root}
			assert.Equal(t, tc.want, c.Rejected(tc.ref, tc.payload))
		})
	}
}

// TestRecords_Rejected_HoldsAcrossProvablyEqualRepoSpellings: a ref-level
// rejection recorded under one spelling of a repository holds under every
// spelling that PROVABLY names the same one — a scheme-default port, an IDN
// host as U-label or A-label — and under no spelling that is merely probably
// the same (a non-default port, a ".git" suffix, repository-path case). A
// rejection that a respelled remote escapes is no rejection.
func TestRecords_Rejected_HoldsAcrossProvablyEqualRepoSpellings(t *testing.T) {
	signer, pub := testSigner(t)
	root := rootTrusting("lead@team.example", pub, signing.NamespaceReject)
	at := func(repo string) trust.Ref {
		return trust.Ref{RepoURL: repo, Bundle: "tooling", Kind: trust.KindFragment, Name: "x"}
	}
	cases := []struct {
		name, recorded, asked string
		want                  bool
	}{
		{"https default port vs ssh default port, U-label vs A-label", "https://bücher.example:443/acme/repo", "ssh://git@xn--bcher-kva.example:22/acme/repo", true},
		{"scp A-label vs https mixed-case U-label", "git@xn--bcher-kva.example:acme/repo", "https://BÜCHER.example/acme/repo", true},
		{"https vs git default port", "https://host.example/acme/repo", "git://host.example:9418/acme/repo", true},
		{"non-default port is another repository", "https://host.example/acme/repo", "https://host.example:8443/acme/repo", false},
		{".git suffix is another repository", "https://host.example/acme/repo", "https://host.example/acme/repo.git", false},
		{"path case is another repository", "https://host.example/acme/repo", "https://host.example/Acme/Repo", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			refStr, err := CountersignRef(at(tc.recorded))
			require.NoError(t, err)
			user := NewStore("/user", afero.NewMemMapFs())
			require.NoError(t, user.WriteRefReject(refStr, signer))
			c := Records{user: user, project: NewStore("/project", afero.NewMemMapFs()), root: root}
			assert.Equal(t, tc.want, c.Rejected(at(tc.asked), []byte("body")))
		})
	}
}
