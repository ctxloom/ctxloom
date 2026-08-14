package countersign

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/ctxloom/ctxloom/internal/shared/iox"
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
	require.NoError(t, s.WriteApprove("acme/tooling#fragments/x", signing.AttestFragmentRaw, payload, signer))

	root := rootTrusting("ben@abbitt.me", pub, signing.NamespaceApprove)
	principal, ok := s.VerifiedApprove("acme/tooling#fragments/x", signing.AttestFragmentRaw, payload, root, time.Now())
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
	require.NoError(t, s.WriteApprove("acme/tooling#fragments/x", signing.AttestFragmentRaw, payload, signer))

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
	principal, ok := s.VerifiedApprove("acme/tooling#fragments/x", signing.AttestFragmentRaw, payload, root, time.Now())
	assert.False(t, ok, "a corrupted signature body must never resolve as approved, even at the right index")
	assert.Empty(t, principal)
}

func TestStore_UntrustedKey_NeverVerifies(t *testing.T) {
	signer, _ := testSigner(t)
	_, otherPub := testSigner(t)
	fs := afero.NewMemMapFs()
	s := NewStore("/store", fs)

	payload := []byte("fragment body")
	require.NoError(t, s.WriteApprove("acme/tooling#fragments/x", signing.AttestFragmentRaw, payload, signer))

	root := rootTrusting("someone-else@example.com", otherPub, signing.NamespaceApprove)
	_, ok := s.VerifiedApprove("acme/tooling#fragments/x", signing.AttestFragmentRaw, payload, root, time.Now())
	assert.False(t, ok, "an untrusted key's countersignature is not an approval")
}

func TestStore_EditedBytes_NeverVerifies(t *testing.T) {
	signer, pub := testSigner(t)
	fs := afero.NewMemMapFs()
	s := NewStore("/store", fs)

	require.NoError(t, s.WriteApprove("acme/tooling#fragments/x", signing.AttestFragmentRaw, []byte("original body"), signer))

	root := rootTrusting("ben@abbitt.me", pub, signing.NamespaceApprove)
	_, ok := s.VerifiedApprove("acme/tooling#fragments/x", signing.AttestFragmentRaw, []byte("edited body"), root, time.Now())
	assert.False(t, ok, "an approval of the original bytes must go pending once the bytes change")
}

func TestStore_ContentReject_MatchesAnyRef(t *testing.T) {
	signer, pub := testSigner(t)
	fs := afero.NewMemMapFs()
	s := NewStore("/store", fs)

	payload := []byte("malicious body")
	require.NoError(t, s.WriteContentReject(signing.AttestFragmentRaw, payload, signer))

	root := rootTrusting("lead@team.example", pub, signing.NamespaceReject)
	// The content-reject payload omits ref entirely; a query never even
	// carries one — VerifiedContentReject matches "these bytes" full stop.
	_, ok := s.VerifiedContentReject(signing.AttestFragmentRaw, payload, root, time.Now())
	assert.True(t, ok, "content-reject must match regardless of which ref the bytes are queried under")
}

func TestStore_RefReject_StickyAcrossContentChange(t *testing.T) {
	signer, pub := testSigner(t)
	fs := afero.NewMemMapFs()
	s := NewStore("/store", fs)

	require.NoError(t, s.WriteRefReject("acme/tooling#fragments/x", signer))

	root := rootTrusting("lead@team.example", pub, signing.NamespaceReject)
	_, ok := s.VerifiedRefReject("acme/tooling#fragments/x", root, time.Now())
	assert.True(t, ok, "the ref-level rejection is form/content-agnostic")
}

func TestStore_EmptyStore_NoCandidates(t *testing.T) {
	_, pub := testSigner(t)
	fs := afero.NewMemMapFs()
	s := NewStore("/nonexistent", fs)

	root := rootTrusting("ben@abbitt.me", pub, signing.NamespaceApprove)
	_, ok := s.VerifiedApprove("x#fragments/y", signing.AttestFragmentRaw, []byte("z"), root, time.Now())
	assert.False(t, ok, "an empty/nonexistent store has no candidates and never approves")
}

