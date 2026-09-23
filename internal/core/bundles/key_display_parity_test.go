package bundles

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/remote"
)

// A read's Key() is the identity config seeds a bundle's profiles under, and a
// profile ref a human types reaches the same identity through the reference
// grammar (remote.CanonicalBundleRef over the bundle part). The two must agree
// for every reader class, or a seeded profile is keyed where no lookup goes.
func TestBundleRead_KeyEqualsCanonicalBundleRefOfDisplayName(t *testing.T) {
	remoteRefs := []string{
		"https://example.test/repo@bundles/kit",
		"ctxloom+git://example.test/repo//bundles/kit",
		"git@example.test:repo@bundles/kit",
	}
	for _, ref := range remoteRefs {
		t.Run(ref, func(t *testing.T) {
			tree := repoTree(t, "kit", readerTreeEnvelope, readerTreeFragments, nil)
			reads, err := NewRepoFSReader(tree, ref, WithRepoURL(repoTreeURL)).Read(context.Background())
			require.NoError(t, err)
			require.Len(t, reads, 1)
			assertKeyMatchesGrammar(t, reads[0])
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

func assertKeyMatchesGrammar(t *testing.T, read BundleRead) {
	t.Helper()
	want, err := remote.CanonicalBundleRef(read.DisplayName())
	require.NoError(t, err, "display name %q", read.DisplayName())
	require.NotEmpty(t, read.Key(), "display name %q minted no key", read.DisplayName())
	assert.Equal(t, want, string(read.Key()), "display name %q", read.DisplayName())
}
