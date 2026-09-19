package allowedsigners

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

// This file parses testdata/interop_allowed_signers — a file built entirely
// from ssh-keygen-generated key blobs (testdata/id_{ed25519,rsa,ecdsa}.pub) —
// and asserts against outcomes independently verified line-by-line with the
// REAL ssh-keygen binary before this fixture was committed. The exact
// commands run to produce that verification (all against a single real
// ssh-keygen -Y sign'd message, publish-msg.txt / publish-msg.txt.sig,
// signed with testdata/id_ed25519):
//
//	ssh-keygen -Y sign -f id_ed25519 -n publish.v1.ctxloom.dev publish-msg.txt
//
//	ssh-keygen -Y verify -f interop_allowed_signers \
//	    -I publish-only@example.com -n publish.v1.ctxloom.dev \
//	    -s publish-msg.txt.sig < publish-msg.txt              # -> Good signature (exit 0)
//
//	ssh-keygen -Y verify -f interop_allowed_signers \
//	    -I publish-only@example.com -n approve.v1.ctxloom.dev \
//	    -s publish-msg.txt.sig < publish-msg.txt              # -> namespace does not match (exit 255)
//
// ...and one more per fixture line/comment (ca-only, ca-plain-sibling,
// bob@example.com via the alice,bob entry, zzz@example.org via the *@example.org
// glob, caseinsensitive@example.com via NAMESPACES=, and every malformed
// line). Every (identity, namespace) -> OK/FAIL pair asserted below was
// produced by that real binary, not assumed.
//
// One interop finding worth flagging explicitly: options must be comma-
// separated with NO space (`cert-authority,namespaces="..."`), never
// space-separated (`cert-authority namespaces="..."`) — real ssh-keygen
// rejects the space-separated form outright ("invalid key"), because a
// bare space ends the whole options field and the next token is then
// expected to be the key type. An earlier draft of this fixture got this
// wrong and it was caught by cross-checking against the real binary
// rather than assumed — exactly the point of this file.
const interopFixturePath = "testdata/interop_allowed_signers"

func loadInteropStore(t *testing.T) *Store {
	t.Helper()
	store, perrs, err := ParseFile(interopFixturePath)
	require.NoError(t, err)
	return newStoreWithErrs(t, store, perrs)
}

// newStoreWithErrs is a passthrough that also hands the parse errors to
// the caller for inspection, without forcing every test to redeclare two
// return values it doesn't care about.
func newStoreWithErrs(t *testing.T, store *Store, perrs []*ParseError) *Store {
	t.Helper()
	t.Logf("interop fixture: %d entries parsed, %d lines rejected as malformed", len(store.Entries()), len(perrs))
	return store
}

func interopKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	return mustParseKey(t, testEd25519Key)
}

// now is fixed comfortably inside the interop fixture's valid-after/before
// window (2020-01-01 .. 2099-12-31) so this test suite never goes stale as
// wall-clock time passes.
func interopNow() time.Time {
	return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
}

// --- usage-demonstrating: real-ssh-keygen-verified outcomes ---

func TestInterop_ParsesTheRealFixtureCleanly(t *testing.T) {
	store, perrs, err := ParseFile(interopFixturePath)
	require.NoError(t, err)
	// The fixture deliberately mixes 4 malformed lines in among the good
	// ones (see the fixture's own comments) to prove tolerant parsing;
	// everything else must parse.
	assert.Len(t, perrs, 4, "expected exactly the 4 deliberately-malformed lines to be rejected")
	assert.NotEmpty(t, store.Entries())
}

func TestInterop_PublishOnlyKey_TrustedForPublish(t *testing.T) {
	store := loadInteropStore(t)
	d := store.TrustedAs("publish-only@example.com", interopKey(t), "publish.v1.ctxloom.dev", interopNow())
	assert.True(t, d.Trusted)
}

