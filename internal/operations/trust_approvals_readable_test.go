package operations

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/internal/signing/countersign"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// denyOpenFs wraps an afero.Fs and fails Open for any path in deny — a fake
// (not a mock) that simulates "permission denied"/"I/O error" reads without
// touching real OS file permissions, mirroring
// internal/signing/countersign's own denyFs test fixture. Used here to drive
// EffectiveTrust's records-construction preamble down its fail-closed path
// deterministically, with no chmod/root-skip flakiness.
type denyOpenFs struct {
	afero.Fs
	deny map[string]error
}

func (f denyOpenFs) Open(name string) (afero.File, error) {
	if err, ok := f.deny[name]; ok {
		return nil, err
	}
	return f.Fs.Open(name)
}

// TestEffectiveTrust_AbsentApprovalsStore_NormalPending is the CONTROL case
// for the fail-closed preamble: neither approvals directory has EVER been
// created (a fresh project, HOME pointed at an empty temp dir — exactly
// TestEffectiveTrust_DefaultRecords_NothingApprovedOrRejected's setup). This
// must resolve as an ordinary pending decision — the normal "nothing
// reviewed yet" outcome — and must NOT record a strictness finding. A fresh
// checkout with no approvals recorded yet is not a fault.
func TestEffectiveTrust_AbsentApprovalsStore_NormalPending(t *testing.T) {
	resetStrictness(t)
	t.Setenv("HOME", t.TempDir())
	fs := afero.NewMemMapFs()

	mark := strictness.Checkpoint()
	res, err := EffectiveTrust(nil, EffectiveTrustRequest{
		Ref:        trust.Ref{RepoURL: trustRepo, Bundle: "b", Kind: trust.KindFragment, Name: "f"},
		Posture:    postureCtxOf(trust.Ref{RepoURL: trustRepo, Bundle: "b", Kind: trust.KindFragment, Name: "f"}),
		Provenance: postureProvOf(trust.Ref{RepoURL: trustRepo, Bundle: "b", Kind: trust.KindFragment, Name: "f"}),
		Payload:    pbytes("x"),
		Form:       rawForm,
		FS:         fs,
	})
	require.NoError(t, err)
	assert.Equal(t, trust.Deny, res.Decision)
	assert.Equal(t, trust.SourcePending, res.Source)
	assert.Empty(t, strictness.Since(mark), "an absent approvals store must never record a strictness finding")
}

// TestEffectiveTrust_UnreadableApprovalsStore_DenyAllAndStrictFatal is the
// DECIDED security fix under test: when the project approvals store EXISTS
// but cannot be read (permission denied listing it), EffectiveTrust must
// deny the item — even one that would otherwise be ALLOWED at an earlier
// step (here, step 2's local-content exemption) — and record a ClassTrust
// strictness finding, mirroring the deleted ledger's own store-open check.
// Proving the override on an IsLocal item is the point: if the preamble
// merely fell through to steps 2-6 on error, this item would wrongly be
// allowed at step 2 before ever consulting the (broken) approvals store.
func TestEffectiveTrust_UnreadableApprovalsStore_DenyAllAndStrictFatal(t *testing.T) {
	resetStrictness(t)
	t.Setenv("HOME", t.TempDir())

	projectDir := filepath.Join(t.TempDir(), ".ctxloom")
	approvalsDir := filepath.Join(projectDir, "approvals")
	fs := afero.NewMemMapFs()
	require.NoError(t, fs.MkdirAll(approvalsDir, 0o755))
	wrapped := denyOpenFs{Fs: fs, deny: map[string]error{approvalsDir: errors.New("permission denied")}}

	cfg := config.NewFixture(config.Fixture{AppPaths: []string{projectDir}})

	mark := strictness.Checkpoint()
	res, err := EffectiveTrust(cfg, EffectiveTrustRequest{
		Ref:        trust.Ref{Bundle: "b", Kind: trust.KindFragment, Name: "f", IsLocal: true},
		Posture:    postureCtxOf(trust.Ref{Bundle: "b", Kind: trust.KindFragment, Name: "f", IsLocal: true}),
		Provenance: postureProvOf(trust.Ref{Bundle: "b", Kind: trust.KindFragment, Name: "f", IsLocal: true}),
		Payload:    pbytes("x"),
		Form:       rawForm,
		FS:         wrapped,
	})
	require.NoError(t, err)
	assert.Equal(t, trust.Deny, res.Decision, "an unreadable approvals store must deny even an otherwise-local-allowed item")
	assert.NotEqual(t, trust.SourceLocal, res.Source, "the local exemption must never be reached once the store proves unreadable")

	found := strictness.Since(mark)
	require.Len(t, found, 1)
	assert.Equal(t, strictness.ClassTrust, found[0].Class)
	assert.Contains(t, found[0].Message, "approvals store")
}

