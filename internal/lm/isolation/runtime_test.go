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

// TestDockerRootless_OmitsUser: rootless docker maps container-root to the host
// user, so the argv must NOT carry --user (the bind-mounted socket is already
// host-owned). The identical-path project mount, socket mount, workdir, image, and
// in-container command must all render.
func TestDockerRootless_OmitsUser(t *testing.T) {
	args := Docker{rootless: true}.RunArgs(sampleSpec())
	joined := strings.Join(args, " ")

	assert.Equal(t, []string{"run", "--rm", "--name", "ctxloom-iso-m-abc"}, args[:4], "run head")
	assert.NotContains(t, joined, "--user", "rootless docker maps root→host user; no --user")
	assert.Contains(t, joined, "-v /home/u/proj:/home/u/proj", "identical-path project mount")
	assert.Contains(t, joined, "-v /tmp/sock:/run/ctxloom/plugin", "socket-dir mount")
	assert.Contains(t, joined, "-e HOME=/root", "fresh HOME")
	assert.Contains(t, joined, "-w /home/u/proj", "workdir")
	// The image precedes the in-container command as the final tokens.
	assert.Equal(t,
		[]string{"ctxloom-agent:latest", "/usr/local/bin/ctxloom", "llm", "serve", "mock"},
		args[len(args)-5:], "image then in-container argv")
}

// TestDockerRootful_AddsUser: a rootful daemon needs --user <uid>:<gid> so the
// plugin's bind-mounted socket lands host-user-owned (connectable).
func TestDockerRootful_AddsUser(t *testing.T) {
	args := Docker{rootless: false}.RunArgs(sampleSpec())
	assert.Contains(t, strings.Join(args, " "),
		fmt.Sprintf("--user %d:%d", os.Getuid(), os.Getgid()),
		"rootful docker runs as the host user")
}

// TestPodman_DockerCompatibleArgv: podman's argv matches docker's (minus --user;
// rootless by default). Same run head + rendered tail.
func TestPodman_DockerCompatibleArgv(t *testing.T) {
	args := Podman{}.RunArgs(sampleSpec())
	joined := strings.Join(args, " ")
	assert.Equal(t, []string{"run", "--rm", "--name", "ctxloom-iso-m-abc"}, args[:4])
	assert.NotContains(t, joined, "--user")
	assert.Contains(t, joined, "-v /home/u/proj:/home/u/proj")
	assert.Equal(t, []string{"rm", "-f", "c1"}, Podman{}.RemoveArgs("c1"))
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
// filesystem markers are host-dependent and covered by the runtime path).
func TestInContainer_EnvMarkers(t *testing.T) {
	t.Setenv("REMOTE_CONTAINERS", "true")
	assert.True(t, InContainer(), "REMOTE_CONTAINERS marks an in-container process")
}
