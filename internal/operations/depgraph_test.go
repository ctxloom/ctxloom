package operations

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/profiles"
	"github.com/ctxloom/ctxloom/internal/remote"
)

// newTestWalker builds a depWalker whose remote reads are served by fetcher.
func newTestWalker(fetcher remote.Fetcher) *depWalker {
	return &depWalker{
		ctx:     context.Background(),
		factory: func(string, remote.AuthConfig) (remote.Fetcher, error) { return fetcher, nil },
		pins:    map[string]PinnedRef{},
		hashes:  map[string]map[string]struct{}{},
		visited: map[string]struct{}{},
	}
}

func TestDepWalker_RecordsAndConflicts(t *testing.T) {
	t.Run("local refs are not tracked as remote pins", func(t *testing.T) {
		w := newTestWalker(remote.NewMockFetcher())
		w.record("ctxloom:local@bundles/x", remote.ItemTypeBundle)
		pins, conflicts, _ := w.result()
		assert.Empty(t, pins)
		assert.Empty(t, conflicts)
	})

	t.Run("same identity at two hashes is a conflict", func(t *testing.T) {
		w := newTestWalker(remote.NewMockFetcher())
		w.record("https://github.com/o/r@bundles/demo@aaaaaaa", remote.ItemTypeBundle)
		w.record("https://github.com/o/r@bundles/demo@bbbbbbb", remote.ItemTypeBundle)
		pins, conflicts, _ := w.result()

		require.Len(t, conflicts, 1)
		assert.Equal(t, "https://github.com/o/r@bundles/demo", conflicts[0].Item)
		assert.Equal(t, []string{"aaaaaaa", "bbbbbbb"}, conflicts[0].Hashes)
		require.Len(t, pins, 1, "the conflicted item still appears once in the pin set")
	})

	t.Run("same identity at the same hash is not a conflict", func(t *testing.T) {
		w := newTestWalker(remote.NewMockFetcher())
		w.record("https://github.com/o/r@bundles/demo@aaaaaaa", remote.ItemTypeBundle)
		w.record("https://github.com/o/r@bundles/demo@aaaaaaa", remote.ItemTypeBundle)
		_, conflicts, _ := w.result()
		assert.Empty(t, conflicts)
	})
}

func TestDepWalker_WalksRemoteParentClosure(t *testing.T) {
	// Local profile P pins bundle X@h1 directly AND a remote parent A@hA; A's
	// content pins the SAME bundle X at a DIFFERENT hash h2 — a diamond conflict
	// that must surface through the parent walk.
	const (
		urlX = "https://github.com/x/repo"
		urlA = "https://github.com/a/repo"
	)
	fetcher := remote.NewMockFetcher().
		WithFile("ctxloom/profiles/a.yaml", []byte("bundles:\n  - "+urlX+"@bundles/x@h2222222\n"))

	w := newTestWalker(fetcher)
	root := &profiles.Profile{
		Name:    "local",
		Bundles: []string{urlX + "@bundles/x@h1111111"},
		Parents: []string{urlA + "@profiles/a@hAAAAAAA"},
	}
	w.walkProfile(root, remote.LocalSource, "")

	pins, conflicts, _ := w.result()

	// X (from both P and A) and A itself are pinned.
	identities := map[string]string{}
	for _, p := range pins {
		identities[p.Identity] = p.Hash
	}
	assert.Contains(t, identities, urlX+"@bundles/x")
	assert.Contains(t, identities, urlA+"@profiles/a")

	require.Len(t, conflicts, 1)
	assert.Equal(t, urlX+"@bundles/x", conflicts[0].Item)
	assert.Equal(t, []string{"h1111111", "h2222222"}, conflicts[0].Hashes)
}

func TestConflictError(t *testing.T) {
	assert.NoError(t, ConflictError(nil))
	err := ConflictError([]DependencyConflict{{Item: "https://github.com/o/r@bundles/demo", Hashes: []string{"aaaaaaa1", "bbbbbbb2"}}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bundles/demo")
	assert.Contains(t, err.Error(), "aaaaaaa") // short form
}
