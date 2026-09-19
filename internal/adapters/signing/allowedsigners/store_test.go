package allowedsigners

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

func mustParseKey(t *testing.T, line string) ssh.PublicKey {
	t.Helper()
	k, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
	require.NoError(t, err)
	return k
}

func fixedNow() time.Time {
	return time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
}

// --- usage-demonstrating: this is the one question the package answers ---

func TestStore_TrustedForNamespace_KeyTrustedInItsListedNamespace(t *testing.T) {
	key := mustParseKey(t, testEd25519Key)
	store := NewStore(Entry{
		Principals: []string{"releases@ctxloom.dev"},
		Namespaces: []string{"publish.v1.ctxloom.dev"},
		PublicKey:  key,
	})

	d := store.TrustedForNamespace(key, "publish.v1.ctxloom.dev", fixedNow())
	assert.True(t, d.Trusted)
	assert.Equal(t, "releases@ctxloom.dev", d.Principal)
	require.NotNil(t, d.Entry)
}

func TestStore_TrustedForNamespace_UnknownKeyIsNotTrusted(t *testing.T) {
	knownKey := mustParseKey(t, testEd25519Key)
	unknownKey := mustParseKey(t, testSKEd25519Key)
	store := NewStore(Entry{
		Principals: []string{"releases@ctxloom.dev"},
		Namespaces: []string{"publish.v1.ctxloom.dev"},
		PublicKey:  knownKey,
	})

	d := store.TrustedForNamespace(unknownKey, "publish.v1.ctxloom.dev", fixedNow())
	assert.False(t, d.Trusted)
	assert.Empty(t, d.Principal)
	assert.Nil(t, d.Entry)
}

func TestStore_TrustedAs_RequiresPrincipalMatch(t *testing.T) {
	key := mustParseKey(t, testEd25519Key)
	store := NewStore(Entry{
		Principals: []string{"ben@abbitt.me"},
		Namespaces: []string{"approve.v1.ctxloom.dev"},
		PublicKey:  key,
	})

	assert.True(t, store.TrustedAs("ben@abbitt.me", key, "approve.v1.ctxloom.dev", fixedNow()).Trusted)
	assert.False(t, store.TrustedAs("someone-else@example.com", key, "approve.v1.ctxloom.dev", fixedNow()).Trusted)
}

// --- THE SECURITY-CRITICAL CHECK: namespaces= is the role system ---
//
// A publish-only key must never be usable to authorize an approve
// assertion. This is what stops a compromised publish-only key (e.g. a CI
// pipeline's signing key, deliberately scoped narrow) from forging an
// approval. These tests are adversarial on purpose: each one plays the
// attacker asking "can I get this key treated as trusted for a namespace
// it was never granted?"

func TestRole_PublishOnlyKeyCannotAuthorizeApprove(t *testing.T) {
	key := mustParseKey(t, testEd25519Key)
	store := NewStore(Entry{
		Principals: []string{"ci-pipeline@ctxloom.dev"},
		Namespaces: []string{"publish.v1.ctxloom.dev"},
		PublicKey:  key,
	})

	approve := store.TrustedForNamespace(key, "approve.v1.ctxloom.dev", fixedNow())
	assert.False(t, approve.Trusted, "a publish-only key must NEVER be trusted to approve")

	reject := store.TrustedForNamespace(key, "reject.v1.ctxloom.dev", fixedNow())
	assert.False(t, reject.Trusted, "a publish-only key must NEVER be trusted to reject")

	publish := store.TrustedForNamespace(key, "publish.v1.ctxloom.dev", fixedNow())
	assert.True(t, publish.Trusted, "the key IS legitimately trusted for the namespace it was granted")
}

func TestRole_ApproveOnlyKeyCannotAuthorizePublish(t *testing.T) {
	// The mirror image: a reviewer's key, scoped to approve/reject,
	// cannot be used to smuggle unreviewed content in as if it were
	// published by a trusted org key.
	key := mustParseKey(t, testEd25519Key)
	store := NewStore(Entry{
		Principals: []string{"lead@team.example"},
		Namespaces: []string{"approve.v1.ctxloom.dev", "reject.v1.ctxloom.dev"},
		PublicKey:  key,
	})

	assert.False(t, store.TrustedForNamespace(key, "publish.v1.ctxloom.dev", fixedNow()).Trusted)
	assert.True(t, store.TrustedForNamespace(key, "approve.v1.ctxloom.dev", fixedNow()).Trusted)
	assert.True(t, store.TrustedForNamespace(key, "reject.v1.ctxloom.dev", fixedNow()).Trusted)
}

