package remote

import (
	"context"
	"errors"
	"fmt"
	"github.com/ctxloom/ctxloom/internal/core/trust"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Canonical lockfile keys used across the bundle-reader tests, and the
// format-v2 tree root each resolves to. Format v2 holds only TREES, so a
// bundle's bytes are its manifest INSIDE that tree — every ReadBundleBytes
// call in this file goes through a treeReaderSpy, never a raw single-file
// fetch, because remote.BundleReader has no other read surface left.
const (
	secKey      = "https://github.com/alice/ctxloom@bundles/security"
	subKey      = "https://github.com/alice/ctxloom@bundles/nested/sub"
	secTreeRoot = ".ctxloom/content/bundles/v2/security"
	subTreeRoot = ".ctxloom/content/bundles/v2/nested/sub"
)

// treeReaderSpyCall records one call the reader made to its tree fetcher.
type treeReaderSpyCall struct {
	owner, repo, root, sha, repoURL string
}

// treeReaderSpy is a TreeFetchFunc that serves canned trees by ROOT and
// records every call, so a test can assert exactly where and at what SHA the
// reader looked — the same property MockFetcher.FetchFileCalls used to give
// the single-file path, which format v2 no longer has.
type treeReaderSpy struct {
	trees map[string]map[string]TreeFile
	err   error
	calls []treeReaderSpyCall
}

func newTreeReaderSpy(trees map[string]map[string]TreeFile) *treeReaderSpy {
	return &treeReaderSpy{trees: trees}
}

func (s *treeReaderSpy) fetch(_ context.Context, _ Fetcher, owner, repo, root, sha, repoURL string) (map[string]TreeFile, error) {
	s.calls = append(s.calls, treeReaderSpyCall{owner, repo, root, sha, repoURL})
	if s.err != nil {
		return nil, s.err
	}
	tree, ok := s.trees[root]
	if !ok {
		return nil, fmt.Errorf("no tree at %s", root)
	}
	return tree, nil
}

// readerFixture builds a bare BundleReader wired to a treeReaderSpy so we
// can assert exactly which calls the reader makes. Returns the reader, the
// spy (for call assertions and error injection), and the lockfile (for
// SHA-mutation tests).
func readerFixture(t *testing.T) (*BundleReader, *treeReaderSpy, *Lockfile) {
	t.Helper()

	fs := afero.NewMemMapFs()
	registry, err := NewRegistry("", WithRegistryFS(fs))
	require.NoError(t, err)
	require.NoError(t, registry.Add("alice", "https://github.com/alice/ctxloom"))

	factory := func(_ string, _ AuthConfig) (Fetcher, error) {
		return NewMockFetcher(), nil
	}

	lock := &Lockfile{
		Bundles: map[trust.BundleKey]LockEntry{
			secKey: {
				SHA: "abc123def",
				URL: "https://github.com/alice/ctxloom",
			},
			subKey: {
				SHA: "ffff111",
				URL: "https://github.com/alice/ctxloom",
			},
		},
	}

	spy := newTreeReaderSpy(map[string]map[string]TreeFile{
		secTreeRoot: {BundleManifestName: {Data: []byte("description: Security bundle\n")}},
		subTreeRoot: {BundleManifestName: {Data: []byte("description: Sub\n")}},
	})

	reader := NewBundleReader(registry, factory, AuthConfig{}, lock, WithReaderTreeFetcher(spy.fetch))
	return reader, spy, lock
}

func TestBundleReader_ReadBundleBytes(t *testing.T) {
	t.Run("fetches at locked SHA", func(t *testing.T) {
		reader, spy, _ := readerFixture(t)

		data, err := reader.ReadBundleBytes(context.Background(), secKey)
		require.NoError(t, err)
		assert.Equal(t, "description: Security bundle\n", string(data))

		require.Len(t, spy.calls, 1)
		call := spy.calls[0]
		assert.Equal(t, "alice", call.owner)
		assert.Equal(t, "ctxloom", call.repo)
		assert.Equal(t, secTreeRoot, call.root)
		assert.Equal(t, "abc123def", call.sha, "must fetch at locked SHA, not default branch")
	})

	t.Run("nested bundle path", func(t *testing.T) {
		reader, spy, _ := readerFixture(t)

		data, err := reader.ReadBundleBytes(context.Background(), subKey)
		require.NoError(t, err)
		assert.Equal(t, "description: Sub\n", string(data))

		require.Len(t, spy.calls, 1)
		assert.Equal(t, subTreeRoot, spy.calls[0].root)
	})

	t.Run("missing bundle returns ErrBundleNotInLockfile", func(t *testing.T) {
		reader, _, _ := readerFixture(t)

		_, err := reader.ReadBundleBytes(context.Background(), "https://github.com/alice/ctxloom@bundles/missing")
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrBundleNotInLockfile)
	})

	t.Run("non-canonical key returns error", func(t *testing.T) {
		reader, _, _ := readerFixture(t)
		reader.lock.Bundles["badkey"] = LockEntry{SHA: "x"}
		_, err := reader.ReadBundleBytes(context.Background(), "badkey")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "canonical")
	})

	t.Run("propagates fetcher errors", func(t *testing.T) {
		reader, spy, _ := readerFixture(t)
		spy.err = errors.New("boom")

		_, err := reader.ReadBundleBytes(context.Background(), secKey)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "boom")
	})

	// An empty entry.SHA must never reach the tree fetcher — the pin IS
	// the security control (EffectiveTrust gates on content read at a
	// specific commit), and a fetcher asked to read "" resolves the default
	// branch TIP instead, silently converting a pinned read into a latest
	// read. A hand-edited, truncated, or future-written lockfile is the
	// realistic path to this state; no production writer emits an empty SHA
	// today, but a pinned reader must refuse to read unpinned regardless of
	// how it got that way.
	t.Run("empty locked SHA is refused, never resolved as latest", func(t *testing.T) {
		reader, spy, lock := readerFixture(t)
		entry := lock.Bundles[secKey]
		entry.SHA = ""
		lock.Bundles[secKey] = entry

		_, err := reader.ReadBundleBytes(context.Background(), secKey)
		require.Error(t, err, "an empty pin must be refused, not silently resolved to the default branch")
		assert.Empty(t, spy.calls, "the tree fetcher must never be asked to read an empty ref")
	})
}

