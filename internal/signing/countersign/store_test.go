package countersign

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/ctxloom/ctxloom/internal/signing"
	"github.com/ctxloom/ctxloom/internal/signing/allowedsigners"
)

// denyFs wraps an afero.Fs and fails Open for any path in deny — a fake
// (not a mock) that simulates "permission denied" / "I/O error" reads
// without touching real OS file permissions (no chmod, no root-skip
// flakiness), used to exercise Store.Readable's "exists but cannot be
// read" branch deterministically.
type denyFs struct {
	afero.Fs
	deny map[string]error
}

func (f denyFs) Open(name string) (afero.File, error) {
	if err, ok := f.deny[name]; ok {
		return nil, err
	}
	return f.Fs.Open(name)
}

// testSigner returns an ephemeral in-memory ed25519 ssh.Signer and its
// ssh.PublicKey, mirroring package signing's own newTestSigner (unexported,
// not reusable across packages).
func testSigner(t *testing.T) (ssh.Signer, ssh.PublicKey) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := ssh.NewSignerFromSigner(priv)
	require.NoError(t, err)
	return signer, signer.PublicKey()
}

func rootTrusting(principal string, pub ssh.PublicKey, namespaces ...string) *allowedsigners.Store {
	return allowedsigners.NewStore(allowedsigners.Entry{
		Principals: []string{principal},
		Namespaces: namespaces,
		PublicKey:  pub,
	})
}

func TestStore_WriteApprove_ThenVerified(t *testing.T) {
	signer, pub := testSigner(t)
	fs := afero.NewMemMapFs()
	s := NewStore("/store", fs)

	payload := []byte("fragment body")
	require.NoError(t, s.WriteApprove(signing.KindFragments, "acme/tooling#fragments/x", signing.FormRaw, payload, signer))

	root := rootTrusting("ben@abbitt.me", pub, signing.NamespaceApprove)
	principal, ok := s.VerifiedApprove(signing.KindFragments, "acme/tooling#fragments/x", signing.FormRaw, payload, root, time.Now())
	assert.True(t, ok)
	assert.Equal(t, "ben@abbitt.me", principal)
}

// TRAP TEST: a signature file present at the CORRECT index hash (so it is
// found as a candidate) but whose body is corrupted must resolve to
// not-verified, never to an approval.
func TestStore_CorruptedSignatureBodyAtCorrectIndex_NeverVerifies(t *testing.T) {
	signer, pub := testSigner(t)
	fs := afero.NewMemMapFs()
	s := NewStore("/store", fs)

	payload := []byte("fragment body")
	require.NoError(t, s.WriteApprove(signing.KindFragments, "acme/tooling#fragments/x", signing.FormRaw, payload, signer))

	// Find the written file and corrupt its body in place — the filename
	// (the index) is untouched, so it is still found as a candidate.
	matches, err := afero.Glob(fs, "/store/*.sig")
	require.NoError(t, err)
	require.Len(t, matches, 1)
	data, err := afero.ReadFile(fs, matches[0])
	require.NoError(t, err)
	corrupted := []byte(string(data))
	for i := len(corrupted) - 30; i < len(corrupted)-15; i++ {
		if corrupted[i] == 'A' {
			corrupted[i] = 'Z'
		} else {
			corrupted[i] = 'A'
		}
	}
	require.NoError(t, afero.WriteFile(fs, matches[0], corrupted, 0o644))

	root := rootTrusting("ben@abbitt.me", pub, signing.NamespaceApprove)
	principal, ok := s.VerifiedApprove(signing.KindFragments, "acme/tooling#fragments/x", signing.FormRaw, payload, root, time.Now())
	assert.False(t, ok, "a corrupted signature body must never resolve as approved, even at the right index")
	assert.Empty(t, principal)
}