func TestRole_NamespaceMatchIsExactNotPrefix(t *testing.T) {
	// A key scoped to "publish.v1.ctxloom.dev" must not be trusted for a
	// namespace that merely shares a prefix or suffix — glob semantics
	// are opt-in (via an explicit '*' in the file), never implicit.
	key := mustParseKey(t, testEd25519Key)
	store := NewStore(Entry{
		Principals: []string{"scoped@example.com"},
		Namespaces: []string{"publish.v1.ctxloom.dev"},
		PublicKey:  key,
	})

	assert.False(t, store.TrustedForNamespace(key, "publish.v1.ctxloom.devEVIL", fixedNow()).Trusted)
	assert.False(t, store.TrustedForNamespace(key, "evilpublish.v1.ctxloom.dev", fixedNow()).Trusted)
	assert.False(t, store.TrustedForNamespace(key, "approve.v1.ctxloom.dev.publish.v1.ctxloom.dev", fixedNow()).Trusted)
}

func TestRole_CompromisedPublishKeyStillCannotApprove_EvenWithMultipleEntries(t *testing.T) {
	// Realistic multi-entry store, mirroring the spec's own example file:
	// a release key and a bundle key (publish-only), and a human's key
	// trusted broadly. The attacker holds ONLY the compromised
	// publish-only key and tries every namespace against it.
	pubOnlyKey := mustParseKey(t, testEd25519Key)
	humanKey := mustParseKey(t, testSKEd25519Key)

	store := NewStore(
		Entry{
			Principals: []string{"bundles@ctxloom.dev"},
			Namespaces: []string{"publish.v1.ctxloom.dev"},
			PublicKey:  pubOnlyKey,
		},
		Entry{
			Principals: []string{"ben@abbitt.me"},
			Namespaces: []string{"approve.v1.ctxloom.dev", "reject.v1.ctxloom.dev", "publish.v1.ctxloom.dev"},
			PublicKey:  humanKey,
		},
	)

	for _, ns := range []string{"approve.v1.ctxloom.dev", "reject.v1.ctxloom.dev"} {
		d := store.TrustedForNamespace(pubOnlyKey, ns, fixedNow())
		assert.Falsef(t, d.Trusted, "compromised publish-only key must not authorize %s", ns)
	}
	// Sanity: the OTHER key, legitimately broad, still works — proves the
	// namespace check is discriminating on the key, not blanket-denying.
	assert.True(t, store.TrustedForNamespace(humanKey, "approve.v1.ctxloom.dev", fixedNow()).Trusted)
}

func TestRole_CertAuthorityEntryNeverGrantsTrustDirectly(t *testing.T) {
	// Verified against real ssh-keygen: a cert-authority-flagged entry
	// refuses to verify a plain signature even when the key, namespace,
	// and validity window all match. This package has no certificate
	// verification, so it must be at least as strict: never trust through
	// a cert-authority entry.
	key := mustParseKey(t, testEd25519Key)
	store := NewStore(Entry{
		Principals:    []string{"ca@example.com"},
		Namespaces:    []string{"publish.v1.ctxloom.dev"},
		CertAuthority: true,
		PublicKey:     key,
	})

	d := store.TrustedForNamespace(key, "publish.v1.ctxloom.dev", fixedNow())
	assert.False(t, d.Trusted, "a cert-authority entry must never grant trust via a direct key match")
}

func TestRole_CertAuthorityDoesNotPoisonASeparatePlainEntryForSameKey(t *testing.T) {
	// Verified against real ssh-keygen: the same key listed once as
	// cert-authority and once plainly still verifies via the plain line.
	key := mustParseKey(t, testEd25519Key)
	store := NewStore(
		Entry{
			Principals:    []string{"ca@example.com"},
			Namespaces:    []string{"publish.v1.ctxloom.dev"},
			CertAuthority: true,
			PublicKey:     key,
		},
		Entry{
			Principals: []string{"plain@example.com"},
			Namespaces: []string{"publish.v1.ctxloom.dev"},
			PublicKey:  key,
		},
	)

	d := store.TrustedForNamespace(key, "publish.v1.ctxloom.dev", fixedNow())
	assert.True(t, d.Trusted)
	assert.Equal(t, "plain@example.com", d.Principal)
}

func TestRole_ExpiredKeyCannotAuthorizeEvenInItsGrantedNamespace(t *testing.T) {
	key := mustParseKey(t, testEd25519Key)
	before := time.Date(2021, 12, 31, 0, 0, 0, 0, time.UTC)
	store := NewStore(Entry{
		Principals:  []string{"expired@example.com"},
		Namespaces:  []string{"publish.v1.ctxloom.dev"},
		ValidBefore: &before,
		PublicKey:   key,
	})

	d := store.TrustedForNamespace(key, "publish.v1.ctxloom.dev", fixedNow()) // fixedNow is 2026
	assert.False(t, d.Trusted, "a key past its valid-before must not authorize anything, even a namespace it was scoped for")
}

