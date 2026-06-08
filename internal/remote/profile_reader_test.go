package remote

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Canonical lockfile profile keys used across the profile-reader tests.
const (
	baseProfileKey   = "https://github.com/alice/ctxloom@profiles/base"
	nestedProfileKey = "https://github.com/alice/ctxloom@profiles/team/dev"
)

func profileReaderFixture(t *testing.T) (*ProfileReader, *MockFetcher, *Lockfile) {
	t.Helper()

	fetcher := NewMockFetcher().
		WithFile("ctxloom/profiles/base.yaml", []byte("description: Base profile\nbundles:\n  - demo\n")).
		WithFile("ctxloom/profiles/team/dev.yaml", []byte("description: Dev\n"))

	factory := func(_ string, _ AuthConfig) (Fetcher, error) {
		return fetcher, nil
	}

	lock := &Lockfile{
		Bundles: map[string]LockEntry{},
		Profiles: map[string]LockEntry{
			baseProfileKey:   {SHA: "abc123def", URL: "https://github.com/alice/ctxloom"},
			nestedProfileKey: {SHA: "ffff111", URL: "https://github.com/alice/ctxloom"},
		},
	}

	return NewProfileReader(factory, AuthConfig{}, lock), fetcher, lock
}

func TestProfileReader_ReadProfileBytes(t *testing.T) {
	t.Run("fetches at locked SHA", func(t *testing.T) {
		reader, fetcher, _ := profileReaderFixture(t)

		data, err := reader.ReadProfileBytes(context.Background(), baseProfileKey)
		require.NoError(t, err)
		assert.Equal(t, "description: Base profile\nbundles:\n  - demo\n", string(data))

		require.Len(t, fetcher.FetchFileCalls, 1)
		call := fetcher.FetchFileCalls[0]
		assert.Equal(t, "alice", call.Owner)
		assert.Equal(t, "ctxloom", call.Repo)
		assert.Equal(t, "ctxloom/profiles/base.yaml", call.Path)
		assert.Equal(t, "abc123def", call.Ref, "must fetch at locked SHA")
		assert.Empty(t, fetcher.ResolveRefCalls, "SHA from the lockfile is the source of truth")
	})

	t.Run("nested profile path", func(t *testing.T) {
		reader, fetcher, _ := profileReaderFixture(t)

		data, err := reader.ReadProfileBytes(context.Background(), nestedProfileKey)
		require.NoError(t, err)
		assert.Equal(t, "description: Dev\n", string(data))
		assert.Equal(t, "ctxloom/profiles/team/dev.yaml", fetcher.FetchFileCalls[0].Path)
	})

	t.Run("unknown profile is ErrProfileNotInLockfile", func(t *testing.T) {
		reader, _, _ := profileReaderFixture(t)
		_, err := reader.ReadProfileBytes(context.Background(), "https://github.com/alice/ctxloom@profiles/missing")
		assert.True(t, errors.Is(err, ErrProfileNotInLockfile))
	})
}

func TestProfileReader_Listing(t *testing.T) {
	reader, _, _ := profileReaderFixture(t)
	assert.Equal(t, []string{baseProfileKey, nestedProfileKey}, reader.ListProfileNames())
	assert.True(t, reader.HasProfile(baseProfileKey))
	assert.False(t, reader.HasProfile("https://github.com/alice/ctxloom@profiles/nope"))
	e, ok := reader.LockEntryFor(baseProfileKey)
	require.True(t, ok)
	assert.Equal(t, "abc123def", e.SHA)
}