func TestStore_UntrustedKey_NeverVerifies(t *testing.T) {
	signer, _ := testSigner(t)
	_, otherPub := testSigner(t)
	fs := afero.NewMemMapFs()
	s := NewStore("/store", fs)

	payload := []byte("fragment body")
	require.NoError(t, s.WriteApprove(signing.KindFragments, "acme/tooling#fragments/x", signing.FormRaw, payload, signer))

	root := rootTrusting("someone-else@example.com", otherPub, signing.NamespaceApprove)
	_, ok := s.VerifiedApprove(signing.KindFragments, "acme/tooling#fragments/x", signing.FormRaw, payload, root, time.Now())
	assert.False(t, ok, "an untrusted key's countersignature is not an approval")
}

func TestStore_EditedBytes_NeverVerifies(t *testing.T) {
	signer, pub := testSigner(t)
	fs := afero.NewMemMapFs()
	s := NewStore("/store", fs)

	require.NoError(t, s.WriteApprove(signing.KindFragments, "acme/tooling#fragments/x", signing.FormRaw, []byte("original body"), signer))

	root := rootTrusting("ben@abbitt.me", pub, signing.NamespaceApprove)
	_, ok := s.VerifiedApprove(signing.KindFragments, "acme/tooling#fragments/x", signing.FormRaw, []byte("edited body"), root, time.Now())
	assert.False(t, ok, "an approval of the original bytes must go pending once the bytes change")
}

func TestStore_ContentReject_MatchesAnyRef(t *testing.T) {
	signer, pub := testSigner(t)
	fs := afero.NewMemMapFs()
	s := NewStore("/store", fs)

	payload := []byte("malicious body")
	require.NoError(t, s.WriteContentReject(signing.KindFragments, signing.FormRaw, payload, signer))

	root := rootTrusting("lead@team.example", pub, signing.NamespaceReject)
	// The content-reject payload omits ref entirely; a query never even
	// carries one — VerifiedContentReject matches "these bytes" full stop.
	_, ok := s.VerifiedContentReject(signing.KindFragments, signing.FormRaw, payload, root, time.Now())
	assert.True(t, ok, "content-reject must match regardless of which ref the bytes are queried under")
}

func TestStore_RefReject_StickyAcrossContentChange(t *testing.T) {
	signer, pub := testSigner(t)
	fs := afero.NewMemMapFs()
	s := NewStore("/store", fs)

	require.NoError(t, s.WriteRefReject(signing.KindFragments, "acme/tooling#fragments/x", signer))

	root := rootTrusting("lead@team.example", pub, signing.NamespaceReject)
	_, ok := s.VerifiedRefReject(signing.KindFragments, "acme/tooling#fragments/x", root, time.Now())
	assert.True(t, ok, "the ref-level rejection is form/content-agnostic")
}

func TestStore_EmptyStore_NoCandidates(t *testing.T) {
	_, pub := testSigner(t)
	fs := afero.NewMemMapFs()
	s := NewStore("/nonexistent", fs)

	root := rootTrusting("ben@abbitt.me", pub, signing.NamespaceApprove)
	_, ok := s.VerifiedApprove(signing.KindFragments, "x#fragments/y", signing.FormRaw, []byte("z"), root, time.Now())
	assert.False(t, ok, "an empty/nonexistent store has no candidates and never approves")
}

// A nil *Store (e.g. "no project store configured") behaves as an empty one.
func TestStore_NilStore_IsSafe(t *testing.T) {
	var s *Store
	_, pub := testSigner(t)
	root := rootTrusting("ben@abbitt.me", pub, signing.NamespaceApprove)
	_, ok := s.VerifiedApprove(signing.KindFragments, "x#fragments/y", signing.FormRaw, []byte("z"), root, time.Now())
	assert.False(t, ok)
}

func TestStore_ApproveRawDoesNotApproveDistilled(t *testing.T) {
	signer, pub := testSigner(t)
	fs := afero.NewMemMapFs()
	s := NewStore("/store", fs)

	payload := []byte("identical bytes")
	require.NoError(t, s.WriteApprove(signing.KindFragments, "acme/tooling#fragments/x", signing.FormRaw, payload, signer))

	root := rootTrusting("ben@abbitt.me", pub, signing.NamespaceApprove)
	_, ok := s.VerifiedApprove(signing.KindFragments, "acme/tooling#fragments/x", signing.FormDistilled, payload, root, time.Now())
	assert.False(t, ok, "an approval of the raw form must not validate the distilled form, even with identical bytes")
}

