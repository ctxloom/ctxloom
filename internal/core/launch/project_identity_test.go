package launch_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/launch/launchtest"
)

// TestResolve_ProjectIdentity_EstablishedOnlyOnceAdmitted: establishing a
// project's identity writes a marker into the project, so a launch that is
// refused must never reach it — a refused `run` in an empty directory used to
// leave a .ctxloom behind that made the directory look like a half-made
// project. An admitted launch establishes it once, before the cell that
// carries it is prepared, and the launch carries the id.
func TestResolve_ProjectIdentity_EstablishedOnlyOnceAdmitted(t *testing.T) {
	env := launchtest.Deps(t)
	calls := 0
	env.Deps.ProjectIdentity = func() (string, error) { calls++; return "minted-id", nil }

	_, err := launch.Resolve(context.Background(), env.Deps, launch.Source{Identity: env.Identity, Agent: "nobody", Mode: engine.Structured, Permission: "bypass", Prompt: "x", WorkDir: env.Project})
	require.ErrorIs(t, err, launch.ErrNoAgent)
	assert.Zero(t, calls, "a refused launch establishes nothing")

	id := env.Identity
	id.Project = ""
	l, err := launch.Resolve(context.Background(), env.Deps, launch.Source{Identity: id, Agent: "setup", Mode: engine.Structured, Permission: "bypass", Prompt: "x", WorkDir: env.Project})
	require.NoError(t, err)
	t.Cleanup(func() { _ = launch.Discard(context.Background(), l) })
	assert.Equal(t, 1, calls, "an admitted launch establishes the identity once")
	assert.Equal(t, "minted-id", l.Identity.Project)
}

// TestResolve_ProjectIdentity_FailureDegrades: the identity is fault-tolerant
// — a failure to establish it is reported and the session runs without one,
// as it did when the caller resolved it up front.
func TestResolve_ProjectIdentity_FailureDegrades(t *testing.T) {
	env := launchtest.Deps(t)
	env.Deps.ProjectIdentity = func() (string, error) { return "", errors.New("registry unreadable") }
	id := env.Identity
	id.Project = ""

	l, err := launch.Resolve(context.Background(), env.Deps, launch.Source{Identity: id, Agent: "setup", Mode: engine.Structured, Permission: "bypass", Prompt: "x", WorkDir: env.Project})
	require.NoError(t, err)
	t.Cleanup(func() { _ = launch.Discard(context.Background(), l) })
	assert.Empty(t, l.Identity.Project)
}

// TestResolve_CredentialRefusal_ComesAfterContentAndBeforeTheIdentity: a run
// that cannot authenticate is refused before its project identity is
// established — but after selection and assembly, so what is missing about
// the launch itself is still reported as that, not as a credential.
func TestResolve_CredentialRefusal_ComesAfterContentAndBeforeTheIdentity(t *testing.T) {
	env := launchtest.Deps(t)
	noCredential := fmt.Errorf("no token: %w", engine.ErrNoCredential)
	env.Deps.CheckCredential = func(string, engine.AuthMode) error { return noCredential }
	minted := 0
	env.Deps.ProjectIdentity = func() (string, error) { minted++; return "id", nil }
	id := env.Identity
	id.Project = ""

	_, err := launch.Resolve(context.Background(), env.Deps, launch.Source{Identity: id, Agent: "nobody", Mode: engine.Structured, Permission: "bypass", Prompt: "x", WorkDir: env.Project})
	require.ErrorIs(t, err, launch.ErrNoAgent, "what to launch is judged first")
	assert.NotErrorIs(t, err, engine.ErrNoCredential)

	_, err = launch.Resolve(context.Background(), env.Deps, launch.Source{Identity: id, Agent: "setup", Mode: engine.Structured, Permission: "bypass", Prompt: "x", WorkDir: env.Project})
	require.ErrorIs(t, err, engine.ErrNoCredential)
	assert.Zero(t, minted, "a run that cannot authenticate establishes nothing")
}