// TestEffectiveTrust_ProductionInjectedRecords_CorruptedStore_DenyAll is the
// PRODUCTION-SHAPED reproduction of the fail-open bug:
// EVERY real caller (contentGate — see contentGate.allow threading g.records
// into EffectiveTrustRequest.Records — plus review.go and bundle_distill.go)
// builds its ReviewRecords ONCE, up front, and passes it NON-NIL on every
// call. The "records == nil" preamble branch — the ONLY place the
// .readable() fail-closed check used to run — therefore never executes for
// any of them; this is what makes the guard DEAD CODE in production, not a
// property of any one call site.
//
// This test builds records exactly that way (a countersignRecords value
// constructed once, mirroring contentGate's constructor), writes a REAL
// unsigned rejection for an item, corrupts the on-disk store AFTER records
// was already built — a directory replaced by a plain file, the exact
// empirical repro (see TestScratch* experiments that proved Readable's
// ReadDir surfaces "not a directory" while afero.Glob silently swallows the
// identical corruption into zero matches) — and re-materializes.
//
// On TODAY's code this FAILS OPEN: the rejected item silently un-rejects
// (Decision: Allow, Source: SourceRejected never fires) because Rejected()
// walks straight through Store.candidates' swallowed Glob error. The fix
// must deny EVERYTHING once the store proves unreadable — including a LOCAL
// item that never even consults Rejected/Approved — because a deny-all
// session is safer than a silently reopened gate.
func TestEffectiveTrust_ProductionInjectedRecords_CorruptedStore_DenyAll(t *testing.T) {
	resetStrictness(t)
	fs := afero.NewOsFs()
	dir := t.TempDir()
	userDir := filepath.Join(dir, "user-approvals")
	projectDir := filepath.Join(dir, "project-approvals")

	userStore := countersign.NewStore(userDir, fs)
	projectStore := countersign.NewStore(projectDir, fs)

	// rejectedRef is IsLocal — the exact empirical repro ("reject a local
	// fragment"), so the fail-open collapse (Rejected() swallowed -> falls
	// straight through to step 3's local ALLOW) is directly observable as
	// Decision flipping deny->allow and Source flipping rejected->local.
	rejectedRef := trust.Ref{Bundle: "tooling", Kind: trust.KindFragment, Name: "rejected-thing", IsLocal: true}
	require.NoError(t, userStore.WriteUnsignedRefReject(mustCountersignRef(t, rejectedRef)))

	// records built ONCE — exactly the shape contentGate's constructor
	// produces (buildCountersignRecords called at gate-construction time,
	// then threaded into every EffectiveTrust call as Records, non-nil).
	records := countersignRecords{user: userStore, project: projectStore}

	// Sanity: while the store is intact, the rejection is honored — a
	// rejected LOCAL item is denied, ref-level, beating the local exemption.
	sanity, err := EffectiveTrust(nil, EffectiveTrustRequest{
		Ref: rejectedRef, Payload: pbytes("x"), Form: rawForm, Records: records, FS: fs,
		Posture: postureCtxOf(rejectedRef), Provenance: postureProvOf(rejectedRef),
	})
	require.NoError(t, err)
	require.Equal(t, trust.Deny, sanity.Decision)
	require.Equal(t, trust.SourceRejected, sanity.Source, "sanity: the rejection must be honored before any corruption")

	// CORRUPT: replace the user store's directory with a plain file — the
	// empirical repro ("replace ~/.ctxloom/approvals with a plain file").
	require.NoError(t, fs.RemoveAll(userDir))
	require.NoError(t, afero.WriteFile(fs, userDir, []byte("not a directory anymore"), 0o644))

	mark := strictness.Checkpoint()

	// Re-materialize the PREVIOUSLY-REJECTED item: must stay denied, and
	// specifically must NOT resolve as the local ALLOW exemption — that
	// exemption is exactly what a swallowed Rejected() falls through to.
	res, err := EffectiveTrust(nil, EffectiveTrustRequest{
		Ref: rejectedRef, Payload: pbytes("x"), Form: rawForm, Records: records, FS: fs,
		Posture: postureCtxOf(rejectedRef), Provenance: postureProvOf(rejectedRef),
	})
	require.NoError(t, err)
	assert.Equal(t, trust.Deny, res.Decision,
		"a corrupted approvals store must deny the previously-rejected item, never silently un-reject it")
	assert.NotEqual(t, trust.SourceLocal, res.Source,
		"the previously-rejected item must not silently un-reject into the local exemption")

	// A SEPARATE, never-reviewed local item must ALSO be denied — proving
	// this is genuinely "deny everything" once the store proves unreadable,
	// not merely "the one item with reject history stays denied".
	untouchedLocalRef := trust.Ref{Bundle: "b", Kind: trust.KindFragment, Name: "f", IsLocal: true}
	res2, err2 := EffectiveTrust(nil, EffectiveTrustRequest{
		Ref: untouchedLocalRef, Payload: pbytes("y"), Form: rawForm, Records: records, FS: fs,
		Posture: postureCtxOf(untouchedLocalRef), Provenance: postureProvOf(untouchedLocalRef),
	})
	require.NoError(t, err2)
	assert.Equal(t, trust.Deny, res2.Decision, "a corrupted approvals store must deny even local content with no rejection history")
	assert.NotEqual(t, trust.SourceLocal, res2.Source, "the local exemption must never be reached once the store proves unreadable")

	found := strictness.Since(mark)
	require.NotEmpty(t, found, "a corrupted approvals store reached via the production-injected Records path must record a strictness finding")
	assert.Equal(t, strictness.ClassTrust, found[0].Class)
}