// Two approve signatures over the SAME (kind, ref, form, payload) by
// DIFFERENT signers must both persist (distinct filenames) — writing the
// second must not clobber the first.
func TestStore_TwoSignersSameContent_BothPersist(t *testing.T) {
	signerA, pubA := testSigner(t)
	signerB, pubB := testSigner(t)
	fs := afero.NewMemMapFs()
	s := NewStore("/store", fs)

	payload := []byte("body")
	require.NoError(t, s.WriteApprove(signing.KindFragments, "acme/tooling#fragments/x", signing.FormRaw, payload, signerA))
	require.NoError(t, s.WriteApprove(signing.KindFragments, "acme/tooling#fragments/x", signing.FormRaw, payload, signerB))

	matches, err := afero.Glob(fs, "/store/*.sig")
	require.NoError(t, err)
	assert.Len(t, matches, 2, "distinct signers over identical content must not clobber each other's file")

	rootA := rootTrusting("a@example.com", pubA, signing.NamespaceApprove)
	_, okA := s.VerifiedApprove(signing.KindFragments, "acme/tooling#fragments/x", signing.FormRaw, payload, rootA, time.Now())
	assert.True(t, okA)

	rootB := rootTrusting("b@example.com", pubB, signing.NamespaceApprove)
	_, okB := s.VerifiedApprove(signing.KindFragments, "acme/tooling#fragments/x", signing.FormRaw, payload, rootB, time.Now())
	assert.True(t, okB)
}

// --- the degraded, unsigned path ---------------------------------------------

func TestStore_UnsignedApprove_RoundTrip(t *testing.T) {
	fs := afero.NewMemMapFs()
	s := NewStore("/store", fs)
	payload := []byte("local body")

	assert.False(t, s.HasUnsignedApprove(signing.KindFragments, "x#fragments/y", signing.FormRaw, payload))
	require.NoError(t, s.WriteUnsignedApprove(signing.KindFragments, "x#fragments/y", signing.FormRaw, payload))
	assert.True(t, s.HasUnsignedApprove(signing.KindFragments, "x#fragments/y", signing.FormRaw, payload))
}

func TestStore_UnsignedApprove_DoesNotSatisfySignedVerification(t *testing.T) {
	fs := afero.NewMemMapFs()
	s := NewStore("/store", fs)
	payload := []byte("local body")
	require.NoError(t, s.WriteUnsignedApprove(signing.KindFragments, "x#fragments/y", signing.FormRaw, payload))

	_, pub := testSigner(t)
	root := rootTrusting("nobody@example.com", pub, signing.NamespaceApprove)
	_, ok := s.VerifiedApprove(signing.KindFragments, "x#fragments/y", signing.FormRaw, payload, root, time.Now())
	assert.False(t, ok, "an unsigned marker must never satisfy the cryptographic verification path")
}

// --- the display-only sidecar index -------------------------------------------

func TestStore_Index_AppendAndLatestApprove(t *testing.T) {
	fs := afero.NewMemMapFs()
	s := NewStore("/store", fs)

	_, found, err := s.LatestApprove(signing.KindFragments, "x#fragments/y", signing.FormRaw)
	require.NoError(t, err)
	assert.False(t, found)

	require.NoError(t, s.AppendIndex(IndexEntry{
		Ref: "x#fragments/y", Kind: "fragments", Form: "raw", Assertion: "approve",
		Principal: "ben@abbitt.me", PayloadHash: "sha256:aaa", ReviewedAt: "2026-01-01T00:00:00Z",
	}))
	require.NoError(t, s.AppendIndex(IndexEntry{
		Ref: "x#fragments/y", Kind: "fragments", Form: "raw", Assertion: "approve",
		Principal: "ben@abbitt.me", PayloadHash: "sha256:bbb", ReviewedAt: "2026-06-01T00:00:00Z",
	}))

	got, found, err := s.LatestApprove(signing.KindFragments, "x#fragments/y", signing.FormRaw)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, "sha256:bbb", got.PayloadHash, "the LATEST entry (by reviewed_at) must win")
}

