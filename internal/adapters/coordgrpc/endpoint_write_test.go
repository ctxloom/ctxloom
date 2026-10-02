//go:build !windows

package coordgrpc

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/discover"
)

// A pre-existing endpoint.json left group/world-readable is owner-only after
// the next write: the file carries the consumer credential.
func TestWriteEndpoint_ALooserExistingFileEnds0600(t *testing.T) {
	path := filepath.Join(t.TempDir(), "endpoint.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"loopback_port":1}`), 0o600))
	require.NoError(t, os.Chmod(path, 0o644))

	require.NoError(t, writeEndpoint(afero.NewOsFs(), path, endpointState{LoopbackPort: 2, ConsumerCred: "c"}))

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.JSONEq(t, `{"loopback_port":2,"consumer_cred":"c"}`, string(raw))
}

var errInjected = errors.New("injected: the write died before it was installed")

// renameFails is an fs whose writer dies before the new file is installed.
type renameFails struct{ afero.Fs }

func (renameFails) Rename(string, string) error { return errInjected }

// A write that dies partway leaves the old endpoint whole: a reader never
// sees a truncated or half-written file.
func TestWriteEndpoint_ACrashMidWriteLeavesTheOldFileIntact(t *testing.T) {
	path := filepath.Join(t.TempDir(), "endpoint.json")
	old := []byte(`{"loopback_port":1,"consumer_cred":"old"}`)
	require.NoError(t, os.WriteFile(path, old, 0o600))

	err := writeEndpoint(renameFails{afero.NewOsFs()}, path, endpointState{LoopbackPort: 2, ConsumerCred: "new"})
	require.ErrorIs(t, err, errInjected)

	raw, rerr := os.ReadFile(path)
	require.NoError(t, rerr)
	assert.Equal(t, old, raw)
	leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".endpoint.json.*"))
	assert.Empty(t, leftovers, "no temp file is left beside it")
}

// The coordinator's own save goes through writeEndpoint: serving over a state
// dir whose endpoint.json was left 0644 leaves it owner-only.
func TestServe_TheSavedEndpointIsOwnerOnly(t *testing.T) {
	stateDir := t.TempDir()
	path := filepath.Join(stateDir, discover.FileName)
	require.NoError(t, os.WriteFile(path, []byte(`{}`), 0o600))
	require.NoError(t, os.Chmod(path, 0o644))

	c := servedCoordinator(t, stateDir)
	t.Cleanup(c.Close)

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}