// TestEffectiveTrust_ProductionInjectedRecords_FreshEmptyStore_NormalPending
// is the BOUNDARY case the unconditional .readable() gate must NOT trip: a
// brand-new install / fresh project whose approvals directories have NEVER
// been created yet. This is indistinguishable from "corrupt" only if the
// gate conflates "absent" with "unreadable" — it must not, because that
// would deny every single item on a first run, which is its own outage (see
// Store.Readable's own doc: os.IsNotExist degrades to nil, never an error).
//
// This exercises the records value in the SAME shape as
// TestEffectiveTrust_ProductionInjectedRecords_CorruptedStore_DenyAll (built
// once, injected non-nil, mirroring contentGate's constructor) specifically
// to prove the new unconditional check point doesn't regress the "fresh
// project" case now that it also runs on the non-nil/injected path.
func TestEffectiveTrust_ProductionInjectedRecords_FreshEmptyStore_NormalPending(t *testing.T) {
	resetStrictness(t)
	fs := afero.NewOsFs()
	dir := t.TempDir()
	// Deliberately never created — WriteApprove/WriteRefReject lazily
	// MkdirAll on first write, so a fresh install's directories simply do
	// not exist on disk yet.
	userStore := countersign.NewStore(filepath.Join(dir, "user-approvals"), fs)
	projectStore := countersign.NewStore(filepath.Join(dir, "project-approvals"), fs)
	records := countersignRecords{user: userStore, project: projectStore}

	mark := strictness.Checkpoint()

	// An ordinary unsigned/unreviewed remote item resolves the everyday
	// "awaiting review" pending — not the deny-all fail-closed path.
	remoteRef := trust.Ref{RepoURL: trustRepo, Bundle: "tooling", Kind: trust.KindFragment, Name: "never-reviewed"}
	res, err := EffectiveTrust(nil, EffectiveTrustRequest{
		Ref: remoteRef, Payload: pbytes("x"), Form: rawForm, Records: records, FS: fs,
		Posture: postureCtxOf(remoteRef), Provenance: postureProvOf(remoteRef),
	})
	require.NoError(t, err)
	assert.Equal(t, trust.Deny, res.Decision)
	assert.Equal(t, trust.SourcePending, res.Source, "a fresh, never-written approvals store must resolve ordinary pending, not a fail-closed deny")

	// A local item must STILL be allowed via the local exemption — proving
	// the guard genuinely did not fire (a false trip would deny this too).
	localRef := trust.Ref{Bundle: "b", Kind: trust.KindFragment, Name: "f", IsLocal: true}
	res2, err2 := EffectiveTrust(nil, EffectiveTrustRequest{
		Ref: localRef, Payload: pbytes("y"), Form: rawForm, Records: records, FS: fs,
		Posture: postureCtxOf(localRef), Provenance: postureProvOf(localRef),
	})
	require.NoError(t, err2)
	assert.Equal(t, trust.Allow, res2.Decision, "a fresh install must not deny local content — the readable() gate must not false-trip on 'never created yet'")
	assert.Equal(t, trust.SourceLocal, res2.Source)

	assert.Empty(t, strictness.Since(mark), "a fresh/absent approvals store must never record a strictness finding, even reached via the injected-records path")
}