// A nil *Store (e.g. "no project store configured") behaves as an empty one.
func TestStore_NilStore_IsSafe(t *testing.T) {
	var s *Store
	_, pub := testSigner(t)
	root := rootTrusting("ben@abbitt.me", pub, signing.NamespaceApprove)
	_, ok := s.VerifiedApprove("x#fragments/y", signing.AttestFragmentRaw, []byte("z"), root, time.Now())
	assert.False(t, ok)
}

func TestStore_ApproveRawDoesNotApproveDistilled(t *testing.T) {
	signer, pub := testSigner(t)
	fs := afero.NewMemMapFs()
	s := NewStore("/store", fs)

	payload := []byte("identical bytes")
	require.NoError(t, s.WriteApprove("acme/tooling#fragments/x", signing.AttestFragmentRaw, payload, signer))

	root := rootTrusting("ben@abbitt.me", pub, signing.NamespaceApprove)
	_, ok := s.VerifiedApprove("acme/tooling#fragments/x", signing.AttestFragmentDistilled, payload, root, time.Now())
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
	require.NoError(t, s.WriteApprove("acme/tooling#fragments/x", signing.AttestFragmentRaw, payload, signerA))
	require.NoError(t, s.WriteApprove("acme/tooling#fragments/x", signing.AttestFragmentRaw, payload, signerB))

	matches, err := afero.Glob(fs, "/store/*.sig")
	require.NoError(t, err)
	assert.Len(t, matches, 2, "distinct signers over identical content must not clobber each other's file")

	rootA := rootTrusting("a@example.com", pubA, signing.NamespaceApprove)
	_, okA := s.VerifiedApprove("acme/tooling#fragments/x", signing.AttestFragmentRaw, payload, rootA, time.Now())
	assert.True(t, okA)

	rootB := rootTrusting("b@example.com", pubB, signing.NamespaceApprove)
	_, okB := s.VerifiedApprove("acme/tooling#fragments/x", signing.AttestFragmentRaw, payload, rootB, time.Now())
	assert.True(t, okB)
}

// --- the degraded, unsigned path ---------------------------------------------

func TestStore_UnsignedApprove_RoundTrip(t *testing.T) {
	fs := afero.NewMemMapFs()
	s := NewStore("/store", fs)
	payload := []byte("local body")

	assert.False(t, s.HasUnsignedApprove("x#fragments/y", signing.AttestFragmentRaw, payload))
	require.NoError(t, s.WriteUnsignedApprove("x#fragments/y", signing.AttestFragmentRaw, payload))
	assert.True(t, s.HasUnsignedApprove("x#fragments/y", signing.AttestFragmentRaw, payload))
}

func TestStore_UnsignedApprove_DoesNotSatisfySignedVerification(t *testing.T) {
	fs := afero.NewMemMapFs()
	s := NewStore("/store", fs)
	payload := []byte("local body")
	require.NoError(t, s.WriteUnsignedApprove("x#fragments/y", signing.AttestFragmentRaw, payload))

	_, pub := testSigner(t)
	root := rootTrusting("nobody@example.com", pub, signing.NamespaceApprove)
	_, ok := s.VerifiedApprove("x#fragments/y", signing.AttestFragmentRaw, payload, root, time.Now())
	assert.False(t, ok, "an unsigned marker must never satisfy the cryptographic verification path")
}

// --- the display-only sidecar index -------------------------------------------

func TestStore_Index_AppendAndLatestApprove(t *testing.T) {
	fs := afero.NewMemMapFs()
	s := NewStore("/store", fs)

	_, found, err := s.LatestApprove("x#fragments/y", signing.FormRaw)
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

	got, found, err := s.LatestApprove("x#fragments/y", signing.FormRaw)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, "sha256:bbb", got.PayloadHash, "the LATEST entry (by reviewed_at) must win")
}

