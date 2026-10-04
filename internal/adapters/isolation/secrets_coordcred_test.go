package isolation

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/platform"
)

// newTestSecrets is a run's secrets file under a test dir.
func newTestSecrets(t *testing.T) *secretsFile {
	t.Helper()
	f, err := newSecretsFile(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = f.release() })
	return f
}

// readSecrets decodes the run's secrets file.
func readSecrets(t *testing.T, f *secretsFile) map[string]string {
	t.Helper()
	b, err := os.ReadFile(f.path())
	require.NoError(t, err)
	got, err := sessions.DecodeSecrets(b)
	require.NoError(t, err)
	return got
}

// A run has ONE secrets file: the engine's credentials and the coordinator
// credential accumulate in it, and each write leaves the whole file decodable.
func TestSecretsFile_AccumulatesIntoOneDotenvFile(t *testing.T) {
	f := newTestSecrets(t)
	require.NoError(t, f.put(map[string]string{"TOKEN": "sk-1"}))
	require.NoError(t, f.put(map[string]string{sessions.EnvCoordCred: "c0ffee"}))
	assert.Equal(t, map[string]string{"TOKEN": "sk-1", sessions.EnvCoordCred: "c0ffee"}, readSecrets(t, f))
	entries, err := os.ReadDir(f.scratch.dir)
	require.NoError(t, err)
	var files []string
	for _, e := range entries {
		if e.Name() != ownedScratchLockName {
			files = append(files, e.Name())
		}
	}
	assert.Equal(t, []string{secretsFileName}, files, "one file, not one per variable")
}

// A value the format cannot hold exactly is refused, and the file keeps what
// it held.
func TestSecretsFile_RefusesAValueItCannotRoundTrip(t *testing.T) {
	f := newTestSecrets(t)
	require.NoError(t, f.put(map[string]string{"TOKEN": "sk-1"}))
	err := f.put(map[string]string{"BAD": `ends in \`})
	require.ErrorIs(t, err, sessions.ErrSecretNotRoundTrippable)
	assert.Equal(t, map[string]string{"TOKEN": "sk-1"}, readSecrets(t, f))
}

// The coordinator credential leaves the runner's spawn env for the secrets
// file: only the file's name crosses, and the caller's map is not mutated.
func TestStageCoordCred_MovesTheCredentialIntoTheSecretsFile(t *testing.T) {
	f := newTestSecrets(t)
	env := map[string]string{sessions.EnvCoordURL: "http://h:1/mcp", sessions.EnvCoordCred: "c0ffee", sessions.EnvRunID: "run-1"}

	got, err := stageCoordCred(f, env, "/engine/side/run.env")
	require.NoError(t, err)
	assert.NotContains(t, got, sessions.EnvCoordCred, "the credential is not in the runner's env")
	assert.Equal(t, "/engine/side/run.env", got[sessions.EnvCoordCredFile])
	assert.Equal(t, "http://h:1/mcp", got[sessions.EnvCoordURL])
	assert.Equal(t, "c0ffee", env[sessions.EnvCoordCred], "the caller's map is not mutated")
	assert.Equal(t, "c0ffee", readSecrets(t, f)[sessions.EnvCoordCred])
}

// No credential (a launch without reach-back) passes through untouched; a
// credential with no secrets file to hold it is refused, never left in the env.
func TestStageCoordCred_PassThroughAndRefusal(t *testing.T) {
	env := map[string]string{sessions.EnvRunID: "run-1"}
	got, err := stageCoordCred(nil, env, "")
	require.NoError(t, err)
	assert.Equal(t, env, got)

	_, err = stageCoordCred(nil, map[string]string{sessions.EnvCoordCred: "x"}, "")
	assert.ErrorIs(t, err, errSecretUnstaged)
}

// A container cell's secret variables all name the one mounted file.
func TestContainerPlacement_SecretVarsNameTheOneFile(t *testing.T) {
	pl := containerPlacement(presentPathsFixture(), layout{creds: engine.Credentials{Env: map[string]string{"A": "1x", "B": "2y"}}})
	want := path.Join(secretsTarget, secretsFileName)
	assert.Equal(t, map[string]string{"A": want, "B": want}, pl.SecretFiles)
	assert.NotContains(t, pl.Env, "A")
}

// A HOST runner gets its credential the same way a container runner does:
// the runner process is started with the file's name and never the value,
// and the run's Cleanup removes the file.
func TestHostEnvironment_StartStagesTheCredentialIntoTheSecretsFile(t *testing.T) {
	withHostOS(t, tmpfsAt(t.TempDir()))
	var spawned map[string]string
	prev := startHostRunner
	startHostRunner = func(_ []string, env map[string]string) (*HostRunner, error) {
		spawned = env
		return nil, errStubRunner
	}
	t.Cleanup(func() { startHostRunner = prev })

	e, err := None{state: SessionState{Harp: harpA}}.environment(hostWorkspace{dir: t.TempDir()}, launch.Placement{}, nil, engine.Credentials{})
	require.NoError(t, err)
	_, _ = e.Start(context.Background(), RunnerRequest{Engine: "mock", Env: map[string]string{sessions.EnvCoordURL: "http://h:1/mcp", sessions.EnvCoordCred: "c0ffee", sessions.EnvRunID: "run-1"}})

	require.NotNil(t, spawned)
	assert.NotContains(t, spawned, sessions.EnvCoordCred, "the credential never rides a host runner's exec env")
	file := spawned[sessions.EnvCoordCredFile]
	require.NotEmpty(t, file)
	b, err := os.ReadFile(file)
	require.NoError(t, err)
	got, err := sessions.DecodeSecrets(b)
	require.NoError(t, err)
	assert.Equal(t, "c0ffee", got[sessions.EnvCoordCred])

	require.NoError(t, e.Cleanup())
	_, err = os.Stat(file)
	assert.ErrorIs(t, err, fs.ErrNotExist, "the run's cleanup removes its secrets")
}

// The host runner refuses an exec env that still carries the credential, so a
// spawn path that skipped staging fails loudly instead of leaking it.
func TestNone_StartRunnerRefusesACredentialInTheExecEnv(t *testing.T) {
	_, err := None{}.startRunner(context.Background(), "mock", "", 0, nil, map[string]string{sessions.EnvCoordCred: "x"})
	assert.ErrorIs(t, err, errCredInExecEnv)
	_, _, err = None{}.interactiveRunner(context.Background(), "mock", nil, map[string]string{sessions.EnvCoordCred: "x"})
	assert.ErrorIs(t, err, errCredInExecEnv)
}

// errStubRunner is a stubbed runner start's refusal: the test inspects what
// the start was handed and starts nothing.
var errStubRunner = errors.New("stub runner: not started")

// presentPathsFixture is a placement's paths with nothing presented.
func presentPathsFixture() present.Paths { return present.Paths{} }

// tmpfsHost is a host whose per-user tmpfs is dir.
type tmpfsHost struct {
	platform.Host
	dir string
}

func (h tmpfsHost) PrivateTmpfs(func(string) string) (string, bool) { return h.dir, true }

// tmpfsAt is the platform with its per-user tmpfs at dir.
func tmpfsAt(dir string) platform.Host { return tmpfsHost{Host: platform.Current(), dir: dir} }
