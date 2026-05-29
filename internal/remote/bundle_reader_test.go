package remote

import (
	"context"
	"errors"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// readerFixture builds a bare BundleReader wired to a MockFetcher so we
// can assert exactly which calls the reader makes. Returns the reader,
// the underlying fetcher (for call assertions), and the lockfile (for
// SHA-mutation tests).
func readerFixture(t *testing.T) (*BundleReader, *MockFetcher, *Lockfile) {
	t.Helper()

	fs := afero.NewMemMapFs()
	registry, err := NewRegistry("", WithRegistryFS(fs))
	require.NoError(t, err)
	require.NoError(t, registry.Add("alice", "https://github.com/alice/ctxloom"))

	fetcher := NewMockFetcher().
		WithFile("ctxloom/bundles/security.yaml", []byte("description: Security bundle\n")).
		WithFile("ctxloom/bundles/nested/sub.yaml", []byte("description: Sub\n"))

	factory := func(_ string, _ AuthConfig) (Fetcher, error) {
		return fetcher, nil
	}

	lock := &Lockfile{
		Bundles: map[string]LockEntry{
			"alice/security": {
				SHA:            "abc123def",
				URL:            "https://github.com/alice/ctxloom",
			},
			"alice/nested/sub": {
				SHA:            "ffff111",
				URL:            "https://github.com/alice/ctxloom",
			},
		},
		Profiles: map[string]LockEntry{},
	}

	reader := NewBundleReader(registry, factory, AuthConfig{}, lock)
	return reader, fetcher, lock
}

func TestBundleReader_ReadBundleBytes(t *testing.T) {
	t.Run("fetches at locked SHA", func(t *testing.T) {
		reader, fetcher, _ := readerFixture(t)

		data, err := reader.ReadBundleBytes(context.Background(), "alice/security")
		require.NoError(t, err)
		assert.Equal(t, "description: Security bundle\n", string(data))

		require.Len(t, fetcher.FetchFileCalls, 1)
		call := fetcher.FetchFileCalls[0]
		assert.Equal(t, "alice", call.Owner)
		assert.Equal(t, "ctxloom", call.Repo)
		assert.Equal(t, "ctxloom/bundles/security.yaml", call.Path)
		assert.Equal(t, "abc123def", call.Ref, "must fetch at locked SHA, not default branch")

		// The reader must NOT re-resolve — the SHA from the lockfile is
		// the source of truth.
		assert.Empty(t, fetcher.ResolveRefCalls)
	})

	t.Run("nested bundle path", func(t *testing.T) {
		reader, fetcher, _ := readerFixture(t)

		data, err := reader.ReadBundleBytes(context.Background(), "alice/nested/sub")
		require.NoError(t, err)
		assert.Equal(t, "description: Sub\n", string(data))

		require.Len(t, fetcher.FetchFileCalls, 1)
		assert.Equal(t, "ctxloom/bundles/nested/sub.yaml", fetcher.FetchFileCalls[0].Path)
	})

	t.Run("missing bundle returns ErrBundleNotInLockfile", func(t *testing.T) {
		reader, _, _ := readerFixture(t)

		_, err := reader.ReadBundleBytes(context.Background(), "alice/missing")
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrBundleNotInLockfile)
	})

	t.Run("invalid key without slash returns error", func(t *testing.T) {
		reader, _, _ := readerFixture(t)
		reader.lock.Bundles["badkey"] = LockEntry{SHA: "x"}
		_, err := reader.ReadBundleBytes(context.Background(), "badkey")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "expected remoteName/path")
	})

	t.Run("falls back to registry URL when lockfile entry has empty URL", func(t *testing.T) {
		reader, fetcher, _ := readerFixture(t)

		entry := reader.lock.Bundles["alice/security"]
		entry.URL = "" // simulate older lockfile
		reader.lock.Bundles["alice/security"] = entry

		_, err := reader.ReadBundleBytes(context.Background(), "alice/security")
		require.NoError(t, err)
		require.Len(t, fetcher.FetchFileCalls, 1)
		assert.Equal(t, "alice", fetcher.FetchFileCalls[0].Owner)
	})

	t.Run("errors when neither lockfile URL nor registry has it", func(t *testing.T) {
		reader, _, _ := readerFixture(t)
		require.NoError(t, reader.registry.Remove("alice"))
		entry := reader.lock.Bundles["alice/security"]
		entry.URL = ""
		reader.lock.Bundles["alice/security"] = entry

		_, err := reader.ReadBundleBytes(context.Background(), "alice/security")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not registered")
	})

	t.Run("propagates fetcher errors", func(t *testing.T) {
		reader, fetcher, _ := readerFixture(t)
		fetcher.FetchFileErr = errors.New("boom")

		_, err := reader.ReadBundleBytes(context.Background(), "alice/security")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "boom")
	})
}

func TestBundleReader_Surface(t *testing.T) {
	t.Run("ListBundleNames is sorted and contains every lockfile key", func(t *testing.T) {
		reader, _, _ := readerFixture(t)
		names := reader.ListBundleNames()
		assert.Equal(t, []string{"alice/nested/sub", "alice/security"}, names)
	})

	t.Run("HasBundle matches lockfile keys", func(t *testing.T) {
		reader, _, _ := readerFixture(t)
		assert.True(t, reader.HasBundle("alice/security"))
		assert.False(t, reader.HasBundle("alice/missing"))
	})

	t.Run("LockEntryFor returns the entry and false for unknown", func(t *testing.T) {
		reader, _, _ := readerFixture(t)
		entry, ok := reader.LockEntryFor("alice/security")
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
}