func TestRole_NotYetValidKeyCannotAuthorize(t *testing.T) {
	key := mustParseKey(t, testEd25519Key)
	after := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	store := NewStore(Entry{
		Principals: []string{"future@example.com"},
		Namespaces: []string{"publish.v1.ctxloom.dev"},
		ValidAfter: &after,
		PublicKey:  key,
	})

	d := store.TrustedForNamespace(key, "publish.v1.ctxloom.dev", fixedNow())
	assert.False(t, d.Trusted)
}

func TestRole_UnrestrictedKeyIsTrustedEverywhere_DocumentedBroadGrant(t *testing.T) {
	// An entry with no namespaces= option is intentionally broad (real
	// ssh-keygen semantics, not a shortcut). Documented here so the
	// breadth of omitting namespaces= is visible in the test suite, not
	// just prose.
	key := mustParseKey(t, testEd25519Key)
	store := NewStore(Entry{
		Principals: []string{"unrestricted@example.com"},
		PublicKey:  key,
	})

	assert.True(t, store.TrustedForNamespace(key, "publish.v1.ctxloom.dev", fixedNow()).Trusted)
	assert.True(t, store.TrustedForNamespace(key, "approve.v1.ctxloom.dev", fixedNow()).Trusted)
	assert.True(t, store.TrustedForNamespace(key, "reject.v1.ctxloom.dev", fixedNow()).Trusted)
}

// --- Union / precedence ---

func TestUnion_CombinesEntriesFromMultipleStores(t *testing.T) {
	key := mustParseKey(t, testEd25519Key)
	embedded := NewStore(Entry{Principals: []string{"embedded@ctxloom.dev"}, Namespaces: []string{"publish.v1.ctxloom.dev"}, PublicKey: key})
	user := NewStore(Entry{Principals: []string{"user@example.com"}, Namespaces: []string{"approve.v1.ctxloom.dev"}, PublicKey: key})

	combined := Union(embedded, user)
	assert.True(t, combined.TrustedForNamespace(key, "publish.v1.ctxloom.dev", fixedNow()).Trusted)
	assert.True(t, combined.TrustedForNamespace(key, "approve.v1.ctxloom.dev", fixedNow()).Trusted)
}

func TestUnion_NilStoreArgumentIgnored(t *testing.T) {
	key := mustParseKey(t, testEd25519Key)
	only := NewStore(Entry{Principals: []string{"a@example.com"}, Namespaces: []string{"publish.v1.ctxloom.dev"}, PublicKey: key})
	combined := Union(nil, only, nil)
	assert.True(t, combined.TrustedForNamespace(key, "publish.v1.ctxloom.dev", fixedNow()).Trusted)
}

// --- Deletion is fail-safe: an empty/nil store trusts nothing ---

func TestStore_NilStoreTrustsNothing(t *testing.T) {
	var s *Store
	key := mustParseKey(t, testEd25519Key)
	assert.False(t, s.TrustedForNamespace(key, "publish.v1.ctxloom.dev", fixedNow()).Trusted)
}

func TestStore_EmptyStoreTrustsNothing(t *testing.T) {
	key := mustParseKey(t, testEd25519Key)
	assert.False(t, NewStore().TrustedForNamespace(key, "publish.v1.ctxloom.dev", fixedNow()).Trusted)
}

func TestStore_NilKeyIsNeverTrusted(t *testing.T) {
	store := NewStore(Entry{Principals: []string{"a@example.com"}, PublicKey: mustParseKey(t, testEd25519Key)})
	assert.False(t, store.TrustedForNamespace(nil, "publish.v1.ctxloom.dev", fixedNow()).Trusted)
}

// --- a store must be able to say "a location failed to load" ----------------

// The API could not express "the trust root failed to load": an unreadable
// location, an absent one, an empty one and an entirely-garbage one all
// resolved to the same value (nil, or a Store with no entries). A caller
// that wants to refuse when the root did not actually load had nothing to
// ask.
func TestStore_FailedSource_IsRepresentable(t *testing.T) {
	failed := FailedSource("/etc/ctxloom/allowed_signers", errors.New("permission denied"))

	require.NotNil(t, failed)
	assert.Zero(t, failed.Len())
	srcs := failed.Sources()
	require.Len(t, srcs, 1)
	assert.Equal(t, "/etc/ctxloom/allowed_signers", srcs[0].Path)
	assert.False(t, srcs[0].Loaded)
	require.Error(t, srcs[0].Err)

	require.Len(t, failed.LoadErrors(), 1)
}

// An EMPTY store that loaded fine is a different fact from one that failed,
// and Len() must not be the only distinguisher.
func TestStore_EmptyLoadedSource_IsNotAFailure(t *testing.T) {
	s, _, err := Parse(strings.NewReader(""))
	require.NoError(t, err)
	loaded := s.WithSource("/home/u/.ctxloom/allowed_signers")

	assert.Zero(t, loaded.Len())
	require.Len(t, loaded.Sources(), 1)
	assert.True(t, loaded.Sources()[0].Loaded)
	assert.Empty(t, loaded.LoadErrors())
}

