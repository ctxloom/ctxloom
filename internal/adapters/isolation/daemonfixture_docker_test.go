//go:build docker_integration

package isolation

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport/dockergate"
)

// daemonFixtures is daemonfixture.Root for this package's own tests, which
// cannot import it (it imports isolation): t's temp dir is pointed at the
// fixture root runtime's daemon can name, and this process's layer is
// returned for the sources a test renders itself. A docker test calls it
// before creating any fixture.
func daemonFixtures(t *testing.T, runtime string) Layer {
	t.Helper()
	l, err := DaemonLayer(runtime)
	require.NoError(t, err, "this process's layer on the %s daemon", runtime)
	root, err := FixtureRoot(l)
	if err != nil {
		dockergate.SkipCapability(t, "no directory this process writes is one the "+runtime+" daemon can name: "+err.Error())
	}
	if root != os.TempDir() {
		t.Setenv("TMPDIR", root)
	}
	return l
}

// TestFixtureRoot_FirstCandidateTheLayerNames: a docker-gated test roots its
// fixtures under the first candidate its own layer names in the daemon's path
// space — the process temp dir where it is shared, else the runner's temp a
// CI job container is given from the host — and refuses when neither is.
func TestFixtureRoot_FirstCandidateTheLayerNames(t *testing.T) {
	root, err := fixtureRoot(ciLayer, []string{"/tmp", "/__w/_temp"})
	require.NoError(t, err)
	assert.Equal(t, "/__w/_temp", root, "a job container's private /tmp has no name on the daemon; the runner temp does")

	root, err = fixtureRoot(HostLayer(), []string{"/tmp", "/__w/_temp"})
	require.NoError(t, err)
	assert.Equal(t, "/tmp", root, "on the daemon's host the process temp dir is already the daemon's")

	_, err = fixtureRoot(ciLayer, []string{"/tmp", ""})
	require.ErrorIs(t, err, ErrUnmapped, "no candidate the daemon can name is a refusal, not a blank bind")
}
