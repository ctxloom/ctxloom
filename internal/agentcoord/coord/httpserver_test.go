package coord

import (
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAdvertiseHostFor pins the per-(GOOS, container-runtime) dial-home
// decision from ensureWide's doc: darwin/windows advertise the Docker-
// Desktop/Podman-Machine magic hostname against the existing loopback
// listener; any other GOOS (Linux today) falls back to the
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

// fakeDockerOnPath installs a stub `docker` earlier on PATH than any real one,
// printing output on the network-inspect probe. PATH holds ONLY the stub dir,
// so podman is absent too and the probe set is fully controlled.
func fakeDockerOnPath(t *testing.T, output string) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\nprintf '%s\\n' " + strconv.Quote(output) + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0o700))
	t.Setenv("PATH", dir)
}

// TestContainerReachIPs_RejectsNonAddressProbeOutput: the gateway probe is a
// Go text/template evaluated by the container runtime, and a template whose
// field is missing prints the sentinel "<no value>" rather than failing. The
// filter used to compare against that one literal string, so any OTHER
// non-address output — a template error, a warning line, a runtime that
// renamed the field — became a candidate host IP the coordinator would try to
// bind and advertise to a container. Only something that parses as an address
// is an address.
func TestContainerReachIPs_RejectsNonAddressProbeOutput(t *testing.T) {
	for _, junk := range []string{
		"<no value>",
		"Template parsing error: template: :1:2: executing \"\" at <.IPAM>: nil pointer",
		"error during connect: Get \"http://docker/v1.47/networks/bridge\": dial unix: permission denied",
		"map[]",
	} {
		t.Run(junk, func(t *testing.T) {
			fakeDockerOnPath(t, junk)
			for _, ip := range containerReachIPs() {
				assert.NotNil(t, net.ParseIP(ip), "probe output %q must not become a candidate host address", ip)
			}
		})
	}
}

// TestContainerReachIPs_KeepsARealGatewayAddress: the filter is narrow — a
// genuine gateway address still comes back, first (most specific first).
func TestContainerReachIPs_KeepsARealGatewayAddress(t *testing.T) {
	fakeDockerOnPath(t, "172.17.0.1")

	got := containerReachIPs()
	require.NotEmpty(t, got)
	assert.Equal(t, "172.17.0.1", got[0], "the bridge gateway must stay ahead of the outbound-interface fallback")
}

// TestContainerReachIPs_ReturnsOnlyRoutableHostAddresses pins the posture
// ensureWide's Linux fallback actually takes: the candidates it binds are
// NON-loopback host addresses. That is the point (a container cannot reach the
// host's 127.0.0.1) and it is the reason widening is on-demand and
// credential-gated rather than the default — a widened listener is reachable
// from the host's network, not just from the container.
func TestContainerReachIPs_ReturnsOnlyRoutableHostAddresses(t *testing.T) {
	for _, ip := range containerReachIPs() {
		parsed := net.ParseIP(ip)
		require.NotNil(t, parsed, "candidate %q must be an address", ip)
		assert.False(t, parsed.IsLoopback(), "a container-reachable candidate is never loopback: %q", ip)
		assert.False(t, parsed.IsUnspecified(), "the coordinator never binds the wildcard address: %q", ip)
	}
}

// TestServe_BindsLoopbackOnly: nothing wide exists until a container child asks
// for it. This is the invariant that keeps a plain host session off the
// machine's network entirely.
func TestServe_BindsLoopbackOnly(t *testing.T) {
	c := newTestCoordinatorAt(t, t.TempDir())
	t.Cleanup(c.Close)
	require.NoError(t, c.Serve())

	parsed, err := url.Parse(c.LoopbackURL())
	require.NoError(t, err)
	host, _, err := net.SplitHostPort(parsed.Host)
	require.NoError(t, err)
	assert.True(t, net.ParseIP(host).IsLoopback(), "Serve binds loopback and nothing else: %q", host)

	srv := c.srv.Load()
	srv.mu.Lock()
	defer srv.mu.Unlock()
	assert.Empty(t, srv.wide, "no wide listener may exist before a container run asks for one")
	assert.Empty(t, srv.wideURL)
}
