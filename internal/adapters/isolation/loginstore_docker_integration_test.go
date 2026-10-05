//go:build docker_integration

// The live-daemon proof that a container run in claude's `auth: login` is
// refused (local-secrets risk 2): with a real runtime surveyed, not a stub,
// the chain's head is the container, and Prepare refuses before it prepares
// anything, so no part of the human's ~/.claude is ever mounted. Build-tagged
// so `just test` never compiles it; run with:
//
//	just test-pkg ./internal/adapters/isolation -tags docker_integration -run TestLoginStore_
package isolation

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/testsupport/dockergate"
)

func TestLoginStore_ALiveContainerRunRefusesTheLogin(t *testing.T) {
	dockergate.RequireRuntime(t, (Docker{}).Available(), "the login-store refusal integration test")
	resetStrictness(t)
	// Demand the ownership this daemon HAS: a demand it cannot serve never
	// reaches a container at all (chainFor refuses it at the gate instead), so
	// the store refusal under test would go unexercised.
	_, owns := newDockerRuntime(runtimeReachable)
	require.NotEqual(t, ownershipUndecided, owns, "docker answered but its ownership could not be probed")
	home := fakeHostHome(t, tokenFixture) // a fixture login under a fake $HOME, never a real one
	s, err := NewSpec(launch.Axes{Workspace: WorkspaceShared, Runtime: owns}, claudeEngine(t)).Project(t.TempDir()).
		Session(harpA, sessionDir(home, harpA), SessionState{Harp: harpA}).Credentials(claudeCredentials(t, engine.AuthLogin)).Build()
	require.NoError(t, err)

	_, err = Prepare(context.Background(), s)
	require.ErrorIs(t, err, engine.ErrHostOnlyStore)
	assert.NoDirExists(t, sessionDir(home, harpA), "refused before anything was created")
}
