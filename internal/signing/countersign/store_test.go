package countersign

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
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
	assert.Empty(t, s.Dir())
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

	_, found := s.LatestApprove(signing.KindFragments, "x#fragments/y", signing.FormRaw)
	assert.False(t, found)

	require.NoError(t, s.AppendIndex(IndexEntry{
		Ref: "x#fragments/y", Kind: "fragments", Form: "raw", Assertion: "approve",
		Principal: "ben@abbitt.me", PayloadHash: "sha256:aaa", ReviewedAt: "2026-01-01T00:00:00Z",
	}))
	require.NoError(t, s.AppendIndex(IndexEntry{
		Ref: "x#fragments/y", Kind: "fragments", Form: "raw", Assertion: "approve",
		Principal: "ben@abbitt.me", PayloadHash: "sha256:bbb", ReviewedAt: "2026-06-01T00:00:00Z",
	}))

	got, found := s.LatestApprove(signing.KindFragments, "x#fragments/y", signing.FormRaw)
	require.True(t, found)
	assert.Equal(t, "sha256:bbb", got.PayloadHash, "the LATEST entry (by reviewed_at) must win")
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
