package isolation

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
)

// sampleSpec is a representative RunSpec used across the argv-rendering tests.
func sampleSpec() RunSpec {
	return RunSpec{
		Image:   "ctxloom-agent:latest",
		Name:    "ctxloom-iso-m-abc",
		WorkDir: "/home/u/proj",
		Home:    "/root",
		Command: []string{"/usr/local/bin/ctxloom", "llm", "serve", "mock"},
		Env:     []string{"CTXLOOM_PLUGIN=ai-backend-v1", "PLUGIN_PROTOCOL_VERSIONS=1"},
		Mounts: []Mount{
			{Host: "/home/u/proj", Container: "/home/u/proj"},
			{Host: "/tmp/sock", Container: "/run/ctxloom/plugin"},
		},
	}
}

// TestDockerRootless_RunsAsMappedRoot: rootless docker maps container-root to
// the host user — the ONLY uid that does — so the argv carries neither --user
// nor a PUID remap request (the run stays container-root). The identical-path
// project mount, socket mount, workdir, image, and in-container command must
// all render.
func TestDockerRootless_RunsAsMappedRoot(t *testing.T) {
	args := Docker{rootless: true}.RunArgs(sampleSpec())
	joined := strings.Join(args, " ")

	assert.Equal(t, []string{"run", "--rm", "--name", "ctxloom-iso-m-abc"}, args[:4], "run head")
	assert.NotContains(t, joined, "--user", "rootless docker maps root→host user; no --user")
	assert.NotContains(t, joined, "PUID", "no identity remap: container-root IS the launching user")
	assert.Contains(t, joined, "--mount type=bind,source=/home/u/proj,target=/home/u/proj", "identical-path project mount")
	assert.Contains(t, joined, "--mount type=bind,source=/tmp/sock,target=/run/ctxloom/plugin", "socket-dir mount")
	assert.Contains(t, joined, "-e HOME=/root", "fresh HOME")
	assert.Contains(t, joined, "-w /home/u/proj", "workdir")
	// The image precedes the in-container command as the final tokens.
	assert.Equal(t,
		[]string{"ctxloom-agent:latest", "/usr/local/bin/ctxloom", "llm", "serve", "mock"},
		args[len(args)-5:], "image then in-container argv")
}

// TestDockerRootful_PassesIdentityEnv: under a rootful daemon the container
// starts as root and PUID/PGID tell the image entrypoint to remap its ctxloom
// user to the launching uid/gid and drop to it — so the plugin's bind-mounted
// socket and every project write land host-user-owned. No --user: the
// entrypoint needs root to usermod.
func TestDockerRootful_PassesIdentityEnv(t *testing.T) {
	joined := strings.Join(Docker{rootless: false}.RunArgs(sampleSpec()), " ")
	assert.Contains(t, joined, fmt.Sprintf("-e PUID=%d", os.Getuid()), "launching uid crosses for the remap")
	assert.Contains(t, joined, fmt.Sprintf("-e PGID=%d", os.Getgid()), "launching gid crosses for the remap")
	assert.NotContains(t, joined, "--user", "the entrypoint, not --user, sets identity")
}

// TestPodmanRootful_DockerCompatibleArgv: rootful podman matches rootful
// docker — identity env for the entrypoint remap, no keep-id, no --user.
func TestPodmanRootful_DockerCompatibleArgv(t *testing.T) {
	args := Podman{}.RunArgs(sampleSpec())
	joined := strings.Join(args, " ")
	assert.Equal(t, []string{"run", "--rm", "--name", "ctxloom-iso-m-abc"}, args[:4])
	assert.NotContains(t, joined, "keep-id")
	assert.NotContains(t, joined, "--user")
	assert.Contains(t, joined, fmt.Sprintf("-e PUID=%d", os.Getuid()))
	assert.Contains(t, joined, "--mount type=bind,source=/home/u/proj,target=/home/u/proj")
	assert.Equal(t, []string{"rm", "-f", "c1"}, Podman{}.RemoveArgs("c1"))
}

// TestPodmanRootless_KeepIDAsRoot: rootless podman needs keep-id so the
// launching uid maps to ITSELF in-container, and must enter as namespaced root
// (keep-id's default user is the host uid, which could not usermod) so the
// entrypoint can remap ctxloom to PUID/PGID and drop to it.
func TestPodmanRootless_KeepIDAsRoot(t *testing.T) {
	joined := strings.Join(Podman{rootless: true}.RunArgs(sampleSpec()), " ")
	assert.Contains(t, joined, "--userns=keep-id", "launching uid maps to itself")
	assert.Contains(t, joined, "--user 0:0", "enter as namespaced root for the remap")
	assert.Contains(t, joined, fmt.Sprintf("-e PUID=%d", os.Getuid()))
}

// TestRemoveArgs force-removes by name for teardown.
func TestRemoveArgs(t *testing.T) {
	assert.Equal(t, []string{"rm", "-f", "c1"}, Docker{}.RemoveArgs("c1"))
}

// TestHost_IsNonContainer: Host is always available and launches nothing (the
// None/worktree path spawns a bare host subprocess instead).
func TestHost_IsNonContainer(t *testing.T) {
	h := Host{}
	assert.Equal(t, "host", h.Name())
	assert.Empty(t, h.Binary())
	assert.True(t, h.Available(), "the host can always run a subprocess")
	assert.Nil(t, h.RunArgs(sampleSpec()))
	assert.Nil(t, h.RemoveArgs("c1"))
}

