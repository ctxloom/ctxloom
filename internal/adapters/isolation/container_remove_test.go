package isolation

import (
	"errors"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
)

// exitWith runs a shell that writes stderr and exits code, returning the error
// the way probeExec's .Output() surfaces it (an *exec.ExitError carrying the
// stderr), so removeOutcome reads exactly what it reads in production.
func exitWith(t *testing.T, stderr string, code int) error {
	t.Helper()
	_, err := exec.Command("sh", "-c", "printf '%s' \"$1\" >&2; exit $2", "sh", stderr, string(rune('0'+code))).Output()
	return err
}

// TestRemoveOutcome: each runtime reads its OWN CLI's teardown report.
//
// Measured on this host (docker 29, podman 5.4.2): `rm -f` of a missing name
// exits 0 with EMPTY stdout on both — docker says "No such container" on
// stderr, podman says nothing — and a remove that took a container down
// echoes its name. Docker additionally reports gone-ness through a FAILURE on
// older daemons ("No such container", exit 1) and while its own --rm cleanup
// is in flight ("removal of container ... is already in progress"); podman
// does not speak docker's wording, so the same failure is unconfirmed there.
func TestRemoveOutcome(t *testing.T) {
	gone := exitWith(t, "Error response from daemon: No such container: abc", 1)
	inProgress := exitWith(t, "Error response from daemon: removal of container abc is already in progress", 1)
	wedged := exitWith(t, "daemon not responding", 1)
	timeout := errors.New("context deadline exceeded")

	cases := []struct {
		name   string
		rt     Runtime
		stdout string
		err    error
		want   removeOutcome
	}{
		{"docker removed", Docker{}, "abc\n", nil, removeRemoved},
		{"docker missing name exits 0 empty", Docker{}, "", nil, removeAlreadyGone},
		{"docker older daemon no such container", Docker{}, "", gone, removeAlreadyGone},
		{"docker own --rm in flight", Docker{}, "", inProgress, removeAlreadyGone},
		{"docker wedged", Docker{}, "", wedged, removeFailed},
		{"docker timeout", Docker{}, "", timeout, removeFailed},
		{"podman removed", Podman{}, "abc\n", nil, removeRemoved},
		{"podman missing name exits 0 empty", Podman{}, "", nil, removeAlreadyGone},
		{"podman does not read docker wording", Podman{}, "", gone, removeFailed},
		{"podman wedged", Podman{}, "", wedged, removeFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.rt.removeOutcome([]byte(tc.stdout), tc.err))
		})
	}
}
