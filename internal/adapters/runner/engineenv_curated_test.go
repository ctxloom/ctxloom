package runner_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/runner"
	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/launch/launchtest"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/engines/mock"
)

const (
	unrelatedSecret = "HOSTENV_UNRELATED_SECRET"
	passedThrough   = "HOSTENV_PASSED_THROUGH"
)

// restoreEnviron re-sets every variable of this process through t.Setenv,
// so each is restored after a test whose curated drive removed everything
// outside the base.
func restoreEnviron(t *testing.T) {
	t.Helper()
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok && k != "" {
			t.Setenv(k, v)
		}
	}
}

func driveUnder(t *testing.T, h agents.EnvHost, environ func() []string) (*childEnvDriver, error) {
	t.Helper()
	restoreEnviron(t)
	t.Setenv(unrelatedSecret, "leak")
	t.Setenv(passedThrough, "kept")
	l, env := tokenChildLaunch(t)
	l.Cell.EnvHost = h
	drive := &childEnvDriver{}
	_, err := runner.Execute(context.Background(), runner.Deps{
		Locks: &launchtest.Locks{},
		Kind:  mock.New(), Inline: env.deps.Inline, ClaimCheck: env.deps.ClaimCheck,
		Static: staticWriter(t), Records: records(t), Driver: drive, Unsetenv: os.Unsetenv, Environ: environ,
	}, l)
	return drive, err
}

// Curated: an unrelated exported variable never reaches the engine, a
// env-listed one does, the base (PATH) does, and what the launch
// sets by value (the token) still arrives.
func TestExecute_ACuratedEngineInheritsOnlyTheBaseAndTheEnvNames(t *testing.T) {
	drive, err := driveUnder(t, agents.EnvHost{Curated: true, Env: []string{passedThrough}}, os.Environ)
	require.NoError(t, err)
	require.NotNil(t, drive.env, "the engine was driven")
	_, leaked := envHas(drive.env, unrelatedSecret)
	assert.False(t, leaked, "an unrelated exported variable must not reach the engine")
	v, _ := envHas(drive.env, passedThrough)
	assert.Equal(t, "kept", v, "a env-listed variable reaches the engine")
	_, hasPath := envHas(drive.env, "PATH")
	assert.True(t, hasPath, "the curated base reaches the engine")
	tok, _ := envHas(drive.env, "CLAUDE_CODE_OAUTH_TOKEN")
	assert.Equal(t, "sk-ant-oat01-child", tok, "what the launch sets still reaches the engine")
}

// Undeclared: inheritance is unchanged — every exported variable reaches
// the engine.
func TestExecute_AnUncuratedEngineInheritsEverything(t *testing.T) {
	drive, err := driveUnder(t, agents.EnvHost{}, os.Environ)
	require.NoError(t, err)
	v, _ := envHas(drive.env, unrelatedSecret)
	assert.Equal(t, "leak", v)
}

// With no environment to read, a curated launch is refused rather than
// driven with everything inherited.
func TestExecute_ACuratedLaunchWithNoEnvironIsRefused(t *testing.T) {
	drive, err := driveUnder(t, agents.EnvHost{Curated: true}, nil)
	require.ErrorIs(t, err, runner.ErrEngineEnvUnscrubbed)
	assert.Nil(t, drive.env, "the engine is never driven")
}

// A cell carrying both a curated EnvHost and a secret file — which no
// Environment prepares today (only the host curates, only a container
// mounts secrets), but the runner cannot tell which one sent the cell —
// still hands the engine the REDEEMED value: curation only removes from
// this process's env, and the secret never lives there; it is laid over in
// the engine's own env. Even an env entry naming the variable, with a
// stale value exported here, cannot shadow it.
func TestExecute_CurationNeverStripsOrShadowsARedeemedSecret(t *testing.T) {
	restoreEnviron(t)
	t.Setenv(unrelatedSecret, "leak")
	t.Setenv(tokenVar, "host-stale")
	file := filepath.Join(t.TempDir(), "run.env")
	b, err := sessions.EncodeSecrets(map[string]string{tokenVar: mountedSecret})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(file, b, 0o600))
	l, env := secretFileLaunch(t, file)
	l.Cell.EnvHost = agents.EnvHost{Curated: true, Env: []string{tokenVar}}

	drive := &childEnvDriver{}
	_, err = runner.Execute(context.Background(), runner.Deps{
		Locks: &launchtest.Locks{},
		Kind:  mock.New(), Inline: env.deps.Inline, ClaimCheck: env.deps.ClaimCheck,
		Static: staticWriter(t), Records: records(t), Driver: drive, Unsetenv: os.Unsetenv, Environ: os.Environ,
	}, l)
	require.NoError(t, err)
	got, _ := envHas(drive.env, tokenVar)
	assert.Equal(t, mountedSecret, got, "the redeemed secret wins over curation and the inherited value")
	_, leaked := envHas(drive.env, unrelatedSecret)
	assert.False(t, leaked, "curation still applies beside the secret")
}

// Env never re-exposes what the launch unsets: a variable the auth
// mode removes (Cell.Unset) stays removed even when the binding's
// env names it.
func TestExecute_EnvNeverReExposesAnUnsetVariable(t *testing.T) {
	restoreEnviron(t)
	t.Setenv(storageVar, "/home/human/.claude")
	l, env := tokenChildLaunch(t)
	l.Cell.EnvHost = agents.EnvHost{Curated: true, Env: []string{storageVar}}

	drive := &childEnvDriver{}
	_, err := runner.Execute(context.Background(), runner.Deps{
		Locks: &launchtest.Locks{},
		Kind:  mock.New(), Inline: env.deps.Inline, ClaimCheck: env.deps.ClaimCheck,
		Static: staticWriter(t), Records: records(t), Driver: drive, Unsetenv: os.Unsetenv, Environ: os.Environ,
	}, l)
	require.NoError(t, err)
	_, exposed := envHas(drive.env, storageVar)
	assert.False(t, exposed, "Unset beats env")
}