// Union must PRESERVE a failed source rather than erase it. Skipping a nil
// argument was the erasure: a caller that returned nil on EACCES produced a
// union indistinguishable from one where that location simply held no keys.
func TestStore_Union_PreservesFailedSources(t *testing.T) {
	pub := mustParseKey(t, testEd25519Key)
	good := NewStore(Entry{Principals: []string{"a@example.com"}, PublicKey: pub}).WithSource("/good")
	bad := FailedSource("/bad", errors.New("permission denied"))

	u := Union(good, bad)
	assert.Equal(t, 1, u.Len())
	require.Len(t, u.LoadErrors(), 1, "a failed location must survive the union")
	assert.Equal(t, "/bad", u.LoadErrors()[0].Path)
	assert.Len(t, u.Sources(), 2)
}

// Len() on a nil store is zero, and asking a nil store for provenance is not
// a panic — nil is still a legal "no store here" for existing callers.
func TestStore_NilProvenance(t *testing.T) {
	var s *Store
	assert.Zero(t, s.Len())
	assert.Empty(t, s.Sources())
	assert.Empty(t, s.LoadErrors())
}

// --- the trust root must not be writable through anything it hands out ------

// TestStore_HandsOutNoWritablePathIntoItsOwnEntries pins the Store as
// immutable from the outside.
//
// Three routes used to lead straight back into s.entries and its inner
// slices, and each is a way to WIDEN a grant after the store was built:
//
//   - Decision.Entry was literally &s.entries[i]. Setting .Namespaces = nil on
//     it means "accepted for all namespaces" (see Entry.Namespaces), so a
//     caller holding a decision for one namespace could promote the entry to
//     every namespace — including reject, whose supremacy the whole design
//     rests on.
//   - Entries() copied the slice but not the slices INSIDE each Entry, so
//     Entries()[0].Namespaces[0] = "*" rewrote the live trust root.
//   - NewStore and Union took the caller's/other stores' inner slices by
//     reference, so the builder retained a handle on what it had built.
//
// Nothing in production mutates these today — that is exactly why the aliasing
// survived. The pin is what keeps it that way.
func TestStore_HandsOutNoWritablePathIntoItsOwnEntries(t *testing.T) {
	key := mustParseKey(t, testEd25519Key)
	newStore := func() *Store {
		return NewStore(Entry{
			Principals: []string{"releases@ctxloom.dev"},
			Namespaces: []string{"publish.v1.ctxloom.dev"},
			PublicKey:  key,
		})
	}

	t.Run("through Decision.Entry", func(t *testing.T) {
		store := newStore()
		d := store.TrustedForNamespace(key, "publish.v1.ctxloom.dev", fixedNow())
		require.True(t, d.Trusted)
		require.NotNil(t, d.Entry)

		d.Entry.Namespaces = nil // "accepted for all namespaces"

		assert.False(t, store.TrustedForNamespace(key, "reject.v1.ctxloom.dev", fixedNow()).Trusted,
			"a decision handed back a writable pointer into the store's entries")
	})

	t.Run("through Entries", func(t *testing.T) {
		store := newStore()
		got := store.Entries()
		require.Len(t, got, 1)

		got[0].Namespaces[0] = "*"
		got[0].Principals[0] = "attacker@example.com"

		assert.False(t, store.TrustedForNamespace(key, "reject.v1.ctxloom.dev", fixedNow()).Trusted,
			"Entries() shared the inner namespaces slice with the store")
		assert.Equal(t, "releases@ctxloom.dev",
			store.TrustedForNamespace(key, "publish.v1.ctxloom.dev", fixedNow()).Principal,
			"Entries() shared the inner principals slice with the store")
	})

	t.Run("through the entries handed to NewStore", func(t *testing.T) {
		ns := []string{"publish.v1.ctxloom.dev"}
		store := NewStore(Entry{
			Principals: []string{"releases@ctxloom.dev"},
			Namespaces: ns,
			PublicKey:  key,
		})

		ns[0] = "*"

		assert.False(t, store.TrustedForNamespace(key, "reject.v1.ctxloom.dev", fixedNow()).Trusted,
			"NewStore retained the caller's namespaces slice")
	})

	t.Run("through a store that was unioned", func(t *testing.T) {
		src := newStore()
		union := Union(src)

		src.entries[0].Namespaces[0] = "*"

		assert.False(t, union.TrustedForNamespace(key, "reject.v1.ctxloom.dev", fixedNow()).Trusted,
			"Union shared the contributing store's inner slices")
	})
}
