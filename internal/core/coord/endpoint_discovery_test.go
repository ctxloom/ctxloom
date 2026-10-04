package coord

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/discover"
)

// endpoint.json is a seam with a writer here and a reader in
// internal/adapters/coordgrpc/discover — the D1 consumer discovery path a separate CLI
// invocation (the TUI, `ctxloom session transcript watch`) uses to find a live
// coordinator. The two halves are in different packages by necessity: this
// package's own tests import discover, so discover can never import coord.
// That makes this the one contract in the package with no call-graph link at
// all, and a silent mismatch here reads as "no coordinator is running" — this
// project's signature failure mode.
//
// So it is pinned end to end: a real Serve() writes the file, and the real
// discovery reader has to find it and hand back a URL that dials.

func TestEndpointFile_ServeWritesWhatDiscoverReads(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// StateDir empty on purpose: this must land in the HOME-relative state dir
	// discover.List globs, not a test temp dir off to one side.
	teeHome(t)
	c, err := New(Options{
		ProjectDir: t.TempDir(),
		ProjectID:  "endpoint-discovery-test",
		Spawner:    newFakeSpawner(t, nil, nil),
		OwnerHarp:  ownerIdentity().Harp,
	})
	require.NoError(t, err)
	t.Cleanup(c.Close)
	require.NoError(t, runnerHooks.Serve(c))

	endpoints, skipped := discover.List()
	assert.Empty(t, skipped, "a freshly served coordinator must not look like a corrupt candidate")
	require.Len(t, endpoints, 1, "the served coordinator must be discoverable")

	assert.Equal(t, c.LoopbackURL(), endpoints[0].URL,
		"the discovered URL must be the one the coordinator actually bound, path and all")
	assert.NotEmpty(t, endpoints[0].Cred, "discovery must carry the read-only consumer credential")

	id, ok := c.Identify(endpoints[0].Cred)
	assert.True(t, ok, "the discovered credential must be one this coordinator accepts")
	assert.True(t, id.Consumer, "the discovered credential must be the read-only consumer class, never a run credential")
}

// TestEndpointFile_NotYetMintedIsSkippedSilently: a state dir whose Serve()
// never ran is the common early case, not corruption — it must not be reported
// as a skipped candidate, or every start-up would look like a fault.
func TestEndpointFile_NotYetMintedIsSkippedSilently(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	teeHome(t)
	c, err := New(Options{
		ProjectDir: t.TempDir(),
		ProjectID:  "endpoint-unserved-test",
		Spawner:    newFakeSpawner(t, nil, nil),
		OwnerHarp:  ownerIdentity().Harp,
	})
	require.NoError(t, err)
	t.Cleanup(c.Close)

	endpoints, skipped := discover.List()
	assert.Empty(t, endpoints)
	assert.Empty(t, skipped, "a coordinator that has not Served yet is not a fault")
}

// A coordinator that has exited leaves endpoint.json behind on purpose (a
// relaunch re-binds its ports), but nothing answers on that port and its
// consumer credential is dead. discover reads the root's owner lock, which
// the kernel releases with the process, so the exited coordinator is no
// longer discoverable.
func TestEndpointFile_ExitedCoordinatorIsNotDiscovered(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	teeHome(t)
	c, err := New(Options{
		ProjectDir: t.TempDir(),
		ProjectID:  "endpoint-exited-test",
		Spawner:    newFakeSpawner(t, nil, nil),
		OwnerHarp:  ownerIdentity().Harp,
	})
	require.NoError(t, err)
	require.NoError(t, runnerHooks.Serve(c))
	endpoints, _ := discover.List()
	require.Len(t, endpoints, 1, "precondition: the served coordinator is discoverable")

	c.Close()
	require.FileExists(t, filepath.Join(c.StateDir(), discover.FileName),
		"precondition: the endpoint file outlives its coordinator")

	endpoints, skipped := discover.List()
	assert.Empty(t, endpoints, "an exited coordinator's endpoint must not be handed back")
	assert.Empty(t, skipped, "an exited coordinator is the ordinary case, not a fault")
}
