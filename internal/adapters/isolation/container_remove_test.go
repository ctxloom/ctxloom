package isolation

import (
	"errors"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestRemoveReportsGone: only the benign already-gone stderr counts as success;
// a wedged/other failure (or a non-exit error like a timeout) is a potential leak.
func TestRemoveReportsGone(t *testing.T) {
	_, gone := exec.Command("sh", "-c", "echo 'No such container: abc' >&2; exit 1").Output()
	_, wedged := exec.Command("sh", "-c", "echo 'daemon not responding' >&2; exit 1").Output()
	// The SECOND benign race (ISO1, live-verified against a real docker
	// daemon: a long-lived attached container exiting right as our own
	// `rm -f` lands hits this message 100% of the time on this docker
	// version) — docker's OWN async --rm cleanup is
	// in-flight at the exact moment ours runs; the container is gone (or
	// guaranteed to become so) either way, not a leak.
	_, inProgress := exec.Command("sh", "-c",
		"echo 'Error response from daemon: removal of container abc is already in progress' >&2; exit 1").Output()
	assert.True(t, removeReportsGone(gone))
	assert.True(t, removeReportsGone(inProgress))
	assert.False(t, removeReportsGone(wedged))
	assert.False(t, removeReportsGone(errors.New("context deadline exceeded")))
}
