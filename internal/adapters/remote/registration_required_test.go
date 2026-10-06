package remote

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// LookupURL answers by repository identity (SameRepository), so any spelling
// of a registered repository finds its remote, and it never registers one.
func TestRegistry_LookupURL(t *testing.T) {
	fs := afero.NewMemMapFs()
	reg, err := NewRegistry("/test/remotes.yaml", WithRegistryFS(fs))
	require.NoError(t, err)
	require.NoError(t, reg.Add("existing", "https://github.com/owner/repo"))

	got, ok := reg.LookupURL("git@github.com:owner/repo")
	require.True(t, ok, "another spelling of a registered repository is that remote")
	assert.Equal(t, "existing", got.Name)

	got.Name = "mutated"
	again, ok := reg.LookupURL("https://github.com/owner/repo")
	require.True(t, ok)
	assert.Equal(t, "existing", again.Name, "the returned remote is a copy")

	_, ok = reg.LookupURL("https://github.com/owner/other")
	assert.False(t, ok, "an unregistered repository is not found")
	_, ok = reg.LookupURL("https://h/o/a%2Fb/r")
	assert.False(t, ok, "a URL that names no repository matches nothing")
	assert.Len(t, reg.List(), 1, "a lookup registers nothing")
}

// A pull of an address no remote is registered for is refused: it names the
// fix, and the registry is left exactly as it was.
func TestPuller_Pull_UnregisteredAddressIsRefused(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, fs.MkdirAll("/test", 0o755))
	reg, err := NewRegistry("/test/remotes.yaml", WithRegistryFS(fs))
	require.NoError(t, err)
	require.NoError(t, reg.Add("alice", "https://github.com/alice/ctxloom"))

	mf := NewMockFetcher()
	mf.Refs["main"] = "abc123def456"
	lm := NewLockfileManager("/test", WithLockfileFS(fs))
	puller := NewPuller(reg, AuthConfig{}, WithTreeInstaller(stubTreeInstaller()),
		WithTreeVerifier(stubTreeVerifier()),
		WithLockfileManager(lm),
		WithFetcherFactory(mockFetcherFactory(mf)),
		WithTreeFetcher(treeAt(map[string]map[string]TreeFile{
			".ctxloom/content/bundles/v2/security": {
				BundleManifestName: {Data: []byte("description: Security bundle\n")},
			},
		}, nil)),
	)

	const addr = "https://github.com/mallory/ctxloom"
	_, err = puller.Pull(context.Background(), addr+"@bundles/security", PullOptions{
		LocalDir: "/test",
		ItemType: ItemTypeBundle,
		Stdout:   &bytes.Buffer{},
		Stdin:    strings.NewReader(""),
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRemoteNotRegistered)
	assert.Contains(t, err.Error(), addr, "the refusal names the address it refused")
	var rem report.Remediable
	require.True(t, errors.As(err, &rem), "the refusal carries its fix")
	assert.Equal(t, "ctxloom remote create <name> "+addr, rem.Remedy())

	assert.Len(t, reg.List(), 1, "nothing was registered")
	reloaded, err := NewRegistry("/test/remotes.yaml", WithRegistryFS(fs))
	require.NoError(t, err)
	assert.Len(t, reloaded.List(), 1, "nothing was persisted")
	lock, err := lm.Load()
	require.NoError(t, err)
	assert.True(t, lock.IsEmpty(), "nothing was pinned")
}