// TestStore_LatestApprove_ScopedToKindRefForm pins U137-F13's refutation: the
// register filed the sidecar index as NOPAY on the theory that its only
// documented consumer ("ctxloom approvals list") does not exist — but
// operations.review (review.go:378, UPDATE-vs-NEW labelling + diff base) and
// operations/trust.go:718 (post-approval index write) are real, live
// production callers of AppendIndex/LatestApprove, rg-verified. This test
// pins the exact scoping behaviour that caller depends on: a chronologically
// LATER entry for a different kind, ref, or form must never win — if it did,
// operations.review would offer the wrong diff base (or label an UPDATE for
// the wrong item), which is precisely the substituted-content risk the index
// exists to avoid.
func TestStore_LatestApprove_ScopedToKindRefForm(t *testing.T) {
	fs := afero.NewMemMapFs()
	s := NewStore("/store", fs)

	// The real entry we care about — earlier in time.
	require.NoError(t, s.AppendIndex(IndexEntry{
		Ref: "x#fragments/y", Kind: "fragments", Form: "raw", Assertion: "approve",
		PayloadHash: "sha256:real", ReviewedAt: "2026-01-01T00:00:00Z",
	}))
	// Chronologically LATER entries that must NOT be selected: different ref,
	// different kind, different form, respectively.
	require.NoError(t, s.AppendIndex(IndexEntry{
		Ref: "x#fragments/OTHER", Kind: "fragments", Form: "raw", Assertion: "approve",
		PayloadHash: "sha256:wrong-ref", ReviewedAt: "2026-06-01T00:00:00Z",
	}))
	require.NoError(t, s.AppendIndex(IndexEntry{
		Ref: "x#fragments/y", Kind: "hooks", Form: "raw", Assertion: "approve",
		PayloadHash: "sha256:wrong-kind", ReviewedAt: "2026-06-01T00:00:00Z",
	}))
	require.NoError(t, s.AppendIndex(IndexEntry{
		Ref: "x#fragments/y", Kind: "fragments", Form: "distilled", Assertion: "approve",
		PayloadHash: "sha256:wrong-form", ReviewedAt: "2026-06-01T00:00:00Z",
	}))

	got, found, err := s.LatestApprove(signing.KindFragments, "x#fragments/y", signing.FormRaw)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, "sha256:real", got.PayloadHash, "a later entry for a different kind/ref/form must never win")
}

func TestStore_Index_NeverConfusedWithSignedVerification(t *testing.T) {
	// The index is untrusted display metadata: appending an entry alone must
	// never make anything verify as approved.
	fs := afero.NewMemMapFs()
	s := NewStore("/store", fs)
	require.NoError(t, s.AppendIndex(IndexEntry{
		Ref: "x#fragments/y", Kind: "fragments", Form: "raw", Assertion: "approve",
		PayloadHash: "sha256:aaa", ReviewedAt: "2026-01-01T00:00:00Z",
	}))

	_, pub := testSigner(t)
	root := rootTrusting("ben@abbitt.me", pub, signing.NamespaceApprove)
	_, ok := s.VerifiedApprove(signing.KindFragments, "x#fragments/y", signing.FormRaw, []byte("body"), root, time.Now())
	assert.False(t, ok)
}

func TestStore_UnsignedRefReject_RoundTrip(t *testing.T) {
	fs := afero.NewMemMapFs()
	s := NewStore("/store", fs)

	assert.False(t, s.HasUnsignedRefReject(signing.KindFragments, "x#fragments/y"))
	require.NoError(t, s.WriteUnsignedRefReject(signing.KindFragments, "x#fragments/y"))
	assert.True(t, s.HasUnsignedRefReject(signing.KindFragments, "x#fragments/y"))
}

// --- Readable: absent vs unreadable (fail-closed preamble seam) --------------

