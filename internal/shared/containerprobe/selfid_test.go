package containerprobe

import (
	"errors"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

const (
	idA = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	idB = "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
)

// files is a fake filesystem: a path absent from the map reads as not-exist.
func files(m map[string]string) func(string) ([]byte, error) {
	return func(p string) ([]byte, error) {
		if s, ok := m[p]; ok {
			return []byte(s), nil
		}
		return nil, os.ErrNotExist
	}
}

func hostname(h string, err error) func() (string, error) {
	return func() (string, error) { return h, err }
}

// TestSelfIDCandidatesFrom: each runtime's own trace of the container id, read
// from fixture files, never the real /proc; strongest first, deduplicated.
func TestSelfIDCandidatesFrom(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
		host  string
		want  []string
	}{
		{
			name: "docker rootful mountinfo under cgroup v2",
			files: map[string]string{
				"/proc/self/mountinfo": "812 790 0:52 / / rw,relatime - overlay overlay rw\n" +
					"830 812 259:2 /var/lib/docker/containers/" + idA + "/resolv.conf /etc/resolv.conf rw,relatime - ext4 /dev/nvme0n1p2 rw\n" +
					"831 812 259:2 /var/lib/docker/containers/" + idA + "/hostname /etc/hostname rw,relatime - ext4 /dev/nvme0n1p2 rw\n",
				"/proc/self/cgroup": "0::/\n",
			},
			host: "devbox",
			want: []string{idA},
		},
		{
			name: "rootless docker mountinfo",
			files: map[string]string{
				"/proc/self/mountinfo": "900 880 0:40 /home/u/.local/share/docker/containers/" + idA + "/hosts /etc/hosts rw - ext4 /dev/sda1 rw\n",
			},
			want: []string{idA},
		},
		{
			name: "podman overlay-containers",
			files: map[string]string{
				"/proc/self/mountinfo": "700 650 0:33 /containers/storage/overlay-containers/" + idA + "/userdata/hostname /etc/hostname rw - tmpfs tmpfs rw\n",
			},
			want: []string{idA},
		},
		{
			name:  "podman containerenv id",
			files: map[string]string{"/run/.containerenv": "engine=\"podman-5.4.2\"\nname=\"box\"\nid=\"" + idB + "\"\nimage=\"x\"\n"},
			want:  []string{idB},
		},
		{
			name:  "cgroup v1 docker path",
			files: map[string]string{"/proc/self/cgroup": "12:memory:/docker/" + idB + "\n11:cpu:/docker/" + idB + "\n"},
			want:  []string{idB},
		},
		{
			name:  "cgroup v1 systemd scope",
			files: map[string]string{"/proc/self/cgroup": "1:name=systemd:/system.slice/docker-" + idB + ".scope\n"},
			want:  []string{idB},
		},
		{
			name: "hex hostname is a candidate",
			host: "0123456789ab",
			want: []string{"0123456789ab"},
		},
		{
			name: "a plain hostname never is",
			host: "devbox",
			want: nil,
		},
		{
			name: "a hex-looking hostname too short is not",
			host: "cafe",
			want: nil,
		},
		{
			name: "strongest first, deduplicated across sources",
			files: map[string]string{
				"/proc/self/mountinfo": "830 812 259:2 /var/lib/docker/containers/" + idA + "/resolv.conf /etc/resolv.conf rw - ext4 /dev/x rw\n",
				"/run/.containerenv":   "id=\"" + idB + "\"\n",
				"/proc/self/cgroup":    "12:memory:/docker/" + idA + "\n",
			},
			host: "0123456789ab",
			want: []string{idA, idB, "0123456789ab"},
		},
		{
			name: "nothing suggests a container",
			want: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SelfIDCandidatesFrom(files(tc.files), hostname(tc.host, nil))
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestSelfIDCandidatesFrom_HostnameErrorIsNoCandidate: an unreadable hostname
// contributes nothing rather than failing the lookup.
func TestSelfIDCandidatesFrom_HostnameErrorIsNoCandidate(t *testing.T) {
	assert.Nil(t, SelfIDCandidatesFrom(files(nil), hostname("", errors.New("no uts"))))
}
