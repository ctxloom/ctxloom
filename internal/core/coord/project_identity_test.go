package coord

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/sessions"
	taskpaths "github.com/ctxloom/ctxloom/internal/shared/tasks/paths"
)

// testProjectID is a resolved project id of the shape the project registry
// mints: a clean single path segment.
const testProjectID = "swift-amber-falcon"

// newProjectCoordinator is a coordinator serving a real project DIRECTORY
// (an absolute path, separators and all) under the resolved projectID.
func newProjectCoordinator(t *testing.T, sp Spawner, projectID string) *Coordinator {
	t.Helper()
	teeHome(t)
	c, err := New(Options{
		ProjectDir: t.TempDir(),
		ProjectKey: projectID,
		StateDir:   t.TempDir(),
		Spawner:    sp,
		OwnerHarp:  ownerIdentity().Harp,
		Reporter:   termSink(),
	})
	require.NoError(t, err)
	require.NoError(t, runnerHooks.Serve(c))
	t.Cleanup(c.Close)
	return c
}

// spawnedIdentity spawns a researcher and returns the identity its launch
// was resolved under — the one its engine env is stamped from.
func spawnedIdentity(t *testing.T, c *Coordinator) Identity {
	t.Helper()
	sp := c.spawner.(*fakeSpawner)
	spawnResearcher(t, c)
	var id Identity
	require.Eventually(t, func() bool {
		sp.mu.Lock()
		defer sp.mu.Unlock()
		if len(sp.identities) == 0 {
			return false
		}
		id = sp.identities[0]
		return true
	}, conformanceWait, 10*time.Millisecond, "the child's launch was never resolved")
	return id
}

// TestChildIdentity_CarriesTheProjectIDNotTheDirectory: the project id a
// spawned child exports (sessions.EnvProjectID) is the resolved id, and it
// keys a task-store path — the exact resolution a container child's
// task-store mount makes. A directory there fails ValidateProjectID on its
// first '/', which is how a containerized child died at standup.
func TestChildIdentity_CarriesTheProjectIDNotTheDirectory(t *testing.T) {
	resetStrictness(t)
	c := newProjectCoordinator(t, researcherSpawner(), testProjectID)

	env := sessions.HookEnv(spawnedIdentity(t, c))
	got := env[sessions.EnvProjectID]
	assert.Equal(t, testProjectID, got)
	require.NoError(t, taskpaths.ValidateProjectID(got))
	_, err := taskpaths.HomeTasksLogPath(got)
	require.NoError(t, err, "a container child's task-store mount resolves from this id")
}

// TestChildIdentity_UnresolvedProjectExportsNoID: with no resolved project
// id the child exports none — the container path's documented degrade — and
// never the directory in its place.
func TestChildIdentity_UnresolvedProjectExportsNoID(t *testing.T) {
	resetStrictness(t)
	c := newProjectCoordinator(t, researcherSpawner(), "")

	env := sessions.HookEnv(spawnedIdentity(t, c))
	assert.Empty(t, env[sessions.EnvProjectID])
}

// TestIdentify_OwnerCredentialCarriesTheProjectID: the session-owner
// credential identifies under the project id, like every child's.
func TestIdentify_OwnerCredentialCarriesTheProjectID(t *testing.T) {
	c := newProjectCoordinator(t, newFakeSpawner(nil, nil), testProjectID)
	token, err := c.RegisterSessionOwner(ownerIdentity().Harp)
	require.NoError(t, err)

	id, ok := c.Identify(token)
	require.True(t, ok)
	assert.Equal(t, testProjectID, id.Project)
}