// A store directory that has never been written to (no decisions recorded
// yet — the normal fresh-project/fresh-user shape) must read as FINE, never
// an error: EffectiveTrust's preamble uses this to decide whether to deny
// everything, and denying every fresh checkout would be its own outage.
func TestStore_Readable_AbsentDirectory_IsNotAnError(t *testing.T) {
	fs := afero.NewMemMapFs()
	s := NewStore("/store", fs)
	assert.NoError(t, s.Readable())
}

// A nil *Store (e.g. "no project store configured") reads as absent too.
func TestStore_Readable_NilStore_IsNotAnError(t *testing.T) {
	var s *Store
	assert.NoError(t, s.Readable())
}

// A store directory that EXISTS but cannot even be listed (permission
// denied / I/O error opening it) must surface as an error — it might be
// hiding a REJECTION, and silently reading it as "empty" would let step 1
// (REJECTED is supreme) miss it.
func TestStore_Readable_DirectoryExistsButUnlistable_IsAnError(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, fs.MkdirAll("/store", 0o755))
	wrapped := denyFs{Fs: fs, deny: map[string]error{"/store": errors.New("permission denied")}}
	s := NewStore("/store", wrapped)

	err := s.Readable()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "permission denied")
}

// A store directory that lists fine but contains a record file that cannot
// be opened (corrupted / permission-denied at the file level) must ALSO
// surface as an error — the same "might be hiding a rejection" reasoning
// applies per-file, not just per-directory.
func TestStore_Readable_FileWithinUnreadable_IsAnError(t *testing.T) {
	signer, _ := testSigner(t)
	fs := afero.NewMemMapFs()
	s := NewStore("/store", fs)
	require.NoError(t, s.WriteRefReject(signing.KindFragments, "acme/tooling#fragments/x", signer))

	matches, err := afero.Glob(fs, "/store/*.sig")
	require.NoError(t, err)
	require.Len(t, matches, 1)

	wrapped := denyFs{Fs: fs, deny: map[string]error{matches[0]: errors.New("permission denied")}}
	s2 := NewStore("/store", wrapped)

	err2 := s2.Readable()
	require.Error(t, err2)
	assert.Contains(t, err2.Error(), "permission denied")
}

// --- an unconfigured store must be an ERROR, never a CWD-relative store ------

// U137-F01. A Store built over dir "" is not "a store with nothing in it" —
// it is a store nobody configured, which happens for real when $HOME cannot
// be resolved and operations' user-store construction swallows the error.
// Readable() is the fail-closed gate EffectiveTrust consults, so an
// unconfigured store MUST surface there: every rejection recorded in the user
// store is invisible for the whole session otherwise.
func TestStore_Readable_EmptyDir_IsAnError(t *testing.T) {
	s := NewStore("", afero.NewOsFs())

	err := s.Readable()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no directory configured")
}

// U137-F02. filepath.Join("", x) == x, so every read path on a dir "" store
// resolves against the PROCESS WORKING DIRECTORY. Unsigned markers are
// honoured with zero cryptographic verification, so a marker file committed
// at a repo root would be an unconditional approval of attacker-chosen bytes.
// An unconfigured store must read as nothing at all.
func TestStore_UnconfiguredStore_DoesNotReadTheWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	fs := afero.NewOsFs()
	configured := NewStore(dir, fs)
	require.NoError(t, configured.WriteUnsignedApprove(signing.KindFragments, "acme#fragments/x", signing.FormRaw, []byte("body")))

	markers, err := afero.Glob(fs, filepath.Join(dir, "*.unsigned"))
	require.NoError(t, err)
	require.Len(t, markers, 1, "the marker must exist in the working directory for this test to mean anything")

	unconfigured := NewStore("", fs)
	assert.False(t, unconfigured.HasUnsignedApprove(signing.KindFragments, "acme#fragments/x", signing.FormRaw, []byte("body")),
		"an unconfigured store must not honour a marker sitting in the working directory")
}