// TestStore_LatestApprove_ScopedToRefAndLayoutForm pins the exact scoping the
// UPDATE-vs-NEW label and the diff base depend on: a chronologically LATER entry
// for a different ref or a different form must never win — if it did,
// operations.review would offer the wrong diff base (or label an UPDATE for the
// wrong item), which is precisely the substituted-content risk the index exists
// to avoid.
//
// The KIND LABEL is deliberately not a query term, and this test pins that too:
// a later entry whose kind is spelled differently but whose ref and form match
// DOES win, because the ref already embeds the kind directory and because a
// record written under an older kind vocabulary must still be recognized as a
// prior approval after a contract bump — stale, rather than absent.
func TestStore_LatestApprove_ScopedToRefAndLayoutForm(t *testing.T) {
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
		Ref: "x#fragments/y", Kind: "fragments", Form: "distilled", Assertion: "approve",
		PayloadHash: "sha256:wrong-form", ReviewedAt: "2026-06-01T00:00:00Z",
	}))

	got, found, err := s.LatestApprove("x#fragments/y", signing.FormRaw)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, "sha256:real", got.PayloadHash, "a later entry for a different ref or form must never win")

	// A record whose KIND is spelled in a superseded vocabulary still counts:
	// staleness must read as an update, never as a first-time item.
	require.NoError(t, s.AppendIndex(IndexEntry{
		Ref: "x#fragments/y", Kind: "agentskills", Form: "raw", Assertion: "approve",
		PayloadHash: "sha256:legacy-kind-label", ReviewedAt: "2026-07-01T00:00:00Z",
	}))
	got, found, err = s.LatestApprove("x#fragments/y", signing.FormRaw)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, "sha256:legacy-kind-label", got.PayloadHash,
		"a prior approval recorded under an older kind vocabulary must still be recognized")
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
	_, ok := s.VerifiedApprove("x#fragments/y", signing.AttestFragmentRaw, []byte("body"), root, time.Now())
	assert.False(t, ok)
}

func TestStore_UnsignedRefReject_RoundTrip(t *testing.T) {
	fs := afero.NewMemMapFs()
	s := NewStore("/store", fs)

	assert.False(t, s.HasUnsignedRefReject("x#fragments/y"))
	require.NoError(t, s.WriteUnsignedRefReject("x#fragments/y"))
	assert.True(t, s.HasUnsignedRefReject("x#fragments/y"))
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
	require.NoError(t, s.WriteRefReject("acme/tooling#fragments/x", signer))

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

// A Store built over dir "" is not "a store with nothing in it" —
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

// filepath.Join("", x) == x, so every read path on a dir "" store
// resolves against the PROCESS WORKING DIRECTORY. Unsigned markers are
// honoured with zero cryptographic verification, so a marker file committed
// at a repo root would be an unconditional approval of attacker-chosen bytes.
// An unconfigured store must read as nothing at all.
func TestStore_UnconfiguredStore_DoesNotReadTheWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	fs := afero.NewOsFs()
	configured := NewStore(dir, fs)
	require.NoError(t, configured.WriteUnsignedApprove("acme#fragments/x", signing.AttestFragmentRaw, []byte("body")))

	markers, err := afero.Glob(fs, filepath.Join(dir, "*.unsigned"))
	require.NoError(t, err)
	require.Len(t, markers, 1, "the marker must exist in the working directory for this test to mean anything")

	unconfigured := NewStore("", fs)
	assert.False(t, unconfigured.HasUnsignedApprove("acme#fragments/x", signing.AttestFragmentRaw, []byte("body")),
		"an unconfigured store must not honour a marker sitting in the working directory")
}

// The signed half of the same defect: candidates() globs "<dir>/<hash>.*.sig",
// which for dir "" is a working-directory-relative pattern.
func TestStore_UnconfiguredStore_DoesNotVerifyFromTheWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	signer, pub := testSigner(t)
	root := rootTrusting("reviewer@example.com", pub, signing.NamespaceReject)
	fs := afero.NewOsFs()

	configured := NewStore(dir, fs)
	require.NoError(t, configured.WriteRefReject("acme#fragments/x", signer))
	_, ok := configured.VerifiedRefReject("acme#fragments/x", root, time.Now())
	require.True(t, ok, "the signature must verify from the configured store for this test to mean anything")

	unconfigured := NewStore("", fs)
	_, ok = unconfigured.VerifiedRefReject("acme#fragments/x", root, time.Now())
	assert.False(t, ok, "an unconfigured store must not verify a signature sitting in the working directory")
}

