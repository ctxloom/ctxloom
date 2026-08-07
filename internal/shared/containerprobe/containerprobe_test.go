package containerprobe

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

// noStat/noRead/noEnv are the "nothing matches" seam: a host with no sentinel
// file, no container env var, and no readable init cgroup.
func noStat(string) error           { return os.ErrNotExist }
func noRead(string) ([]byte, error) { return nil, os.ErrNotExist }
func noEnv(string) string           { return "" }
func envWith(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestMarkersFrom_NoSignalsMeansNotInAContainer(t *testing.T) {
	assert.Empty(t, MarkersFrom(noStat, noRead, noEnv))
}

// Each probe must be able to answer ALONE. A runtime that exposes only one of
// the three (podman's sentinel without the docker one, cgroup v2 with no
// runtime string, a k8s pod with neither file) is the normal case, not the
// exception — so a detector that needed agreement would report "host" inside a
// real container.
func TestMarkersFrom_EachProbeAnswersOnItsOwn(t *testing.T) {
	tests := []struct {
		name     string
		stat     func(string) error
		readFile func(string) ([]byte, error)
		getenv   func(string) string
		want     string
	}{
		{
			name:     "docker sentinel file",
			stat:     func(p string) error { return map[bool]error{true: nil, false: os.ErrNotExist}[p == "/.dockerenv"] },
			readFile: noRead,
			getenv:   noEnv,
			want:     "/.dockerenv",
		},
		{
			name: "podman sentinel file",
			stat: func(p string) error {
				return map[bool]error{true: nil, false: os.ErrNotExist}[p == "/run/.containerenv"]
			},
			readFile: noRead,
			getenv:   noEnv,
			want:     "/run/.containerenv",
		},
		{
			name:     "devcontainer env var",
			stat:     noStat,
			readFile: noRead,
			getenv:   envWith(map[string]string{"REMOTE_CONTAINERS": "true"}),
			want:     "$REMOTE_CONTAINERS",
		},
		{
			name:     "kubernetes env var",
			stat:     noStat,
			readFile: noRead,
			getenv:   envWith(map[string]string{"KUBERNETES_SERVICE_HOST": "10.0.0.1"}),
			want:     "$KUBERNETES_SERVICE_HOST",
		},
		{
			name:     "cgroup v1 runtime signature",
			stat:     noStat,
			readFile: func(string) ([]byte, error) { return []byte("12:pids:/docker/abc123"), nil },
			getenv:   noEnv,
			want:     "cgroup:docker",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, []string{tt.want}, MarkersFrom(tt.stat, tt.readFile, tt.getenv))
		})
	}
}

// A cgroup v2 host exposes a bare "0::/" with no runtime name. It must not
// register as a container — this is precisely why the cgroup probe is
// documented as best-effort and never stands alone as a negative signal.
func TestMarkersFrom_CgroupV2BareHierarchyIsNotAContainer(t *testing.T) {
	assert.Empty(t, MarkersFrom(noStat, func(string) ([]byte, error) { return []byte("0::/\n"), nil }, noEnv))
}

// InContainer is the boolean projection: it must follow the markers rather
// than carry a second, drifting definition.
func TestInContainer_FollowsTheMarkers(t *testing.T) {
	assert.Equal(t, len(Markers()) > 0, InContainer())
}
