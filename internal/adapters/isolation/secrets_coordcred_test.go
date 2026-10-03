package isolation

import (
	"os"
	"path"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/sessions"
)

// A container runner's coordinator credential leaves its spawn env for the
// run's read-only secret dir: the `run` client and the container are handed
// only the NAME of the file, and the runner reads the exact bytes back.
func TestStageCoordCred_MovesTheCredentialIntoTheSecretDir(t *testing.T) {
	sec, err := newOwnedScratch(t.TempDir(), secretScratchPrefix)
	require.NoError(t, err)
	t.Cleanup(sec.release)
	cw := &containerWorkspace{secrets: sec}
	env := map[string]string{sessions.EnvCoordURL: "http://h:1/mcp", sessions.EnvCoordCred: "c0ffee", sessions.EnvRunID: "run-1"}

	got, err := stageCoordCred(cw, env)
	require.NoError(t, err)
	assert.NotContains(t, got, sessions.EnvCoordCred, "the credential is not in the runner's env")
	assert.Equal(t, path.Join(secretsTarget, sessions.EnvCoordCred), got[sessions.EnvCoordCredFile])
	assert.Equal(t, "http://h:1/mcp", got[sessions.EnvCoordURL])
	assert.Equal(t, "c0ffee", env[sessions.EnvCoordCred], "the caller's map is not mutated")

	b, err := os.ReadFile(filepath.Join(sec.dir, sessions.EnvCoordCred))
	require.NoError(t, err)
	assert.Equal(t, "c0ffee", string(b))
}

// No credential (a launch without reach-back) passes through untouched; a
// credential with no secret dir to hold it is refused, never left in the env.
func TestStageCoordCred_PassThroughAndRefusal(t *testing.T) {
	env := map[string]string{sessions.EnvRunID: "run-1"}
	got, err := stageCoordCred(&containerWorkspace{}, env)
	require.NoError(t, err)
	assert.Equal(t, env, got)

	_, err = stageCoordCred(&containerWorkspace{}, map[string]string{sessions.EnvCoordCred: "x"})
	assert.ErrorIs(t, err, errSecretUnstaged)
}