// The write half: an unconfigured store must refuse to write rather
// than scatter approval records across the working directory.
func TestStore_UnconfiguredStore_RefusesToWrite(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	signer, _ := testSigner(t)
	s := NewStore("", afero.NewOsFs())

	assert.Error(t, s.WriteApprove("acme#fragments/x", signing.AttestFragmentRaw, []byte("body"), signer))
	assert.Error(t, s.WriteUnsignedApprove("acme#fragments/x", signing.AttestFragmentRaw, []byte("body")))
	assert.Error(t, s.AppendIndex(IndexEntry{Ref: "acme#fragments/x"}))
}

// --- an approval that pinned nothing must not be written ---------------------

// countersignRecords.Approved returns false for an empty payload
// before it touches any store, so a record written over an empty payload can
// never be honoured — yet the write succeeds and the user is shown
// "approved" with a key fingerprint. Refusing the write is the fail-loud form
// of a rule the reader already enforces.
func TestStore_WriteApprove_EmptyPayload_IsRefused(t *testing.T) {
	signer, _ := testSigner(t)
	fs := afero.NewMemMapFs()
	s := NewStore("/store", fs)

	err := s.WriteApprove("acme#fragments/x", signing.AttestFragmentRaw, nil, signer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty payload")

	err = s.WriteUnsignedApprove("acme#fragments/x", signing.AttestFragmentRaw, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty payload")
}

// The reject side is the mirror: a REF reject legitimately pins no bytes
// (it blocks the ref whatever its content becomes), so it must still work.
func TestStore_WriteRefReject_EmptyPayload_IsStillAllowed(t *testing.T) {
	signer, _ := testSigner(t)
	fs := afero.NewMemMapFs()
	s := NewStore("/store", fs)

	assert.NoError(t, s.WriteRefReject("acme#fragments/x", signer))
	assert.NoError(t, s.WriteUnsignedRefReject("acme#fragments/x"))
}

// --- the sidecar index must not be destroyed by a corrupt read ---------------

// readIndex() maps an unmarshal error onto nil, indistinguishable
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

	_, found, err := s.LatestApprove("acme#fragments/x", signing.FormRaw)
	require.Error(t, err, "an index this store cannot parse must not answer 'no prior approval'")
	assert.False(t, found)
}

// An ABSENT index is the normal fresh shape and must stay quiet — the whole
// point of the error channel is that it separates absent from corrupt.
func TestStore_LatestApprove_AbsentIndex_IsNotAnError(t *testing.T) {
	s := NewStore(t.TempDir(), afero.NewOsFs())

	_, found, err := s.LatestApprove("acme#fragments/x", signing.FormRaw)
	assert.NoError(t, err)
	assert.False(t, found)
}

// --- a record that will not even UNARMOR is not "absent" ---------------------

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
	require.NoError(t, s.WriteRefReject("acme/tooling#fragments/x", signer))

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
	require.NoError(t, s.WriteRefReject("acme/tooling#fragments/x", signer))

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

// --- the closed attestation vocabulary, enforced at the store boundary -------

// A form outside the closed vocabulary must be REFUSED on write: a record
// signed under a form no query can reconstruct is unreachable forever, so
// writing it would tell the user "approved" and leave the item pending.
func TestStore_Write_RefusesAFormOutsideTheClosedVocabulary(t *testing.T) {
	signer, _ := testSigner(t)
	fs := afero.NewMemMapFs()
	s := NewStore("/store", fs)

	const rogue signing.AttestationForm = "exec"
	assert.Error(t, s.WriteApprove("acme#mcp/x", rogue, []byte("body"), signer))
	assert.Error(t, s.WriteUnsignedApprove("acme#mcp/x", rogue, []byte("body")))
	assert.Error(t, s.WriteContentReject(rogue, []byte("body"), signer))
}

// The read side is the mirror, and fails CLOSED rather than loud: a lookup
// under an unrecognized form finds nothing, so nothing is ever approved on the
// strength of a form the vocabulary does not contain.
func TestStore_Read_AFormOutsideTheClosedVocabularyFindsNothing(t *testing.T) {
	signer, pub := testSigner(t)
	root := rootTrusting("fixture@example.com", pub, signing.NamespaceApprove)
	fs := afero.NewMemMapFs()
	s := NewStore("/store", fs)
	payload := []byte("body")

	require.NoError(t, s.WriteApprove("acme#mcp/x", signing.AttestExecMCP, payload, signer))
	_, ok := s.VerifiedApprove("acme#mcp/x", signing.AttestExecMCP, payload, root, time.Now())
	require.True(t, ok, "the record under the real form must verify, or this test proves nothing")

	const rogue signing.AttestationForm = "exec"
	_, ok = s.VerifiedApprove("acme#mcp/x", rogue, payload, root, time.Now())
	assert.False(t, ok, "an unrecognized form must resolve to 'nothing recorded'")
	assert.False(t, s.HasUnsignedApprove("acme#mcp/x", rogue, payload))
}

// TestStore_WriteNamespaceIsDerivedFromTheAssertion pins the one thing the
// signer and the verifier must never disagree about.
//
// write used to take `namespace` as a parameter INDEPENDENT of
// header.Assertion, hand-re-encoded at each of the three assertion wrappers,
// while VerifyCountersignature DERIVES it from the assertion. The two
// namespaces are a domain separator (spec §1) precisely so a rejection can
// never be replayed as an approval — so a mismatch at one wrapper produces a
// record that is written, reported to the user, and then never verifiable by
// anyone.
//
// On the REJECT path that is a fail-OPEN: an unverifiable rejection reads as
// "nothing rejected", which is benign, so the item is silently un-rejected.
// The test asserts on the record's namespace by construction: a root that
// trusts the key ONLY for the assertion's own namespace must verify it, and a
// root that trusts it only for the OTHER namespace must not.
func TestStore_WriteNamespaceIsDerivedFromTheAssertion(t *testing.T) {
	signer, pub := testSigner(t)
	payload := []byte("reviewed bytes")
	now := time.Now()

	approveOnly := rootTrusting("ben@abbitt.me", pub, signing.NamespaceApprove)
	rejectOnly := rootTrusting("ben@abbitt.me", pub, signing.NamespaceReject)

	t.Run("approve is signed under the approve namespace", func(t *testing.T) {
		s := NewStore("/store", afero.NewMemMapFs())
		require.NoError(t, s.WriteApprove("bundle:a", signing.AttestFragmentRaw, payload, signer))

		_, ok := s.VerifiedApprove("bundle:a", signing.AttestFragmentRaw, payload, approveOnly, now)
		assert.True(t, ok, "an approve record must verify under a root trusting the approve namespace")

		_, ok = s.VerifiedApprove("bundle:a", signing.AttestFragmentRaw, payload, rejectOnly, now)
		assert.False(t, ok, "it must NOT verify under a root trusting only the reject namespace")
	})

	t.Run("content reject is signed under the reject namespace", func(t *testing.T) {
		s := NewStore("/store", afero.NewMemMapFs())
		require.NoError(t, s.WriteContentReject(signing.AttestFragmentRaw, payload, signer))

		_, ok := s.VerifiedContentReject(signing.AttestFragmentRaw, payload, rejectOnly, now)
		assert.True(t, ok, "a reject record must verify under a root trusting the reject namespace")

		_, ok = s.VerifiedContentReject(signing.AttestFragmentRaw, payload, approveOnly, now)
		assert.False(t, ok, "it must NOT verify under a root trusting only the approve namespace")
	})

	t.Run("ref reject is signed under the reject namespace", func(t *testing.T) {
		s := NewStore("/store", afero.NewMemMapFs())
		require.NoError(t, s.WriteRefReject("bundle:a", signer))

		_, ok := s.VerifiedRefReject("bundle:a", rejectOnly, now)
		assert.True(t, ok, "a ref-reject record must verify under a root trusting the reject namespace")

		_, ok = s.VerifiedRefReject("bundle:a", approveOnly, now)
		assert.False(t, ok, "it must NOT verify under a root trusting only the approve namespace")
	})
}

// TestStore_DirectoryWithGlobMetacharacters_StillFindsRejections is the
// fail-open candidates' swallowed Glob error actually produces.
//
// candidates built a PATTERN by joining s.dir with the index hash, so every
// metacharacter in the store's own path was interpreted rather than matched.
// An unterminated '[' anywhere in it makes filepath.Match return ErrBadPattern
// and candidates return nil — for every query, forever.
//
// On the approve path that leaves items pending, which is safe. On the REJECT
// path nil candidates is indistinguishable from "nothing rejected", so a
// rejection a human recorded silently stops applying. Readable() cannot catch
// it: it lists the directory by its literal name, so it reports a perfectly
// healthy store while every lookup into it comes back empty.
func TestStore_DirectoryWithGlobMetacharacters_StillFindsRejections(t *testing.T) {
	signer, pub := testSigner(t)
	root := rootTrusting("ben@abbitt.me", pub, signing.NamespaceApprove, signing.NamespaceReject)
	now := time.Now()
	payload := []byte("refused bytes")

	for _, dir := range []string{
		"/home/u/proj[wip/.ctxloom/approvals", // unterminated '[' — ErrBadPattern
		"/home/u/proj[ab]/.ctxloom/approvals", // a valid class that matches a DIFFERENT directory
		`/home/u/pro\j/.ctxloom/approvals`,    // backslash is an escape to Match, not a literal
	} {
		t.Run(dir, func(t *testing.T) {
			s := NewStore(dir, afero.NewMemMapFs())
			require.NoError(t, s.WriteRefReject("bundle:evil", signer))
			require.NoError(t, s.WriteContentReject(signing.AttestFragmentRaw, payload, signer))

			require.NoError(t, s.Readable(), "the store is physically fine, so nothing warns")

			_, ok := s.VerifiedRefReject("bundle:evil", root, now)
			assert.True(t, ok, "a recorded ref rejection must still be found")

			_, ok = s.VerifiedContentReject(signing.AttestFragmentRaw, payload, root, now)
			assert.True(t, ok, "a recorded content rejection must still be found")
		})
	}
}

// statDenyFs fails Stat for chosen paths while leaving Open/ReadDir working —
// the shape that separates hasUnsigned's probe from Readable's whole-store
// walk, since one stats a single path and the other lists and opens.
type statDenyFs struct {
	afero.Fs
	deny map[string]error
}

func (f statDenyFs) Stat(name string) (os.FileInfo, error) {
	if err, ok := f.deny[name]; ok {
		return nil, err
	}
	return f.Fs.Stat(name)
}

// TestStore_UnsignedRejectMarker_IsNotErasedByAStatFailure pins the reject
// direction of hasUnsigned's error handling.
//
// hasUnsigned returned `err == nil && exists`, folding every stat failure into
// "absent". For an unsigned marker, EXISTENCE IS THE ENTIRE RECORD (§9.5) —
// there is nothing to re-verify — so a stat that fails does not degrade the
// answer, it INVERTS it. On HasUnsignedContentReject and HasUnsignedRefReject
// that is a fail-open: a rejection a human recorded reads as no rejection.
//
// The store-wide guard (Readable, gating EffectiveTrust's preamble) covers the
// ordinary cause, an unreadable directory, because it lists and opens every
// file. It does NOT cover a failure that hides one path from Stat alone, which
// is exactly what this fixture is: Readable reports the store healthy.
func TestStore_UnsignedRejectMarker_IsNotErasedByAStatFailure(t *testing.T) {
	base := afero.NewMemMapFs()
	s := NewStore("/store", base)
	require.NoError(t, s.WriteUnsignedRefReject("bundle:evil"))

	h := signing.CountersignHeader{Assertion: signing.AssertionReject, Ref: "bundle:evil", Form: signing.AttestNone}
	marker := filepath.Join("/store", unsignedFilename(h, nil))

	blind := NewStore("/store", statDenyFs{Fs: base, deny: map[string]error{marker: os.ErrPermission}})

	require.NoError(t, blind.Readable(), "the whole-store guard sees nothing wrong here")
	assert.True(t, blind.HasUnsignedRefReject("bundle:evil"),
		"a recorded unsigned rejection must not be erased by a stat failure")
}

// TestStore_LatestApprove_OrdersByInstantNotByBytes pins the ordering rule
// LatestApprove needs to answer the question it is asked.
//
// It compared ReviewedAt with `>` — a byte comparison of an unparsed free-text
// YAML field. That is correct only while every stamp is UTC Z-suffixed
// RFC3339, which is true of the single writer today and of nothing else: the
// index is a plain YAML file a human may edit, and its doc requires it to
// outlive contract bumps, so it is expected to accumulate records this build
// did not write.
//
// The consequence is not cosmetic. LatestApprove supplies the DIFF BASE and
// decides whether an item is labelled UPDATE or NEW, which is what a reviewer
// looks at to notice substituted bytes.
func TestStore_LatestApprove_OrdersByInstantNotByBytes(t *testing.T) {
	entry := func(stamp, hash string) IndexEntry {
		return IndexEntry{
			Ref: "bundle:a", Kind: "fragment", Form: "raw",
			Assertion:   string(signing.AssertionApprove),
			PayloadHash: hash, ReviewedAt: stamp,
		}
	}

	t.Run("an offset-suffixed stamp does not out-sort a later UTC one", func(t *testing.T) {
		s := NewStore("/store", afero.NewMemMapFs())
		// 13:00+01:00 is 12:00Z — EARLIER than 12:30Z — but sorts later by bytes.
		require.NoError(t, s.AppendIndex(entry("2026-01-01T12:30:00Z", "sha256:actually-latest")))
		require.NoError(t, s.AppendIndex(entry("2026-01-01T13:00:00+01:00", "sha256:earlier")))

		got, ok, err := s.LatestApprove("bundle:a", signing.FormRaw)
		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, "sha256:actually-latest", got.PayloadHash)
	})

	t.Run("an unreadable stamp does not win by sorting high", func(t *testing.T) {
		s := NewStore("/store", afero.NewMemMapFs())
		require.NoError(t, s.AppendIndex(entry("2026-06-01T00:00:00Z", "sha256:actually-latest")))
		// 'y' sorts above every digit, so this used to beat any real stamp.
		require.NoError(t, s.AppendIndex(entry("yesterday-ish", "sha256:unreadable")))

		got, ok, err := s.LatestApprove("bundle:a", signing.FormRaw)
		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, "sha256:actually-latest", got.PayloadHash,
			"a stamp that reads must beat one that does not")
	})
}

