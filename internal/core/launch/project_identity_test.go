package launch_test

import (
	"context"
	"errors"
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
