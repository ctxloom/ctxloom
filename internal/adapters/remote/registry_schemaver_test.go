package remote

import (
	"strconv"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/shared/schemaver"
	"github.com/ctxloom/ctxloom/resources"
)

const (
	remotesTestPath = "/proj/.ctxloom/remotes.yaml"
	remotesEntry    = "remotes:\n  kit:\n    url: https://example.test/kit\n"
)

func seedRemotes(t *testing.T, body string) afero.Fs {
	t.Helper()
	fs := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(fs, remotesTestPath, []byte(body), 0o644))
	return fs
}

// A remotes.yaml written before it was versioned (every one in the wild) and a
// current one both load, and constructing the registry writes nothing.
func TestRegistry_KeylessAndCurrentLoadAndReadingNeverWrites(t *testing.T) {
	for name, body := range map[string]string{
		"keyless": remotesEntry,
		"current": schemaver.Key + ": " + strconv.Itoa(remotesKind.Current()) + "\n" + remotesEntry,
	} {
		t.Run(name, func(t *testing.T) {
			fs := seedRemotes(t, body)
			reg, err := NewRegistry(remotesTestPath, WithRegistryFS(fs))
			require.NoError(t, err)
			assert.True(t, reg.Has("kit"))

			onDisk, err := afero.ReadFile(fs, remotesTestPath)
			require.NoError(t, err)
			assert.Equal(t, body, string(onDisk), "a read must not write")
		})
	}
}

func TestRegistry_NewerIsRefusedNamingBothNumbers(t *testing.T) {
	fs := seedRemotes(t, schemaver.Key+": "+strconv.Itoa(remotesKind.Current()+1)+"\n"+remotesEntry)
	_, err := NewRegistry(remotesTestPath, WithRegistryFS(fs))
	require.ErrorIs(t, err, schemaver.ErrNewer)
	var ve *schemaver.VersionError
	require.ErrorAs(t, err, &ve)
	assert.Equal(t, remotesKind.Current()+1, ve.Found)
	assert.Equal(t, remotesKind.Current(), ve.Current)
}

// save stamps the current generation and still carries every top-level key it
// does not manage.
func TestRegistry_SaveStampsAndKeepsUnknownKeys(t *testing.T) {
	fs := seedRemotes(t, "not_ours: kept\n"+remotesEntry)
	reg, err := NewRegistry(remotesTestPath, WithRegistryFS(fs))
	require.NoError(t, err)
	require.NoError(t, reg.Add("other", "https://example.test/other"))

	data, err := afero.ReadFile(fs, remotesTestPath)
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, yaml.Unmarshal(data, &doc))
	assert.Equal(t, remotesKind.Current(), doc[schemaver.Key])
	assert.Equal(t, "kept", doc["not_ours"])
}

// The scaffold `ctxloom init` writes is itself a remotes.yaml writer: it must
// already be current, or every new project starts one migration behind.
func TestRegistry_DefaultScaffoldIsCurrent(t *testing.T) {
	data, err := resources.GetDefaultRemotes()
	require.NoError(t, err)
	r, err := remotesKind.Upgrade(data)
	require.NoError(t, err)
	assert.Empty(t, r.Applied, "the embedded default remotes.yaml must declare %s %d", schemaver.Key, remotesKind.Current())
}