// U137-F02, signed half: candidates() globs "<dir>/<hash>.*.sig", which for
// dir "" is a working-directory-relative pattern.
func TestStore_UnconfiguredStore_DoesNotVerifyFromTheWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	signer, pub := testSigner(t)
	root := rootTrusting("reviewer@example.com", pub, signing.NamespaceReject)
	fs := afero.NewOsFs()

	configured := NewStore(dir, fs)
	require.NoError(t, configured.WriteRefReject(signing.KindFragments, "acme#fragments/x", signer))
	_, ok := configured.VerifiedRefReject(signing.KindFragments, "acme#fragments/x", root, time.Now())
	require.True(t, ok, "the signature must verify from the configured store for this test to mean anything")

	unconfigured := NewStore("", fs)
	_, ok = unconfigured.VerifiedRefReject(signing.KindFragments, "acme#fragments/x", root, time.Now())
	assert.False(t, ok, "an unconfigured store must not verify a signature sitting in the working directory")
}

// U137-F02, write half: an unconfigured store must refuse to write rather
// than scatter approval records across the working directory.
func TestStore_UnconfiguredStore_RefusesToWrite(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	signer, _ := testSigner(t)
	s := NewStore("", afero.NewOsFs())

	assert.Error(t, s.WriteApprove(signing.KindFragments, "acme#fragments/x", signing.FormRaw, []byte("body"), signer))
	assert.Error(t, s.WriteUnsignedApprove(signing.KindFragments, "acme#fragments/x", signing.FormRaw, []byte("body")))
	assert.Error(t, s.AppendIndex(IndexEntry{Ref: "acme#fragments/x"}))
}

// --- an approval that pinned nothing must not be written ---------------------