// TestStore_ThreeRecordKindsHaveThreeAuthorityModels pins the taxonomy the
// Store doc now states, in the one direction nothing else covers: that an
// unsigned marker really is honoured on FILENAME EXISTENCE alone, and a signed
// record really is not.
//
// The doc used to claim the type "never answers 'is X approved' on the
// strength of a filename alone", which is false of the §9.5 degraded path by
// design. Its forgeability is the reason unsigned writes must never reach the
// committable project store, so it has to be stated rather than implied — and
// a later "hardening" of the marker path that silently made this test fail
// would be changing a documented security property, not fixing a bug.
func TestStore_ThreeRecordKindsHaveThreeAuthorityModels(t *testing.T) {
	_, pub := testSigner(t)
	root := rootTrusting("ben@abbitt.me", pub, signing.NamespaceApprove, signing.NamespaceReject)
	fs := afero.NewMemMapFs()
	s := NewStore("/store", fs)
	payload := []byte("bytes nobody signed")

	h := signing.CountersignHeader{Assertion: signing.AssertionApprove, Ref: "bundle:a", Form: signing.AttestFragmentRaw}

	// (2) Unsigned marker: a file this package's writer never produced, planted
	// directly, is honoured — existence IS the record.
	require.NoError(t, afero.WriteFile(fs, filepath.Join("/store", unsignedFilename(h, payload)), []byte("unsigned\n"), 0o644))
	assert.True(t, s.HasUnsignedApprove("bundle:a", signing.AttestFragmentRaw, payload),
		"an unsigned marker is honoured on filename existence alone (spec §9.5)")

	// (1) Signed record: a planted file at the very same index hash proves
	// nothing, because every candidate is re-verified.
	require.NoError(t, afero.WriteFile(fs, filepath.Join("/store", filename(h, payload, pub)), []byte("not a signature\n"), 0o644))
	_, ok := s.VerifiedApprove("bundle:a", signing.AttestFragmentRaw, payload, root, time.Now())
	assert.False(t, ok, "a signed record is never honoured on filename alone")

	// (3) Sidecar index: still no approval, whatever it says.
	require.NoError(t, s.AppendIndex(IndexEntry{
		Ref: "bundle:a", Kind: "fragment", Form: "raw",
		Assertion:   string(signing.AssertionApprove),
		PayloadHash: "sha256:whatever", ReviewedAt: "2026-01-01T00:00:00Z",
	}))
	_, ok = s.VerifiedApprove("bundle:a", signing.AttestFragmentRaw, payload, root, time.Now())
	assert.False(t, ok, "the display index is never an input to a trust decision")
}

