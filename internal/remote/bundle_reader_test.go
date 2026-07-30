package remote

import (
	"context"
	"errors"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/errs"
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
		WithFile(".ctxloom/content/bundles/security.yaml", []byte("description: Security bundle\n")).
		WithFile(".ctxloom/content/bundles/nested/sub.yaml", []byte("description: Sub\n"))

	factory := func(_ string, _ AuthConfig) (Fetcher, error) {
		return fetcher, nil
	}

	lock := &Lockfile{
		Bundles: map[string]LockEntry{
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

	reader := NewBundleReader(registry, factory, AuthConfig{}, lock)
	return reader, fetcher, lock
}

// Canonical lockfile keys used across the bundle-reader tests.
const (
	secKey = "https://github.com/alice/ctxloom@bundles/security"
	subKey = "https://github.com/alice/ctxloom@bundles/nested/sub"
)

func TestBundleReader_ReadBundleBytes(t *testing.T) {
	t.Run("fetches at locked SHA", func(t *testing.T) {
		reader, fetcher, _ := readerFixture(t)

		data, err := reader.ReadBundleBytes(context.Background(), secKey)
		require.NoError(t, err)
		assert.Equal(t, "description: Security bundle\n", string(data))

		require.Len(t, fetcher.FetchFileCalls, 1)
		call := fetcher.FetchFileCalls[0]
		assert.Equal(t, "alice", call.Owner)
		assert.Equal(t, "ctxloom", call.Repo)
		assert.Equal(t, ".ctxloom/content/bundles/security.yaml", call.Path)
		assert.Equal(t, "abc123def", call.Ref, "must fetch at locked SHA, not default branch")

		// The reader must NOT re-resolve — the SHA from the lockfile is
		// the source of truth.
		assert.Empty(t, fetcher.ResolveRefCalls)
	})

	t.Run("nested bundle path", func(t *testing.T) {
		reader, fetcher, _ := readerFixture(t)

		data, err := reader.ReadBundleBytes(context.Background(), subKey)
		require.NoError(t, err)
		assert.Equal(t, "description: Sub\n", string(data))

		require.Len(t, fetcher.FetchFileCalls, 1)
		assert.Equal(t, ".ctxloom/content/bundles/nested/sub.yaml", fetcher.FetchFileCalls[0].Path)
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
		reader, fetcher, _ := readerFixture(t)
		fetcher.FetchFileErr = errors.New("boom")

		_, err := reader.ReadBundleBytes(context.Background(), secKey)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "boom")
	})

	// U093-F01: an empty entry.SHA must never reach the fetcher — the pin IS
	// the security control (EffectiveTrust gates on content read at a
	// specific commit), and a fetcher asked to read "" resolves the default
	// branch TIP instead, silently converting a pinned read into a latest
	// read. A hand-edited, truncated, or future-written lockfile is the
	// realistic path to this state; no production writer emits an empty SHA
	// today, but a pinned reader must refuse to read unpinned regardless of
	// how it got that way.
	t.Run("empty locked SHA is refused, never resolved as latest", func(t *testing.T) {
		reader, fetcher, lock := readerFixture(t)
		entry := lock.Bundles[secKey]
		entry.SHA = ""
		lock.Bundles[secKey] = entry

		_, err := reader.ReadBundleBytes(context.Background(), secKey)
		require.Error(t, err, "an empty pin must be refused, not silently resolved to the default branch")
		assert.Empty(t, fetcher.FetchFileCalls, "the fetcher must never be asked to read an empty ref")
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

	// U093-F08 read "empty loaded AND empty failures" as a silent success over
	// unread bundles. The real invariant is stronger and is what this pins:
	// every name the source admits to knowing is accounted for in exactly one
	// of the two maps. "Zero failures" can only ever mean "nothing was named",
	// never "something was named and skipped" — which is the shape the row was
	// worried about.
	t.Run("every listed name lands in exactly one map", func(t *testing.T) {
		reader, fetcher, lock := readerFixture(t)
		lock.Bundles["ghost/missing"] = LockEntry{SHA: "x"}
		fetcher.FetchFileErr = errors.New("clone unreadable")

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

// The signature is a plain sibling path in the SAME tree at the SAME pinned
// SHA — reading it is the identical FetchFile call with ".sig" appended. No new
// transport, no network, no second SHA.
func TestBundleReader_ReadBundleSignature(t *testing.T) {
	t.Run("fetches the sibling .sig at the locked SHA", func(t *testing.T) {
		reader, fetcher, _ := readerFixture(t)
		fetcher.WithFile(".ctxloom/content/bundles/security.yaml.sig", []byte("-----BEGIN SSH SIGNATURE-----\nblob\n"))

		data, err := reader.ReadBundleSignature(context.Background(), secKey)
		require.NoError(t, err)
		assert.Equal(t, "-----BEGIN SSH SIGNATURE-----\nblob\n", string(data))

		require.Len(t, fetcher.FetchFileCalls, 1)
		call := fetcher.FetchFileCalls[0]
		assert.Equal(t, ".ctxloom/content/bundles/security.yaml.sig", call.Path,
			"the signature is the bundle path + .sig, nothing else")
		assert.Equal(t, "abc123def", call.Ref,
			"the signature must be read at the SAME pinned SHA as the bytes it covers")
	})

	// A missing .sig is the "unsigned" signal, and it must be cleanly
	// distinguishable from a real failure — it is the common case today.
	t.Run("absent .sig returns a typed not-found", func(t *testing.T) {
		reader, _, _ := readerFixture(t)

		_, err := reader.ReadBundleSignature(context.Background(), secKey)
		require.Error(t, err)
		assert.ErrorIs(t, err, errs.ErrRemoteContentNotFound,
			"an unsigned bundle is signalled by a typed not-found, never a crash or an opaque error")
	})

	t.Run("unknown bundle is not in the lockfile", func(t *testing.T) {
		reader, _, _ := readerFixture(t)

		_, err := reader.ReadBundleSignature(context.Background(), "https://github.com/alice/ctxloom@bundles/nope")
		assert.ErrorIs(t, err, ErrBundleNotInLockfile)
	})
}
