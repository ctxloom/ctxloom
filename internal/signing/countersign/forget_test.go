package countersign

import (
	"errors"
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/signing"
)

// Forgetting a decision.
//
// The store holds three states — approved, rejected, undecided — and the two
// write paths only ever move an item INTO one of the first two. These tests
// pin the way back out: the record is removed, so the next query answers
// exactly what it answered before anyone decided anything.
//
// Both assertion families are checked in both directions. A remove that took
// out everything under the directory would satisfy "the decision is gone"
// while destroying every neighbouring decision, so each case also asserts a
// second, untouched record still answers yes.

// TestForgetApprove_RemovesTheApprovalAndNothingElse: the approval stops
// verifying, and an approval of DIFFERENT bytes at the same ref survives.
func TestForgetApprove_RemovesTheApprovalAndNothingElse(t *testing.T) {
	signer, pub := testSigner(t)
	s := NewStore("/store", afero.NewMemMapFs())
	root := rootTrusting("ben@abbitt.me", pub, signing.NamespaceApprove)

	const ref = "acme/tooling#fragments/x"
	target := []byte("the reviewed body")
	neighbour := []byte("a different body entirely")
	require.NoError(t, s.WriteApprove(ref, signing.AttestFragmentRaw, target, signer))
	require.NoError(t, s.WriteApprove(ref, signing.AttestFragmentRaw, neighbour, signer))

	_, ok := s.VerifiedApprove(ref, signing.AttestFragmentRaw, target, root, time.Now())
	require.True(t, ok, "the approval must be in force before it can meaningfully be forgotten")

	removed, err := s.ForgetApprove(ref, signing.AttestFragmentRaw, target)
	require.NoError(t, err)
	assert.Equal(t, 1, removed)

	_, ok = s.VerifiedApprove(ref, signing.AttestFragmentRaw, target, root, time.Now())
	assert.False(t, ok, "the forgotten approval must no longer verify")

	_, ok = s.VerifiedApprove(ref, signing.AttestFragmentRaw, neighbour, root, time.Now())
	assert.True(t, ok, "forgetting one decision must not take out the store's other records")
}

// TestForgetRefReject_RemovesTheStickyBlock: the sticky ref block is the
// durable half of a rejection, so clearing it is the durable half of
// forgetting one.
func TestForgetRefReject_RemovesTheStickyBlock(t *testing.T) {
	signer, pub := testSigner(t)
	s := NewStore("/store", afero.NewMemMapFs())
	root := rootTrusting("ben@abbitt.me", pub, signing.NamespaceReject)

	const rejected = "acme/tooling#fragments/x"
	const other = "acme/tooling#fragments/y"
	require.NoError(t, s.WriteRefReject(rejected, signer))
	require.NoError(t, s.WriteRefReject(other, signer))

	_, ok := s.VerifiedRefReject(rejected, root, time.Now())
	require.True(t, ok)

	removed, err := s.ForgetRefReject(rejected)
	require.NoError(t, err)
	assert.Equal(t, 1, removed)

	_, ok = s.VerifiedRefReject(rejected, root, time.Now())
	assert.False(t, ok, "the forgotten ref block must no longer verify")
	_, ok = s.VerifiedRefReject(other, root, time.Now())
	assert.True(t, ok, "a rejection of a different ref must survive")
}

// TestForgetContentReject_RemovesTheBytewiseBlock: the content component is
// ref-omitted, so it is looked up — and cleared — by bytes and form alone.
func TestForgetContentReject_RemovesTheBytewiseBlock(t *testing.T) {
	signer, pub := testSigner(t)
	s := NewStore("/store", afero.NewMemMapFs())
	root := rootTrusting("ben@abbitt.me", pub, signing.NamespaceReject)

	payload := []byte("rm -rf danger")
	require.NoError(t, s.WriteContentReject(signing.AttestFragmentRaw, payload, signer))
	_, ok := s.VerifiedContentReject(signing.AttestFragmentRaw, payload, root, time.Now())
	require.True(t, ok)

	removed, err := s.ForgetContentReject(signing.AttestFragmentRaw, payload)
	require.NoError(t, err)
	assert.Equal(t, 1, removed)

	_, ok = s.VerifiedContentReject(signing.AttestFragmentRaw, payload, root, time.Now())
	assert.False(t, ok, "the forgotten content block must no longer verify")
}

