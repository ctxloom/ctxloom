package bundles

import (
	"context"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/core/trust"
)

// A read's Key() is the identity config seeds a bundle's profiles under, and a
// profile ref a human types reaches the same identity through the reference
// grammar (remote.CanonicalBundleRef over the bundle part). The two must agree
// for every reader class, or a seeded profile is keyed where no lookup goes.
//
// The same identity is also what a pull writes the lockfile entry under
// (Reference.LockKey) and what the trust gate looks a retraction up by, so
// every spelling below is held to all four agreeing. The spellings are the
// ones that have split them: a path needing escaping (a space, a literal
// '%'), host case, a trailing '/', http://, userinfo and scp. A publisher's
// retraction keyed one way and looked up another is a retraction silently
// not enforced.
func TestBundleRead_KeyEqualsCanonicalBundleRefOfDisplayName(t *testing.T) {
	remoteRefs := []string{
		"https://example.test/repo@bundles/kit",
		"ctxloom+git://example.test/repo//bundles/kit",
		"git@example.test:repo@bundles/kit",
		"https://Example.TEST/repo@bundles/kit",
		"https://example.test/repo/@bundles/kit",
		"http://example.test/repo@bundles/kit",
		"https://alice@example.test/repo@bundles/kit",
		"file:///srv/has%20space/repo@bundles/kit",
		"file:///srv/pct%2541dir/repo@bundles/kit",
		"ctxloom+file:///srv/has%20space/pct%2541dir//bundles/kit",
	}
	for _, ref := range remoteRefs {
		t.Run(ref, func(t *testing.T) {
			tree := repoTree(t, "kit", readerTreeEnvelope, readerTreeFragments, nil)
			reads, err := NewRepoFSReader(tree, ref, WithRepoURL(repoTreeURL)).Read(context.Background())
			require.NoError(t, err)
			require.Len(t, reads, 1)
			assertKeyMatchesGrammar(t, reads[0])
			assertLockKeyMatchesKey(t, reads[0])
		})
	}

	t.Run("companion", func(t *testing.T) {
		reads, err := NewCompanionReader(
			loadoutProbe(CompanionLoadout{Bin: "ltk", Document: readerLoadoutDoc}),
		).Read(context.Background())
		require.NoError(t, err)
		require.Len(t, reads, 1)
		assertKeyMatchesGrammar(t, reads[0])
	})
}

// Every spelling of ONE git repository is one identity: the transport, host
// case, a trailing slash and credentials are not part of which repository it
// is. If any of these keyed apart, a retraction recorded under one spelling
// would not withhold content pulled under another.
func TestLockKey_SpellingsOfOneRepositoryAreOneKey(t *testing.T) {
	spellings := []string{
		"https://example.test/repo@bundles/kit",
		"ctxloom+git://example.test/repo//bundles/kit",
		"git@example.test:repo@bundles/kit",
		"https://Example.TEST/repo@bundles/kit",
		"https://example.test/repo/@bundles/kit",
		"http://example.test/repo@bundles/kit",
		"https://alice@example.test/repo@bundles/kit",
	}
	want := lockKeyOf(t, spellings[0])
	for _, s := range spellings[1:] {
		assert.Equal(t, want, lockKeyOf(t, s), "spelling %q", s)
	}
}

func lockKeyOf(t *testing.T, ref string) string {
	t.Helper()
	parsed, err := remote.ParseReference(ref)
	require.NoError(t, err, "ref %q", ref)
	return parsed.LockKey()
}

func assertKeyMatchesGrammar(t *testing.T, read BundleRead) {
	t.Helper()
	want, err := remote.CanonicalBundleRef(read.DisplayName())
	require.NoError(t, err, "display name %q", read.DisplayName())
	require.NotEmpty(t, read.Key(), "display name %q minted no key", read.DisplayName())
	assert.Equal(t, want, string(read.Key()), "display name %q", read.DisplayName())
}

// assertLockKeyMatchesKey holds the lockfile key a pull of read's display name
// writes to read's own Key, and then holds the retraction lookup to it: a
// retraction recorded under that lockfile key must be found by the gate for
// an item of read, exactly as bundles.Decide asks.
func assertLockKeyMatchesKey(t *testing.T, read BundleRead) {
	t.Helper()
	parsed, err := remote.ParseReference(read.DisplayName())
	require.NoError(t, err)
	lockKey := parsed.LockKey()
	assert.Equal(t, string(read.Key()), lockKey, "lockfile key of %q", read.DisplayName())

	lm := remote.NewLockfileManager("/lk", remote.WithLockfileFS(afero.NewMemMapFs()))
	lock, err := lm.Load()
	require.NoError(t, err)
	lock.AddEntry(remote.ItemTypeBundle, lockKey, remote.LockEntry{SHA: "abc123", Retracted: true, RetractedReason: "withdrawn"})
	require.NoError(t, lm.Save(lock))

	itemRef, err := ItemRefFor(read.SourceRef(), trust.KindMCP, "server")
	require.NoError(t, err)
	br, err := trust.ParseBundleRef(itemRef)
	require.NoError(t, err)
	retracted, _ := remote.NewLockfileRetraction(lm).Retracted(trust.RefFromBundleRef(br))
	assert.True(t, retracted, "a retraction recorded under %q is enforced for %q", lockKey, itemRef)
}