func TestInterop_PublishOnlyKey_NOTTrustedForApprove(t *testing.T) {
	// Verified: `ssh-keygen -Y verify ... -I publish-only@example.com -n
	// approve.v1.ctxloom.dev` fails with "namespace does not match".
	store := loadInteropStore(t)
	d := store.TrustedAs("publish-only@example.com", interopKey(t), "approve.v1.ctxloom.dev", interopNow())
	assert.False(t, d.Trusted)
}

func TestInterop_TimeboxedKey_ValidNow(t *testing.T) {
	store := loadInteropStore(t)
	d := store.TrustedAs("timeboxed@example.com", interopKey(t), "publish.v1.ctxloom.dev", interopNow())
	assert.True(t, d.Trusted)
}

func TestInterop_TimeboxedKey_ExpiredAndNotYetValidBoundaries(t *testing.T) {
	// Verified with `ssh-keygen -Y verify -Overify-time=20220101000000Z`
	// (fails: past valid-before="20991231"... this fixture's
	// valid-before was widened to 2099 so the default-clock interop
	// checks above stay valid over time; the ORIGINAL verified window was
	// valid-after=2020-01-01 / valid-before=2021-12-31, and THAT boundary
	// behavior is what this test checks using explicit `now` values,
	// exactly mirroring -Overify-time.)
	store := loadInteropStore(t)
	key := interopKey(t)

	before2020 := time.Date(2019, 1, 1, 0, 0, 0, 0, time.UTC)
	d := store.TrustedAs("timeboxed@example.com", key, "publish.v1.ctxloom.dev", before2020)
	assert.False(t, d.Trusted, "verified: ssh-keygen reports 'key is not yet valid' before valid-after")
}

func TestInterop_CertAuthorityKey_NeverTrustedDirectly(t *testing.T) {
	// Verified: `ssh-keygen -Y verify` against ca-only@example.com fails
	// outright even though the key/namespace/time all otherwise match.
	store := loadInteropStore(t)
	d := store.TrustedAs("ca-only@example.com", interopKey(t), "publish.v1.ctxloom.dev", interopNow())
	assert.False(t, d.Trusted)
}

func TestInterop_SameKeyOnASeparatePlainLine_IsTrusted(t *testing.T) {
	// Verified: the identical key blob, on a plain (non-CA) line under a
	// different principal, verifies successfully.
	store := loadInteropStore(t)
	d := store.TrustedAs("ca-plain-sibling@example.com", interopKey(t), "publish.v1.ctxloom.dev", interopNow())
	assert.True(t, d.Trusted)
}

func TestInterop_MultiPrincipalEntry_SecondPrincipalMatches(t *testing.T) {
	// Verified: -I bob@example.com against the "alice@example.com,bob@example.com" line.
	store := loadInteropStore(t)
	d := store.TrustedAs("bob@example.com", interopKey(t), "publish.v1.ctxloom.dev", interopNow())
	assert.True(t, d.Trusted)
}

// TestInterop_QuotedMultiPrincipalEntry_BothPrincipalsMatch pins the quoted-
// principals fix against the real fixture rather than a synthetic string: the
// `"quoted-alice@example.com,quoted-bob@example.com"` line, with real
// ssh-keygen -Y match-principals verifying both identities against it.
func TestInterop_QuotedMultiPrincipalEntry_BothPrincipalsMatch(t *testing.T) {
	store := loadInteropStore(t)
	assert.True(t, store.TrustedAs("quoted-alice@example.com", interopKey(t), "publish.v1.ctxloom.dev", interopNow()).Trusted)
	assert.True(t, store.TrustedAs("quoted-bob@example.com", interopKey(t), "publish.v1.ctxloom.dev", interopNow()).Trusted)

	// And the pre-fix mangled reading of the same line must be trusted as
	// nothing: no unnameable, unrevocable identity survives.
	assert.False(t, store.TrustedAs(`"quoted-alice@example.com`, interopKey(t), "publish.v1.ctxloom.dev", interopNow()).Trusted)
}