// TestForget_ClearsTheUnsignedMarkerToo. The degraded path (spec §9.5) records
// a bare marker whose EXISTENCE is the whole decision, and it is a separate
// file from the signed record. A forget that swept only `.sig` files would
// report a cleared decision and leave the item exactly as withheld as before —
// exit 0, a success line, no effect.
func TestForget_ClearsTheUnsignedMarkerToo(t *testing.T) {
	s := NewStore("/store", afero.NewMemMapFs())

	const ref = "acme/tooling#fragments/x"
	payload := []byte("body")
	require.NoError(t, s.WriteUnsignedApprove(ref, signing.AttestFragmentRaw, payload))
	require.NoError(t, s.WriteUnsignedRefReject(ref))
	require.NoError(t, s.WriteUnsignedContentReject(signing.AttestFragmentRaw, payload))
	require.True(t, s.HasUnsignedApprove(ref, signing.AttestFragmentRaw, payload))
	require.True(t, s.HasUnsignedRefReject(ref))
	require.True(t, s.HasUnsignedContentReject(signing.AttestFragmentRaw, payload))

	for _, forget := range []func() (int, error){
		func() (int, error) { return s.ForgetApprove(ref, signing.AttestFragmentRaw, payload) },
		func() (int, error) { return s.ForgetRefReject(ref) },
		func() (int, error) { return s.ForgetContentReject(signing.AttestFragmentRaw, payload) },
	} {
		removed, err := forget()
		require.NoError(t, err)
		assert.Equal(t, 1, removed)
	}

	assert.False(t, s.HasUnsignedApprove(ref, signing.AttestFragmentRaw, payload))
	assert.False(t, s.HasUnsignedRefReject(ref))
	assert.False(t, s.HasUnsignedContentReject(signing.AttestFragmentRaw, payload))
}

// TestForget_ClearsEverySignersRecord: several people may have countersigned
// the same bytes at the same ref. Clearing the decision clears the decision —
// leaving one signer's record behind would leave the item decided while
// reporting it forgotten.
func TestForget_ClearsEverySignersRecord(t *testing.T) {
	alice, alicePub := testSigner(t)
	bob, bobPub := testSigner(t)
	s := NewStore("/store", afero.NewMemMapFs())

	const ref = "acme/tooling#fragments/x"
	payload := []byte("body")
	require.NoError(t, s.WriteApprove(ref, signing.AttestFragmentRaw, payload, alice))
	require.NoError(t, s.WriteApprove(ref, signing.AttestFragmentRaw, payload, bob))

	removed, err := s.ForgetApprove(ref, signing.AttestFragmentRaw, payload)
	require.NoError(t, err)
	assert.Equal(t, 2, removed, "both signers' records are the same decision and go together")

	aliceRoot := rootTrusting("alice@example.com", alicePub, signing.NamespaceApprove)
	bobRoot := rootTrusting("bob@example.com", bobPub, signing.NamespaceApprove)
	_, ok := s.VerifiedApprove(ref, signing.AttestFragmentRaw, payload, aliceRoot, time.Now())
	assert.False(t, ok, "alice's record must be gone")
	_, ok = s.VerifiedApprove(ref, signing.AttestFragmentRaw, payload, bobRoot, time.Now())
	assert.False(t, ok, "bob's record must be gone too")
}

// TestForget_NothingRecorded_IsNotAnError: forgetting an undecided item is the
// no-op it reads as. It reports zero records so a caller can tell "there was
// nothing to clear" from "a decision was cleared" without inventing either.
func TestForget_NothingRecorded_IsNotAnError(t *testing.T) {
	s := NewStore("/store", afero.NewMemMapFs())
	removed, err := s.ForgetApprove("acme/tooling#fragments/x", signing.AttestFragmentRaw, []byte("body"))
	require.NoError(t, err)
	assert.Zero(t, removed)
}

// TestForget_UnconfiguredStore_Refuses. A store with no directory resolves
// every path against the PROCESS WORKING DIRECTORY (filepath.Join("", x) == x
// — see Store.configured), so a forget that proceeded would delete files out
// of whatever directory ctxloom happened to be run from. Every other path
// through this type refuses; so does this one.
func TestForget_UnconfiguredStore_Refuses(t *testing.T) {
	s := NewStore("", afero.NewMemMapFs())
	_, err := s.ForgetRefReject("acme/tooling#fragments/x")
	require.Error(t, err)
}

// TestForget_InvalidHeader_Refuses. A header outside the closed vocabulary
// cannot address a record, so "I removed nothing" would be a true statement
// and a useless one: the caller asked to clear a decision and must be told the
// request was unanswerable rather than answered in the negative.
func TestForget_InvalidHeader_Refuses(t *testing.T) {
	s := NewStore("/store", afero.NewMemMapFs())
	_, err := s.ForgetApprove("acme/tooling#fragments/x", signing.AttestationForm("not-a-form"), []byte("body"))
	require.Error(t, err)
}

// TestForget_RemoveFailureSurfaces: a record this process cannot delete is a
// decision still in force. Reporting success there is the silent no-op in its
// purest form — the user is told the item is back to pending and it is not.
func TestForget_RemoveFailureSurfaces(t *testing.T) {
	signer, _ := testSigner(t)
	base := afero.NewMemMapFs()
	s := NewStore("/store", base)
	const ref = "acme/tooling#fragments/x"
	require.NoError(t, s.WriteRefReject(ref, signer))

	blocked := NewStore("/store", removeDenyFs{Fs: base, err: errors.New("permission denied")})
	_, err := blocked.ForgetRefReject(ref)
	require.Error(t, err)
}

// removeDenyFs fails every Remove, standing in for a store directory this
// process may read but not write.
type removeDenyFs struct {
	afero.Fs
	err error
}

func (f removeDenyFs) Remove(string) error { return f.err }