// TestContainerHandshakeEnv_Curates keeps ONLY the go-plugin handshake vars
// (magic cookie + PLUGIN_*), overrides the socket dir to the container path, and
// drops everything else (host paths/secrets never cross the boundary).
func TestContainerHandshakeEnv_Curates(t *testing.T) {
	in := []string{
		pb.HandshakeConfig.MagicCookieKey + "=ai-backend-v1",
		"PLUGIN_PROTOCOL_VERSIONS=1",
		"PLUGIN_MIN_PORT=0",
		"PLUGIN_UNIX_SOCKET_DIR=/tmp/host-sock", // must be overridden
		"HOME=/home/babbitt",                    // host env — must be dropped
		"ANTHROPIC_API_KEY=secret",              // secret — must be dropped
		"malformed-no-equals",                   // skipped
	}
	out := containerHandshakeEnv(in, "/run/ctxloom/plugin")

	assert.Contains(t, out, pb.HandshakeConfig.MagicCookieKey+"=ai-backend-v1")
	assert.Contains(t, out, "PLUGIN_PROTOCOL_VERSIONS=1")
	assert.Contains(t, out, "PLUGIN_MIN_PORT=0")
	assert.Contains(t, out, "PLUGIN_UNIX_SOCKET_DIR=/run/ctxloom/plugin", "socket dir overridden to the container path")
	assert.NotContains(t, out, "PLUGIN_UNIX_SOCKET_DIR=/tmp/host-sock", "host socket path must not leak")
	for _, kv := range out {
		assert.False(t, strings.HasPrefix(kv, "HOME="), "host HOME must not cross")
		assert.NotContains(t, kv, "ANTHROPIC_API_KEY", "secrets must not cross")
	}
}

// TestAddrTranslator_PrefixSwap maps the plugin's announced container socket path
// to the host bind mount and back, and leaves paths outside the mount untouched.
func TestAddrTranslator_PrefixSwap(t *testing.T) {
	tr := containerAddrTranslator{hostSocketDir: "/tmp/host-sock", containerSocketDir: "/run/ctxloom/plugin"}

	net, host, err := tr.PluginToHost("unix", "/run/ctxloom/plugin/plugin123")
	assert.NoError(t, err)
	assert.Equal(t, "unix", net)
	assert.Equal(t, "/tmp/host-sock/plugin123", host, "container→host socket path")

	_, plug, err := tr.HostToPlugin("unix", "/tmp/host-sock/broker456")
	assert.NoError(t, err)
	assert.Equal(t, "/run/ctxloom/plugin/broker456", plug, "host→container socket path")

	_, outside, err := tr.PluginToHost("unix", "/var/run/other.sock")
	assert.NoError(t, err)
	assert.Equal(t, "/var/run/other.sock", outside, "paths outside the mount are unchanged")
}

// TestInContainer_EnvMarkers: the dev-container env markers trip detection (the
// filesystem markers are host-dependent and covered by the seam tests below).
func TestInContainer_EnvMarkers(t *testing.T) {
	t.Setenv("REMOTE_CONTAINERS", "true")
	assert.True(t, InContainer(), "REMOTE_CONTAINERS marks an in-container process")
}

// TestInContainerFrom_Markers drives the seam-injected detection core across
// every marker class WITHOUT touching the real /proc, sentinel files, or env
// (CI itself runs in containers; the hostile-env suite junks the env). Each
// hit is named for `container check`.
func TestInContainerFrom_Markers(t *testing.T) {
	none := func(string) error { return os.ErrNotExist }
	noFile := func(string) ([]byte, error) { return nil, os.ErrNotExist }
	noEnv := func(string) string { return "" }

	tests := []struct {
		name     string
		stat     func(string) error
		readFile func(string) ([]byte, error)
		getenv   func(string) string
		want     []string
	}{
		{"no markers → outside", none, noFile, noEnv, nil},
		{"docker sentinel", func(p string) error {
			if p == "/.dockerenv" {
				return nil
			}
			return os.ErrNotExist
		}, noFile, noEnv, []string{"/.dockerenv"}},
		{"podman sentinel", func(p string) error {
			if p == "/run/.containerenv" {
				return nil
			}
			return os.ErrNotExist
		}, noFile, noEnv, []string{"/run/.containerenv"}},
		{"DEVCONTAINER env", none, noFile, func(e string) string {
			if e == "DEVCONTAINER" {
				return "true"
			}
			return ""
		}, []string{"$DEVCONTAINER"}},
		{"kubernetes env", none, noFile, func(e string) string {
			if e == "KUBERNETES_SERVICE_HOST" {
				return "10.0.0.1"
			}
			return ""
		}, []string{"$KUBERNETES_SERVICE_HOST"}},
		{"cgroup v1 docker", none, func(string) ([]byte, error) {
			return []byte("12:cpuset:/docker/abc123"), nil
		}, noEnv, []string{"cgroup:docker"}},
		{"cgroup v2 bare → no marker", none, func(string) ([]byte, error) {
			return []byte("0::/"), nil
		}, noEnv, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, inContainerFrom(tt.stat, tt.readFile, tt.getenv))
		})
	}
}