// TestStore_RecordFilenamesMatchTheDocumentedContract pins the shape
// filename/unsignedFilename document, because it is a contract between two
// functions with no compiler check between them: candidates matches
// "<indexHash>." as a literal prefix and hasUnsigned matches the unsigned name
// exactly. Reordering the fields or changing the separator orphans every
// record already on disk — they stay valid and are simply never found again,
// which on the reject path reads as nothing rejected.
func TestStore_RecordFilenamesMatchTheDocumentedContract(t *testing.T) {
	_, pub := testSigner(t)
	payload := []byte("reviewed bytes")
	h := signing.CountersignHeader{Assertion: signing.AssertionApprove, Ref: "bundle:a", Form: signing.AttestFragmentRaw}
	hash := indexHash(h, payload)

	signed := filename(h, payload, pub)
	assert.Equal(t, hash+"."+string(h.Assertion)+"."+keyTag(pub)+".sig", signed)
	assert.True(t, strings.HasPrefix(signed, hash+"."),
		"candidates finds records by this prefix")
	assert.True(t, strings.HasSuffix(signed, ".sig"))

	unsigned := unsignedFilename(h, payload)
	assert.Equal(t, hash+"."+string(h.Assertion)+".unsigned", unsigned)
	assert.False(t, strings.HasSuffix(unsigned, ".sig"),
		"a marker must not be picked up by candidates' .sig filter")
}

