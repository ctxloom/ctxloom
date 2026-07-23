package coord

import "testing"

// TestAdvertiseHostFor pins the per-(GOOS, container-runtime) dial-home
// decision from ensureWide's doc: darwin/windows advertise the Docker-
// Desktop/Podman-Machine magic hostname against the existing loopback
// listener (brisk-mango); any other GOOS (Linux today) falls back to the
// bridge-gateway/primary-outbound-IP path, signaled by "".
func TestAdvertiseHostFor(t *testing.T) {
	cases := []struct {
		name        string
		goos        string
		runtimeName string
		want        string
	}{
		{"darwin docker", "darwin", "docker", "host.docker.internal"},
		{"darwin podman", "darwin", "podman", "host.containers.internal"},
		{"windows docker", "windows", "docker", "host.docker.internal"},
		{"windows podman", "windows", "podman", "host.containers.internal"},
		{"darwin unknown runtime defaults to docker", "darwin", "", "host.docker.internal"},
		{"linux docker: bridge-gateway path, not a magic hostname", "linux", "docker", ""},
		{"linux podman: bridge-gateway path, not a magic hostname", "linux", "podman", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := advertiseHostFor(tc.goos, tc.runtimeName)
			if got != tc.want {
				t.Errorf("advertiseHostFor(%q, %q) = %q, want %q", tc.goos, tc.runtimeName, got, tc.want)
			}
		})
	}
}

// TestPreferredContainerRuntimeIsDockerOrPodman confirms the live-host probe
// only ever names one of the two runtimes ensureWide understands (never a
// blank string that would silently fall through advertiseHostFor's default
// case for an unrelated reason) — the whole point of the probe is picking
// docker vs podman for the magic-hostname choice above.
func TestPreferredContainerRuntimeIsDockerOrPodman(t *testing.T) {
	got := preferredContainerRuntime()
	if got != "docker" && got != "podman" {
		t.Fatalf("preferredContainerRuntime() = %q, want %q or %q", got, "docker", "podman")
	}
}