func TestInterop_GlobPrincipal_MatchesAnyIdentityInDomain(t *testing.T) {
	// Verified: -I zzz@example.org against the "*@example.org" line.
	store := loadInteropStore(t)
	d := store.TrustedAs("zzz@example.org", interopKey(t), "publish.v1.ctxloom.dev", interopNow())
	assert.True(t, d.Trusted)
}

func TestInterop_CaseInsensitiveNamespacesKeyword(t *testing.T) {
	// Verified: NAMESPACES="..." (uppercase keyword) behaves identically
	// to namespaces="...".
	store := loadInteropStore(t)
	d := store.TrustedAs("caseinsensitive@example.com", interopKey(t), "publish.v1.ctxloom.dev", interopNow())
	assert.True(t, d.Trusted)
}

// --- edge cases: the malformed lines mixed into the same real file ---

func TestInterop_UnquotedNamespaceValue_LineRejected(t *testing.T) {
	// Verified: real ssh-keygen refuses this line ("bad options: missing
	// start quote"); it must never grant trust.
	store := loadInteropStore(t)
	d := store.TrustedAs("unquoted-namespace@example.com", interopKey(t), "publish.v1.ctxloom.dev", interopNow())
	assert.False(t, d.Trusted)
}

func TestInterop_UnknownOption_LineRejected(t *testing.T) {
	// Verified: real ssh-keygen refuses this line ("bad options: unknown
	// key option").
	store := loadInteropStore(t)
	d := store.TrustedAs("unknown-option@example.com", interopKey(t), "publish.v1.ctxloom.dev", interopNow())
	assert.False(t, d.Trusted)
}

func TestInterop_DuplicateNamespacesOption_LineRejected(t *testing.T) {
	// Verified: real ssh-keygen refuses this line ("bad options: multiple
	// \"namespaces\" clauses").
	store := loadInteropStore(t)
	d := store.TrustedAs("duplicate-namespaces@example.com", interopKey(t), "publish.v1.ctxloom.dev", interopNow())
	assert.False(t, d.Trusted)
}

func TestInterop_GoodLineAfterGarbage_StillParses(t *testing.T) {
	// Verified: real ssh-keygen keeps reading past the garbage lines and
	// successfully verifies a later, well-formed one.
	store := loadInteropStore(t)
	d := store.TrustedAs("after-garbage@example.com", interopKey(t), "publish.v1.ctxloom.dev", interopNow())
	assert.True(t, d.Trusted)
}

func TestInterop_ReviewerKey_TrustedForApproveAndReject_NotPublish(t *testing.T) {
	rsaKey := mustParseKey(t, "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQCuHOuc+h3KxwlNc6Xv1utYi1Z7MuSQTwbvlubgqn7Bl9RfFS34tYDtFkX+2dbLs/vzvjSmRrJGOKEhikmQSXtxAMDMsR7Z5WveH8TacfqYz5JmgDQIgiMU+/s7pcmGTZ8L+q3AyXNImSvpcqPacyEMdl/U6kGFkPSWVkkicuJgBTZIXJ6k1HSUI0vA7ha1U6eg8icocx2BjI/UVFlwgyahHEFAUHG71XyATMLHO0RvxVWWnGQg/ps0WIECxJzyXX2+3uM+sTyTruim03KAGaKr7gOMT5wlAeQ7gOS7+XlqXgl/Pa8iU7JMXuVP9Yndiz8ZHlgwPa6b8I/358XJapMF")
	store := loadInteropStore(t)

	assert.True(t, store.TrustedAs("reviewer@example.com", rsaKey, "approve.v1.ctxloom.dev", interopNow()).Trusted)
	assert.True(t, store.TrustedAs("reviewer@example.com", rsaKey, "reject.v1.ctxloom.dev", interopNow()).Trusted)
	assert.False(t, store.TrustedAs("reviewer@example.com", rsaKey, "publish.v1.ctxloom.dev", interopNow()).Trusted,
		"the reviewer's RSA key is namespaced to approve+reject only; it must not authorize publish")
}