// This is the MIRROR trap test. The package's existing trap coverage runs
// the APPROVE direction (a corrupted approval resolves pending, never allow),
// where collapsing "corrupt" onto "absent" is safe. This is the REJECT
// direction, where the identical collapse is a fail-OPEN: a signed rejection
// record whose bytes have been corrupted at the CONTENT level (it opens fine,
// it just will not unarmor) reads as "not countersigned", and "not
// countersigned" on the reject path is benign — the item silently un-rejects
// and falls through to the local exemption.
//
// The corruption here is deliberately NOT the I/O-error variant already
// covered above (a directory replaced by a file, permission denied): the
// directory lists fine, the file opens fine, only its CONTENT is garbage.
func TestEffectiveTrust_CorruptedRejectSignature_StaysDenied(t *testing.T) {
	resetStrictness(t)
	fs := afero.NewOsFs()
	dir := t.TempDir()
	userDir := filepath.Join(dir, "user-approvals")
	signer := testSigner(t)

	userStore := countersign.NewStore(userDir, fs)
	projectStore := countersign.NewStore(filepath.Join(dir, "project-approvals"), fs)

	rejectedRef := trust.Ref{Bundle: "tooling", Kind: trust.KindFragment, Name: "rejected-thing", IsLocal: true}
	require.NoError(t, userStore.WriteRefReject(mustCountersignRef(t, rejectedRef), signer))

	records := countersignRecords{user: userStore, project: projectStore}

	// CORRUPT THE CONTENT ONLY: same filename, same index hash, same
	// permissions — bytes that are not a signature.
	matches, gerr := afero.Glob(fs, filepath.Join(userDir, "*.sig"))
	require.NoError(t, gerr)
	require.Len(t, matches, 1)
	require.NoError(t, afero.WriteFile(fs, matches[0], []byte("-----BEGIN SSH SIGNATURE-----\ngarbage\n"), 0o644))

	mark := strictness.Checkpoint()

	res, err := EffectiveTrust(nil, EffectiveTrustRequest{
		Ref: rejectedRef, Payload: pbytes("x"), Form: rawForm, Records: records, FS: fs,
		Posture: postureCtxOf(rejectedRef), Provenance: postureProvOf(rejectedRef),
	})
	require.NoError(t, err)
	assert.Equal(t, trust.Deny, res.Decision,
		"a corrupted REJECT record must not silently un-reject the item")
	assert.NotEqual(t, trust.SourceLocal, res.Source,
		"the local exemption is exactly what a swallowed rejection falls through to")

	found := strictness.Since(mark)
	require.NotEmpty(t, found, "an unparseable record in the approvals store must record a strictness finding")
	assert.Equal(t, strictness.ClassTrust, found[0].Class)
}
