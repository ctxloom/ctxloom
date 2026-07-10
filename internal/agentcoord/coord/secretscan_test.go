package coord

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSecretScan_TokenNeverOnDisk pins the "no credential ever touches a
// file" invariant (B1.6 deliverable 6): after a session-owner mint and a
// child spawn, NO file under the coordinator's state dir contains a raw
// bearer token — journals persist only SHA-256 hashes; the tokens ride
// process env seams exclusively.
func TestSecretScan_TokenNeverOnDisk(t *testing.T) {
	resetStrictness(t)
	stateDir := t.TempDir()
	c, err := New(Options{
		ProjectDir: t.TempDir(),
		StateDir:   stateDir,
		Spawner:    researcherSpawner(),
	})
	require.NoError(t, err)
	require.NoError(t, c.Serve())
	t.Cleanup(c.Close)

	ownerToken, err := c.RegisterSessionOwner(ownerIdentity().Harp)
	require.NoError(t, err)
	out := spawnResearcher(t, c)
	childToken := waitForChildEnv(t, c, out.RunID)[EnvCoordCred]
	require.NotEmpty(t, childToken)

	// File a report + queue mail so every journal has content.
	_, err = c.AgentSend(ownerIdentity(), out.Harp, "note", "remember this")
	require.NoError(t, err)

	secrets := []string{ownerToken, childToken}
	require.NoError(t, filepath.Walk(stateDir, func(path string, info os.FileInfo, werr error) error {
		if werr != nil || info.IsDir() {
			return werr
		}
		raw, rerr := os.ReadFile(path)
		require.NoError(t, rerr)
		for _, secret := range secrets {
			require.False(t, strings.Contains(string(raw), secret),
				"raw credential found in %s — only SHA-256 hashes may be persisted", path)
		}
		return nil
	}))

	// And the ENGINE env (what a harness could write into its own files)
	// carries no token either — the runner spawn env is the only carrier.
	env := c.spawner.(*fakeSpawner).engine(0).env()
	for k, v := range env {
		for _, secret := range secrets {
			require.NotEqual(t, secret, v, "engine env %s carries a raw credential", k)
		}
	}
}
