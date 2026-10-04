package runner_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/runner"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/launch/launchtest"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/engines/mock"
)

const (
	tokenVar      = "CLAUDE_CODE_OAUTH_TOKEN"
	mountedSecret = "sk-ant-oat01-from-the-mounted-file"
)

// secretFileLaunch is a container child's launch as it arrives: the token
// NOT in the cell's env, only the engine-side file that holds it.
func secretFileLaunch(t *testing.T, file string) (launch.Launch, *deliveryEnv) {
	t.Helper()
	l, env := tokenChildLaunch(t)
	delete(l.Cell.Env, tokenVar)
	l.Cell.SecretFiles = map[string]string{tokenVar: file}
	return l, env
}

func executeWith(t *testing.T, l launch.Launch, env *deliveryEnv, drive runner.Driver) error {
	t.Helper()
	_, err := runner.Execute(context.Background(), runner.Deps{
		Locks: &launchtest.Locks{},
		Kind:  mock.New(), Inline: env.deps.Inline, ClaimCheck: env.deps.ClaimCheck,
		Static: staticWriter(t), Records: records(t), Driver: drive, Unsetenv: os.Unsetenv,
	}, l)
	return err
}

// The runner redeems each secret file the cell names into the ENGINE's env:
// the exact bytes the originator wrote, for the engine's process alone —
// never into the runner's own env, which everything it spawns would inherit.
func TestExecute_ASecretFileReachesTheEngineEnvOnly(t *testing.T) {
	t.Setenv(tokenVar, "")
	require.NoError(t, os.Unsetenv(tokenVar))
	file := filepath.Join(t.TempDir(), "run.env")
	b, err := sessions.EncodeSecrets(map[string]string{tokenVar: mountedSecret, "OTHER": "not this one"})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(file, b, 0o600))
	l, env := secretFileLaunch(t, file)

	drive := &childEnvDriver{}
	require.NoError(t, executeWith(t, l, env, drive))
	got, ok := envHas(drive.env, tokenVar)
	require.True(t, ok, "the engine authenticates from the mounted file")
	assert.Equal(t, mountedSecret, got, "the exact value, out of the run's one secrets file")
	_, inRunner := os.LookupEnv(tokenVar)
	assert.False(t, inRunner, "the runner's own env never holds the secret")
}

// A secret the runner cannot read refuses the run before the engine is
// driven: an engine started without its credential would fail later and
// less legibly, or fall back to some other login.
func TestExecute_RefusesAnUnreadableSecretFile(t *testing.T) {
	l, env := secretFileLaunch(t, filepath.Join(t.TempDir(), "absent"))
	drive := &childEnvDriver{}
	err := executeWith(t, l, env, drive)
	require.ErrorIs(t, err, runner.ErrSecretUnreadable)
	assert.Contains(t, err.Error(), tokenVar, "the refusal names the variable")
	assert.Nil(t, drive.env, "the engine is never driven")
}

// A secrets file that does not hold the variable the cell names refuses the
// run the same way.
func TestExecute_RefusesASecretMissingFromTheFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "run.env")
	b, err := sessions.EncodeSecrets(map[string]string{"OTHER": "x"})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(file, b, 0o600))
	l, env := secretFileLaunch(t, file)
	err = executeWith(t, l, env, &childEnvDriver{})
	require.ErrorIs(t, err, runner.ErrSecretUnreadable)
	assert.Contains(t, err.Error(), tokenVar)
}
