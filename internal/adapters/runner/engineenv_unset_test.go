package runner_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/runner"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/engines/mock"
)

// childEnvDriver builds, at drive time, the env the engine's child process
// is spawned with — through the real builder every hosted backend uses
// (agent.BaseBackend.BuildEnv: this process's env plus the exec's own).
type childEnvDriver struct{ env []string }

func (d *childEnvDriver) Drive(_ context.Context, t runner.Turn) error {
	d.env = (&agent.BaseBackend{}).BuildEnv(t.Exec.Env)
	return nil
}

func envHas(env []string, key string) (string, bool) {
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			return v, true
		}
	}
	return "", false
}

const storageVar = "CLAUDE_SECURESTORAGE_CONFIG_DIR"

// tokenChildLaunch resolves a launch and gives its cell what a token
// child's auth resolves to: the token set, the login's storage var unset.
func tokenChildLaunch(t *testing.T) (launch.Launch, *deliveryEnv) {
	t.Helper()
	env := newDeliveryEnv(t)
	l, err := launch.Resolve(context.Background(), env.deps, launch.Source{
		Identity: env.mint(t, 1, "run-u"), Agent: "x", Mode: engine.Structured, Prompt: "go", WorkDir: env.project,
	})
	require.NoError(t, err)
	l.Cell.Env["CLAUDE_CODE_OAUTH_TOKEN"] = "sk-ant-oat01-child"
	l.Cell.Unset = []string{storageVar}
	return l, env
}

// A token child launched from an env that carries the human's
// credential-storage var (a launch from inside a login run) must not
// inherit it: "" would be HOME/.claude, the human's own credential. The
// runner removes it before the drive, and the child env the real builder
// composes has no such var — while the token the launch sets still arrives.
func TestExecute_TheChildEnvHasNoVariableTheLaunchUnsets(t *testing.T) {
	t.Setenv(storageVar, "/human/.claude")
	l, env := tokenChildLaunch(t)
	drive := &childEnvDriver{}
	_, err := runner.Execute(context.Background(), runner.Deps{
		Kind: mock.New(), Inline: env.deps.Inline, ClaimCheck: env.deps.ClaimCheck,
		Static: staticWriter(t), Records: records(t), Driver: drive, Unsetenv: os.Unsetenv,
	}, l)
	require.NoError(t, err)
	require.NotNil(t, drive.env, "the engine was driven")
	_, has := envHas(drive.env, storageVar)
	assert.False(t, has, "the child env must not carry %s", storageVar)
	tok, _ := envHas(drive.env, "CLAUDE_CODE_OAUTH_TOKEN")
	assert.Equal(t, "sk-ant-oat01-child", tok, "what the launch sets still reaches the child")
}

// With nothing to remove the variables with, the runner refuses rather than
// drive an engine that would inherit one.
func TestExecute_RefusesWhenAnUnsetCannotBeApplied(t *testing.T) {
	t.Setenv(storageVar, "/human/.claude")
	l, env := tokenChildLaunch(t)
	drive := &childEnvDriver{}
	_, err := runner.Execute(context.Background(), runner.Deps{
		Kind: mock.New(), Inline: env.deps.Inline, ClaimCheck: env.deps.ClaimCheck,
		Static: staticWriter(t), Records: records(t), Driver: drive,
	}, l)
	require.ErrorIs(t, err, runner.ErrEngineEnvUnscrubbed)
	assert.Nil(t, drive.env, "the engine is never driven")
}
