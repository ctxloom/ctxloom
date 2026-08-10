package coord

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/agentcoord/discover"
)

// endpoint.json is a seam with a writer here and a reader in
// internal/agentcoord/discover — the D1 consumer discovery path a separate CLI
// invocation (the TUI, `ctxloom session transcript watch`) uses to find a live
// coordinator. The two halves are in different packages by necessity: coord
// imports internal/operations, which imports discover, so discover can never
// import coord. That makes this the one contract in the package with no
// call-graph link at all, and a silent mismatch here reads as "no coordinator
// is running" — this project's signature failure mode.
//
// So it is pinned end to end: a real Serve() writes the file, and the real
// discovery reader has to find it and hand back a URL that dials.

func TestEndpointFile_ServeWritesWhatDiscoverReads(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// StateDir empty on purpose: this must land in the HOME-relative state dir
	// discover.List globs, not a test temp dir off to one side.
	c, err := New(Options{
		ProjectDir: t.TempDir(),
		ProjectKey: "endpoint-discovery-test",
		Spawner:    newFakeSpawner(nil, nil),
	})
	require.NoError(t, err)
	t.Cleanup(c.Close)
	require.NoError(t, c.Serve())

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

	c, err := New(Options{
		ProjectDir: t.TempDir(),
		ProjectKey: "endpoint-unserved-test",
		Spawner:    newFakeSpawner(nil, nil),
	})
	require.NoError(t, err)
	t.Cleanup(c.Close)

	endpoints, skipped := discover.List()
	assert.Empty(t, endpoints)
	assert.Empty(t, skipped, "a coordinator that has not Served yet is not a fault")
}
