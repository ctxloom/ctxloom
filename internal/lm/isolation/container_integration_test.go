//go:build docker_integration

// Package isolation's docker-gated transport proof. Build-tagged so the normal
// `just test` never compiles it (the gate stays green without docker); run with:
//
//	GOWORK=off just test-pkg ./internal/lm/isolation/... -tags docker_integration -run Container
//
// It builds a minimal image (static ctxloom on alpine), spawns the `mock` plugin
// IN the container via the container policy, connects through go-plugin over the
// AddrTranslator-mapped unix socket, asserts Info() returns the mock backend's
// metadata (this precedes any Execute, so no engine CLI or auth is needed), then
// Kills and asserts the container is gone. This proves the RunnerFunc +
// AddrTranslator plugin-in-container transport works against docker end-to-end.
package isolation

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const integrationImage = "ctxloom-iso-itest:latest"

// TestContainerPolicy_TransportEndToEnd is the P1 transport gate.
func TestContainerPolicy_TransportEndToEnd(t *testing.T) {
	if !(Docker{}).Available() {
		t.Skip("docker unavailable; skipping the container transport integration test")
	}
	buildIntegrationImage(t)

	rt := SelectRuntime("docker")
	require.Equal(t, "docker", rt.Name(), "docker must be selected when available")
	pol := NewContainer(rt, integrationImage)

	// A real project dir to bind-mount at its identical path inside the container.
	projectDir := t.TempDir()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	ws, err := pol.PrepareWorkspace(ctx, projectDir, "itest")
	require.NoError(t, err, "PrepareWorkspace must succeed with docker + the built image")
	require.Equal(t, projectDir, ws.Dir(), "workspace dir is the identical-path project dir")

	client, err := pol.SpawnClient("mock", "", 0, ws)
	require.NoError(t, err, "SpawnClient must bring up the plugin in a container and connect")

	// Info() crosses the container boundary over the translated unix socket.
	info, err := client.Info(ctx)
	require.NoError(t, err, "Info() must round-trip over the plugin-in-container gRPC transport")
	assert.Equal(t, "mock", info.GetName(), "backend name from inside the container")
	assert.Equal(t, "1.0.0", info.GetVersion(), "backend version from inside the container")
	t.Logf("Info() over container transport: name=%q version=%q modes=%v",
		info.GetName(), info.GetVersion(), info.GetSupportedModes())

	// Teardown: Kill force-removes the container; Cleanup removes host scratch.
	client.Kill()
	require.NoError(t, ws.Cleanup())

	// The container is gone (no leftover ctxloom-iso-itest-* after Kill).
	assert.Eventually(t, func() bool {
		out, _ := exec.CommandContext(ctx, "docker", "ps", "-a",
			"--filter", "name=ctxloom-iso-itest-", "--format", "{{.Names}}").Output()
		return strings.TrimSpace(string(out)) == ""
	}, 15*time.Second, 250*time.Millisecond, "the container must be force-removed on Kill")
}

// TestContainerPolicy_TransportLoopbackTCP is the macOS/Windows-path proof, run on
// Linux. Forcing PLUGIN_LISTEN_TCP=1 makes ctxloom take the exact transport a
// non-Linux host takes: the in-container plugin listens on TCP, the run publishes
// that port to host loopback (-p 127.0.0.1:P:P), and the host client dials
// loopback. On Linux this rides the same kernel, but it exercises the identical
// publish + loopback-dial + AddrTranslator-no-op-on-tcp wiring the Docker Desktop
// VM boundary needs. It asserts the LIVE container actually publishes a 127.0.0.1
// TCP port (proving TCP, not the unix socket, is in use) and that Info() round-
// trips over it.
func TestContainerPolicy_TransportLoopbackTCP(t *testing.T) {
	if !(Docker{}).Available() {
		t.Skip("docker unavailable; skipping the TCP-loopback transport integration test")
	}
	// Force the loopback (TCP) transport ctxloom otherwise takes only off Linux.
	t.Setenv("PLUGIN_LISTEN_TCP", "1")
	buildIntegrationImage(t)

	rt := SelectRuntime("docker")
	require.Equal(t, "docker", rt.Name())
	pol := NewContainer(rt, integrationImage)

	projectDir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	ws, err := pol.PrepareWorkspace(ctx, projectDir, "itest-tcp")
	require.NoError(t, err, "PrepareWorkspace must succeed")

	client, err := pol.SpawnClient("mock", "", 0, ws)
	require.NoError(t, err, "SpawnClient must bring up the plugin over the TCP-loopback transport")

	// The live container must publish a 127.0.0.1 TCP port — the discriminating
	// proof that the loopback transport (not the bind-mounted unix socket) is in
	// use. SpawnClient has already completed the go-plugin handshake, so the
	// container is up with its port published here.
	require.Eventually(t, func() bool {
		out, _ := exec.CommandContext(ctx, "docker", "ps",
			"--filter", "name=ctxloom-iso-itest-tcp-", "--format", "{{.Ports}}").Output()
		return strings.Contains(string(out), "127.0.0.1:")
	}, 10*time.Second, 200*time.Millisecond, "the container must publish a loopback TCP port")

	info, err := client.Info(ctx)
	require.NoError(t, err, "Info() must round-trip over the TCP-loopback transport")
	assert.Equal(t, "mock", info.GetName(), "backend name over the TCP-loopback transport")
	t.Logf("Info() over TCP-loopback container transport: name=%q version=%q", info.GetName(), info.GetVersion())

	client.Kill()
	require.NoError(t, ws.Cleanup())

	assert.Eventually(t, func() bool {
		out, _ := exec.CommandContext(ctx, "docker", "ps", "-a",
			"--filter", "name=ctxloom-iso-itest-tcp-", "--format", "{{.Names}}").Output()
		return strings.TrimSpace(string(out)) == ""
	}, 15*time.Second, 250*time.Millisecond, "the container must be force-removed on Kill")
}

// buildIntegrationImage builds the minimal image (static linux ctxloom on alpine)
// used by the transport test. It rebuilds the binary + image each run so the test
// exercises the current tree.
func buildIntegrationImage(t *testing.T) {
	t.Helper()
	dir := t.TempDir()

	// Build the static linux/amd64 ctxloom into the docker build context.
	bin := filepath.Join(dir, "ctxloom")
	build := exec.Command("go", "build", "-o", bin, "github.com/ctxloom/ctxloom/cmd/ctxloom")
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64", "GOWORK=off")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build static ctxloom: %v\n%s", err, out)
	}

	dockerfile := "FROM alpine:latest\nCOPY ctxloom /usr/local/bin/ctxloom\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(dockerfile), 0o644))

	img := exec.Command("docker", "build", "-t", integrationImage, dir)
	if out, err := img.CombinedOutput(); err != nil {
		t.Fatalf("docker build minimal image: %v\n%s", err, out)
	}
}