// TestStore_Write_UsesDurableWrite pins the ruled site (taskloom
// unbounded-bacon): a countersignature is rotation lineage, and Store.write
// must pass iox.Durable() so a crash cannot silently revert the rename and
// leave a record that was reported "written" actually gone. Durable is a
// no-op on afero.MemMapFs (see iox.Durable's doc), which is why this test —
// unlike the rest of this file — must use a REAL OS filesystem: only that
// backend gives the seam anything to fire on.
func TestStore_Write_UsesDurableWrite(t *testing.T) {
	signer, _ := testSigner(t)
	dir := t.TempDir()
	s := NewStore(dir, afero.NewOsFs())

	var synced []string
	restore := iox.SetSyncDirForTesting(func(d string) error {
		synced = append(synced, d)
		return nil
	})
	defer restore()

	require.NoError(t, s.WriteApprove("acme/tooling#fragments/x", signing.AttestFragmentRaw, []byte("payload"), signer))
	assert.NotEmpty(t, synced, "Store.write must pass iox.Durable(): a countersignature is unrecoverable rotation lineage")
}

// TestStore_WriteIndex_UsesDurableWrite is writeIndex's twin of the above:
// the sidecar index is what labels an item UPDATE vs first-time (see
// AppendIndex's doc) and must survive the same crash the object write does.
func TestStore_WriteIndex_UsesDurableWrite(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir, afero.NewOsFs())

	var synced []string
	restore := iox.SetSyncDirForTesting(func(d string) error {
		synced = append(synced, d)
		return nil
	})
	defer restore()

	require.NoError(t, s.AppendIndex(IndexEntry{
		Ref: "acme/tooling#fragments/x", Kind: "fragment", Form: string(signing.AttestFragmentRaw),
		Assertion: string(signing.AssertionApprove), Principal: "ben@abbitt.me",
		PayloadHash: "sha256:whatever", ReviewedAt: "2026-01-01T00:00:00Z",
	}))
	assert.NotEmpty(t, synced, "writeIndex must pass iox.Durable(): the index is review-integrity-bearing, not cosmetic")
}