// U137-F04. countersignRecords.Approved returns false for an empty payload
// before it touches any store, so a record written over an empty payload can
// never be honoured — yet the write succeeds and the user is shown
// "approved" with a key fingerprint. Refusing the write is the fail-loud form
// of a rule the reader already enforces.
func TestStore_WriteApprove_EmptyPayload_IsRefused(t *testing.T) {
	signer, _ := testSigner(t)
	fs := afero.NewMemMapFs()
	s := NewStore("/store", fs)

	err := s.WriteApprove(signing.KindFragments, "acme#fragments/x", signing.FormRaw, nil, signer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty payload")

	err = s.WriteUnsignedApprove(signing.KindFragments, "acme#fragments/x", signing.FormRaw, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty payload")
}

// The reject side is the mirror: a REF reject legitimately pins no bytes
// (it blocks the ref whatever its content becomes), so it must still work.
func TestStore_WriteRefReject_EmptyPayload_IsStillAllowed(t *testing.T) {
	signer, _ := testSigner(t)
	fs := afero.NewMemMapFs()
	s := NewStore("/store", fs)

	assert.NoError(t, s.WriteRefReject(signing.KindFragments, "acme#fragments/x", signer))
	assert.NoError(t, s.WriteUnsignedRefReject(signing.KindFragments, "acme#fragments/x"))
}

// --- the sidecar index must not be destroyed by a corrupt read ---------------

// U137-F08. readIndex() maps an unmarshal error onto nil, indistinguishable
// from "no index yet", and AppendIndex then rewrites the whole file from that
// nil — one truncated write destroys the entire approval history. The index
// is display-only, but it is what labels an item UPDATE and supplies the diff
// base, so losing it makes substituted bytes look like a first-time item.
func TestStore_AppendIndex_CorruptIndex_RefusesRatherThanTruncates(t *testing.T) {
	dir := t.TempDir()
	fs := afero.NewOsFs()
	s := NewStore(dir, fs)

	corrupt := []byte("- ref: acme#fragments/x\n  this is not: [valid\n")
	require.NoError(t, afero.WriteFile(fs, filepath.Join(dir, "index.yaml"), corrupt, 0o644))

	err := s.AppendIndex(IndexEntry{Ref: "acme#fragments/y", Kind: "fragments", Assertion: "approve"})
	require.Error(t, err, "appending onto an unparseable index must fail, not silently replace it")

	after, rerr := afero.ReadFile(fs, filepath.Join(dir, "index.yaml"))
	require.NoError(t, rerr)
	assert.Equal(t, corrupt, after, "the existing index must be left exactly as it was")
}

// LatestApprove is the display-side reader of the same file. A corrupt index
// must not read as "there was never a prior approval" — that silently
// relabels an UPDATE as NEW.
func TestStore_LatestApprove_CorruptIndex_IsNotSilentlyEmpty(t *testing.T) {
	dir := t.TempDir()
	fs := afero.NewOsFs()
	s := NewStore(dir, fs)

	require.NoError(t, afero.WriteFile(fs, filepath.Join(dir, "index.yaml"), []byte("- ref: x\n  bad: [\n"), 0o644))

	_, found, err := s.LatestApprove(signing.KindFragments, "acme#fragments/x", signing.FormRaw)
	require.Error(t, err, "an index this store cannot parse must not answer 'no prior approval'")
	assert.False(t, found)
}

// An ABSENT index is the normal fresh shape and must stay quiet — the whole
// point of the error channel is that it separates absent from corrupt.
func TestStore_LatestApprove_AbsentIndex_IsNotAnError(t *testing.T) {
	s := NewStore(t.TempDir(), afero.NewOsFs())

	_, found, err := s.LatestApprove(signing.KindFragments, "acme#fragments/x", signing.FormRaw)
	assert.NoError(t, err)
	assert.False(t, found)
}

// --- U137-F03: a record that will not even UNARMOR is not "absent" -----------

// The mirror of TestStore_Readable_FileWithinUnreadable_IsAnError, for
// content rather than I/O. A .sig file that opens fine but whose bytes are
// not a parseable armored signature cannot be distinguished from a
// SUPPRESSED REJECTION: VerifyCountersignature collapses the unarmor failure
// into (false, ""), Rejected() then answers "not rejected", and the item is
// re-exposed. Readable() is the only place that can tell the difference,
// because it is the only pass that sees every file in the store rather than
// the ones whose index hash a particular query happens to reconstruct.
func TestStore_Readable_UnparseableSignature_IsAnError(t *testing.T) {
	signer, _ := testSigner(t)
	dir := t.TempDir()
	fs := afero.NewOsFs()
	s := NewStore(dir, fs)
	require.NoError(t, s.WriteRefReject(signing.KindFragments, "acme/tooling#fragments/x", signer))

	matches, err := afero.Glob(fs, filepath.Join(dir, "*.sig"))
	require.NoError(t, err)
	require.Len(t, matches, 1)
	require.NoError(t, afero.WriteFile(fs, matches[0], []byte("not an armored signature at all\n"), 0o644))

	rerr := s.Readable()
	require.Error(t, rerr, "a .sig this store cannot parse must not read as 'nothing recorded'")
	assert.Contains(t, rerr.Error(), "unparseable")
}

// A well-formed signature that simply does not VERIFY (wrong key, wrong
// bytes) is NOT a Readable() error — that is the normal "not proven"
// outcome the store is designed around, and turning it into a hard error
// would deny every session that carries one stale record.
func TestStore_Readable_UnverifiableButParseableSignature_IsNotAnError(t *testing.T) {
	signer, _ := testSigner(t)
	dir := t.TempDir()
	fs := afero.NewOsFs()
	s := NewStore(dir, fs)
	require.NoError(t, s.WriteRefReject(signing.KindFragments, "acme/tooling#fragments/x", signer))

	assert.NoError(t, s.Readable())
}

// Non-record files (the display-only index.yaml, an editor's leftovers) are
// not signatures and must not be parsed as such.
func TestStore_Readable_NonSignatureFiles_AreIgnored(t *testing.T) {
	dir := t.TempDir()
	fs := afero.NewOsFs()
	s := NewStore(dir, fs)

	require.NoError(t, afero.WriteFile(fs, filepath.Join(dir, "index.yaml"), []byte("[]\n"), 0o644))
	require.NoError(t, afero.WriteFile(fs, filepath.Join(dir, "notes.txt"), []byte("hi\n"), 0o644))

	assert.NoError(t, s.Readable())
}
