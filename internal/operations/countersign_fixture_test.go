package operations

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/signing"
	"github.com/ctxloom/ctxloom/internal/signing/allowedsigners"
	"github.com/ctxloom/ctxloom/internal/signing/countersign"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// trustFixture is the test replacement for the deleted hash-pair trust.Store
// fixture (newTrustStore): it wraps two REAL countersignature stores
// (in-memory fs) plus an allowed_signers trust root trusting one key for both
// approve and reject, and exposes low-level write helpers that mirror the old
// store.SetAccepted/SetRejected SHAPE — "this ref/these bytes are
// approved/rejected" — but sign for real, so the same tests exercise the
// actual verification path end to end instead of a hash comparison.
type trustFixture struct {
	t       *testing.T
	signer  ssh.Signer
	pub     ssh.PublicKey
	user    *countersign.Store
	project *countersign.Store
	root    *allowedsigners.Store
	// memFs backs both stores. Exposed via fs() for the rare test that must
	// place a file in a store DIRECTLY — a record framed under a superseded
	// contract, which no current API can write.
	memFs afero.Fs
}

// The directories the fixture's two stores are rooted at, named so a test
// writing into one directly does not re-spell the path.
const (
	userApprovalsDir    = "/user-approvals"
	projectApprovalsDir = "/project-approvals"
)

func (f *trustFixture) fs() afero.Fs { return f.memFs }

// newTrustFixture builds a fresh fixture over an in-memory filesystem, with
// one signer trusted for both the approve and reject namespaces.
func newTrustFixture(t *testing.T) *trustFixture {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := ssh.NewSignerFromSigner(priv)
	require.NoError(t, err)

	root := allowedsigners.NewStore(allowedsigners.Entry{
		Principals: []string{"fixture@example.com"},
		Namespaces: []string{signing.NamespaceApprove, signing.NamespaceReject},
		PublicKey:  signer.PublicKey(),
	})
	fs := afero.NewMemMapFs()
	return &trustFixture{
		t: t, signer: signer, pub: signer.PublicKey(),
		user:    countersign.NewStore(userApprovalsDir, fs),
		project: countersign.NewStore(projectApprovalsDir, fs),
		root:    root,
		memFs:   fs,
	}
}

// records returns the ReviewRecords built over this fixture's stores — pass
// as EffectiveTrustRequest.Records / PendingReviewRequest's injected stores.
func (f *trustFixture) records() countersignRecords {
	return countersignRecords{user: f.user, project: f.project, root: f.root}
}

// approve signs+writes a REAL approve countersignature over payload, for ref
// in form, to the USER store — the direct equivalent of the deleted
// store.SetAccepted's single-slot write.
//
// form is the LAYOUT form, as at every gate call site; the ATTESTATION form the
// record binds is DERIVED from the ref's kind, exactly as production derives it,
// so a fixture can no more choose a role than a caller can.
func (f *trustFixture) approve(ref trust.Ref, form signing.Form, payload []byte) {
	f.t.Helper()
	attested, err := attestationFormFor(ref.Kind, form)
	require.NoError(f.t, err)
	require.NoError(f.t, f.user.WriteApprove(CountersignRef(ref), attested, payload, f.signer))
}

// rejectRef writes a ref-level (sticky) reject countersignature — the direct
// equivalent of the deleted store.SetRejected(repo, ref) with no hashes.
func (f *trustFixture) rejectRef(ref trust.Ref) {
	f.t.Helper()
	require.NoError(f.t, f.user.WriteRefReject(CountersignRef(ref), f.signer))
}

// rejectContent writes a content-reject countersignature over payload (ref
// omitted, form-scoped) — the direct equivalent of one hash in the deleted
// store.SetRejected(repo, ref, hashes...) denylist write.
func (f *trustFixture) rejectContent(kind trust.ItemKind, form signing.Form, payload []byte) {
	f.t.Helper()
	attested, err := attestationFormFor(kind, form)
	require.NoError(f.t, err)
	require.NoError(f.t, f.user.WriteContentReject(attested, payload, f.signer))
}

// approveFragment/approvePrompt are approve() convenience wrappers for the
// overwhelmingly common test shape: a single-form (raw) approval of a
// trustRepo-hosted fragment/prompt by (bundle, name, body).
func (f *trustFixture) approveFragment(bundle, name, rawBody string) {
	f.t.Helper()
	f.approve(trust.Ref{RepoURL: trustRepo, Bundle: bundle, Kind: trust.KindFragment, Name: name}, signing.FormRaw, []byte(rawBody))
}

func (f *trustFixture) approvePrompt(bundle, name, rawBody string) {
	f.t.Helper()
	f.approve(trust.Ref{RepoURL: trustRepo, Bundle: bundle, Kind: trust.KindPrompt, Name: name}, signing.FormRaw, []byte(rawBody))
}

// rejectFragment/rejectPrompt are the combined-rejection convenience wrappers
// mirroring the deleted store.SetRejected(repo, ref, hash)'s two-component
// write: a ref-level (sticky) block AND a content-reject over rawBody. Pass
// rawBody == "" to write only the ref-level block (the deleted store's
// no-hash rejection).
func (f *trustFixture) rejectFragment(bundle, name, rawBody string) {
	f.t.Helper()
	f.rejectRef(trust.Ref{RepoURL: trustRepo, Bundle: bundle, Kind: trust.KindFragment, Name: name})
	if rawBody != "" {
		f.rejectContent(trust.KindFragment, signing.FormRaw, []byte(rawBody))
	}
}

func (f *trustFixture) rejectPrompt(bundle, name, rawBody string) {
	f.t.Helper()
	f.rejectRef(trust.Ref{RepoURL: trustRepo, Bundle: bundle, Kind: trust.KindPrompt, Name: name})
	if rawBody != "" {
		f.rejectContent(trust.KindPrompt, signing.FormRaw, []byte(rawBody))
	}
}

// rejectItem is the kind-agnostic combined-rejection helper (ref + content),
// for mcp/hook items that approveFragment/approvePrompt's kind-specific
// wrappers don't cover.
func (f *trustFixture) rejectItem(ref trust.Ref, form signing.Form, payload []byte) {
	f.t.Helper()
	f.rejectRef(ref)
	if len(payload) > 0 {
		f.rejectContent(ref.Kind, form, payload)
	}
}

// installUnsignedRejection writes an UNSIGNED ref+content rejection directly
// to the REAL user countersignature store (~/.ctxloom/approvals, OS fs) — for
// tests exercising the full default (non-injected) gate-construction path,
// which always reads that real location. HOME must already be pointed at a
// controlled temp dir (t.Setenv("HOME", ...)) before calling this — the
// degraded unsigned path (spec §9.5) is honored from the user store only.
func installUnsignedRejection(t *testing.T, ref trust.Ref, form signing.Form, payload []byte) {
	t.Helper()
	home, err := paths.HomeApprovalsPath()
	require.NoError(t, err)
	store := countersign.NewStore(home, afero.NewOsFs())
	refStr := CountersignRef(ref)
	require.NoError(t, store.WriteUnsignedRefReject(refStr))
	if len(payload) > 0 {
		attested, ferr := attestationFormFor(ref.Kind, form)
		require.NoError(t, ferr)
		require.NoError(t, store.WriteUnsignedContentReject(attested, payload))
	}
}