func TestBundleReader_Surface(t *testing.T) {
	t.Run("ListBundleNames is sorted and contains every lockfile key", func(t *testing.T) {
		reader, _, _ := readerFixture(t)
		names := reader.ListBundleNames()
		assert.Equal(t, []string{subKey, secKey}, names)
	})

	t.Run("HasBundle matches lockfile keys", func(t *testing.T) {
		reader, _, _ := readerFixture(t)
		assert.True(t, reader.HasBundle(secKey))
		assert.False(t, reader.HasBundle("https://github.com/alice/ctxloom@bundles/missing"))
	})

	t.Run("LockEntryFor returns the entry and false for unknown", func(t *testing.T) {
		reader, _, _ := readerFixture(t)
		entry, ok := reader.LockEntryFor(secKey)
		require.True(t, ok)
		assert.Equal(t, "abc123def", entry.SHA)

		_, ok = reader.LockEntryFor("nope")
		assert.False(t, ok)
	})

	t.Run("nil receiver is safe", func(t *testing.T) {
		var nilReader *BundleReader
		assert.Empty(t, nilReader.ListBundleNames())
		assert.False(t, nilReader.HasBundle("anything"))
		_, ok := nilReader.LockEntryFor("anything")
		assert.False(t, ok)

		_, err := nilReader.ReadBundleBytes(context.Background(), "x/y")
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrBundleNotInLockfile)
	})

	t.Run("nil lockfile is safe", func(t *testing.T) {
		reader := NewBundleReader(nil, nil, AuthConfig{}, nil)
		assert.Empty(t, reader.ListBundleNames())
		assert.False(t, reader.HasBundle("x/y"))
		_, ok := reader.LockEntryFor("x/y")
		assert.False(t, ok)

		_, err := reader.ReadBundleBytes(context.Background(), "x/y")
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrBundleNotInLockfile)
	})
}

func TestLoadAllBytes(t *testing.T) {
	t.Run("returns every bundle and empty failures on success", func(t *testing.T) {
		reader, _, _ := readerFixture(t)
		loaded, failures := LoadAllBytes(context.Background(), reader)
		assert.Len(t, loaded, 2)
		assert.Empty(t, failures)
	})

	t.Run("per-bundle failures are reported separately, others still load", func(t *testing.T) {
		reader, _, lock := readerFixture(t)
		lock.Bundles["ghost/missing"] = LockEntry{SHA: "x"} // bad: no registry, no URL

		loaded, failures := LoadAllBytes(context.Background(), reader)
		assert.Len(t, loaded, 2, "the two valid bundles still load")
		assert.Len(t, failures, 1, "the bad bundle is in failures")
		assert.Contains(t, failures, "ghost/missing")
	})

	t.Run("nil source returns empty maps", func(t *testing.T) {
		loaded, failures := LoadAllBytes(context.Background(), nil)
		assert.Empty(t, loaded)
		assert.Empty(t, failures)
	})

	// A prior version of LoadAllBytes read "empty loaded AND empty failures" as
	// a silent success over unread bundles. The real invariant is stronger and
	// is what this pins:
	// every name the source admits to knowing is accounted for in exactly one
	// of the two maps. "Zero failures" can only ever mean "nothing was named",
	// never "something was named and skipped" — which is the shape the row was
	// worried about.
	t.Run("every listed name lands in exactly one map", func(t *testing.T) {
		reader, spy, lock := readerFixture(t)
		lock.Bundles["ghost/missing"] = LockEntry{SHA: "x"}
		spy.err = errors.New("clone unreadable")

		names := reader.ListBundleNames()
		loaded, failures := LoadAllBytes(context.Background(), reader)

		require.NotEmpty(t, names)
		assert.Len(t, failures, len(names), "a source that names bundles and reads none reports every one as a failure")
		assert.Empty(t, loaded)
		for _, n := range names {
			_, ok := failures[n]
			assert.True(t, ok, "name %q must be accounted for", n)
		}
	})
}

// --- detached publisher signatures (spec §4.1) --------------------------------
