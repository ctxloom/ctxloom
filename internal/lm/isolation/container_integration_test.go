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
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport/dockergate"
)

const integrationImage = "ctxloom-iso-itest:latest"

// TestContainerPolicy_TransportEndToEnd is the P1 transport gate.
func TestContainerPolicy_TransportEndToEnd(t *testing.T) {
	dockergate.RequireRuntime(t, (Docker{}).Available(), "the container transport integration test")
	buildIntegrationImage(t)

	rt := ProbeRuntime("docker")
	require.Equal(t, "docker", rt.Name(), "docker must be selected when available")
	pol := NewContainerFor(rt, "mock").WithImage(integrationImage)

	// A real project dir to bind-mount at its identical path inside the container.
	projectDir := t.TempDir()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	ws, err := pol.PrepareWorkspace(ctx, projectDir, "itest")
	require.NoError(t, err, "PrepareWorkspace must succeed with docker + the built image")
	require.Equal(t, projectDir, ws.Dir(), "workspace dir is the identical-path project dir")

	client, err := pol.SpawnClient("mock", "", 0, ws, nil)
	require.NoError(t, err, "SpawnClient must bring up the plugin in a container and connect")
	// Kill on cleanup so a later require failure can't leak a running container
	// (Kill is idempotent: the explicit Kill below + this one both just `rm -f`).
	t.Cleanup(func() { client.Kill() })

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

// buildIntegrationImage builds the minimal image (static linux ctxloom on alpine)
// used by the transport test. It rebuilds the binary + image each run so the test
// exercises the current tree.
func buildIntegrationImage(t *testing.T) {
	t.Helper()
	dir := t.TempDir()

	// Build the static linux ctxloom into the docker build context, targeting the
	// HOST arch: `FROM alpine:latest` resolves the host's arch, so a hardcoded
	// GOARCH=amd64 binary would `exec format error` on an arm64 host.
	bin := filepath.Join(dir, "ctxloom")
	build := exec.Command("go", "build", "-o", bin, "github.com/ctxloom/ctxloom/cmd/ctxloom")
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+runtime.GOARCH, "GOWORK=off")
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
