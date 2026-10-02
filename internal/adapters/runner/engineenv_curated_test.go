package runner_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/runner"
	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/launch/launchtest"
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

func driveUnder(t *testing.T, h agents.HostEnv, environ func() []string) (*childEnvDriver, error) {
	t.Helper()
	restoreEnviron(t)
	t.Setenv(unrelatedSecret, "leak")
	t.Setenv(passedThrough, "kept")
	l, env := tokenChildLaunch(t)
	l.Cell.HostEnv = h
	drive := &childEnvDriver{}
	_, err := runner.Execute(context.Background(), runner.Deps{
		Locks: &launchtest.Locks{},
		Kind:  mock.New(), Inline: env.deps.Inline, ClaimCheck: env.deps.ClaimCheck,
		Static: staticWriter(t), Records: records(t), Driver: drive, Unsetenv: os.Unsetenv, Environ: environ,
	}, l)
	return drive, err
}

// Curated: an unrelated exported variable never reaches the engine, a
// passthrough-listed one does, the base (PATH) does, and what the launch
// sets by value (the token) still arrives.
func TestExecute_ACuratedEngineInheritsOnlyTheBaseAndThePassthrough(t *testing.T) {
	drive, err := driveUnder(t, agents.HostEnv{Curated: true, Passthrough: []string{passedThrough}}, os.Environ)
	require.NoError(t, err)
	require.NotNil(t, drive.env, "the engine was driven")
	_, leaked := envHas(drive.env, unrelatedSecret)
	assert.False(t, leaked, "an unrelated exported variable must not reach the engine")
	v, _ := envHas(drive.env, passedThrough)
	assert.Equal(t, "kept", v, "a passthrough-listed variable reaches the engine")
	_, hasPath := envHas(drive.env, "PATH")
	assert.True(t, hasPath, "the curated base reaches the engine")
	tok, _ := envHas(drive.env, "CLAUDE_CODE_OAUTH_TOKEN")
	assert.Equal(t, "sk-ant-oat01-child", tok, "what the launch sets still reaches the engine")
}

// Undeclared: inheritance is unchanged — every exported variable reaches
// the engine.
func TestExecute_AnUncuratedEngineInheritsEverything(t *testing.T) {
	drive, err := driveUnder(t, agents.HostEnv{}, os.Environ)
	require.NoError(t, err)
	v, _ := envHas(drive.env, unrelatedSecret)
	assert.Equal(t, "leak", v)
}

// With no environment to read, a curated launch is refused rather than
// driven with everything inherited.
func TestExecute_ACuratedLaunchWithNoEnvironIsRefused(t *testing.T) {
	drive, err := driveUnder(t, agents.HostEnv{Curated: true}, nil)
	require.ErrorIs(t, err, runner.ErrEngineEnvUnscrubbed)
	assert.Nil(t, drive.env, "the engine is never driven")
}
