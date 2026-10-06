//go:build docker_integration

package isolation

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/testsupport/dockergate"
	"github.com/ctxloom/ctxloom/internal/testsupport/sourcedir"
)

// The three views of one directory TestCrossing_ThreePaths proves: the host
// names it H, the controller /ctl/work, the child /agent/work.
const (
	crossingCtlView   = "/ctl/work"
	crossingChildView = "/agent/work"
	// crossingRoleEnv runs this test as the CONTROLLER, inside its container;
	// crossingHostEnv carries H as the host names it.
	crossingRoleEnv = "CTXLOOM_TEST_CROSSING_ROLE"
	crossingHostEnv = "CTXLOOM_TEST_CROSSING_HOST"
	// crossingImage is the controller's image: the docker CLI over alpine.
	crossingImage = "ctxloom-crossing-controller:test"
)

// agentWorkPolicy is the test-only child placement policy: the controller's
// /ctl/work is mounted at /agent/work in the child; nothing else is placed.
type agentWorkPolicy struct{}

func (agentWorkPolicy) toContainer(host string) (string, error) {
	if host == crossingCtlView {
		return crossingChildView, nil
	}
	return "", errors.New("agentWorkPolicy places only " + crossingCtlView)
}

// TestCrossing_ThreePaths: a controller that runs in a container, with a host
// directory H mounted at /ctl/work and the daemon's socket — and NO /tmp
// shared at the same path — launches a child that its placement policy gives
// the same directory at /agent/work. The daemon must be handed H (the
// controller's mount reversed), the child must work in /agent/work, a file
// the child writes must be the file the host and the controller see, and the
// child's path for it must come back to the controller's.
//
// The outer test names every source it hands the daemon through its OWN
// layer, so it holds wherever this process runs: on the daemon's host, or in
// a CI job container that shares nothing at the same path.
func TestCrossing_ThreePaths(t *testing.T) {
	if os.Getenv(crossingRoleEnv) == "controller" {
		crossingController(t)
		return
	}
	dockergate.RequireRuntime(t, (Docker{}).Available(), "the three-path crossing test")
	outer, _ := newDockerRuntime(runtimeReachable)
	require.NoError(t, outer.identified(), "this process's own layer must be known to name its paths to the daemon")
	primary := outer.primary()

	hostH, ctlH := daemonVisibleDir(t, primary, "xl-crossing-h-")
	binHost, binDir := daemonVisibleDir(t, primary, "xl-crossing-bin-")
	buildCrossingController(t, binDir)
	socket := daemonSocket(t, primary)

	harp := "crossing-" + randToken()
	name := "xl-crossing-ctl-" + randToken()
	args := []string{"run", "--rm", "--name", name,
		"--label", labelHarp + "=" + harp, "-e", sessions.EnvHarp + "=" + harp,
		"-e", crossingRoleEnv + "=controller", "-e", crossingHostEnv + "=" + hostH,
		"--mount", "type=bind,source=" + hostH + ",target=" + crossingCtlView,
		"--mount", "type=bind,source=" + socket + ",target=/var/run/docker.sock",
		"--mount", "type=bind,source=" + binHost + ",target=/xl,readonly",
		crossingImage, "/xl/isolation.test", "-test.run", "^TestCrossing_ThreePaths$", "-test.v", "-test.count=1"}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	require.NoError(t, err, "the controller container:\n%s", out)
	require.Contains(t, string(out), "--- PASS: TestCrossing_ThreePaths", "the controller ran the cell rather than skipping it:\n%s", out)

	got, err := os.ReadFile(filepath.Join(ctlH, "out.txt"))
	require.NoError(t, err, "the child's file is the host's file")
	assert.Equal(t, "crossed\n", string(got))
}

// crossingController is the cell's controller, inside its container: it must
// identify itself on the daemon by its harp label, and launch the child.
func crossingController(t *testing.T) {
	hostH := os.Getenv(crossingHostEnv)
	require.NotEmpty(t, hostH)
	d, _ := newDockerRuntime(runtimeReachable)
	require.NoError(t, settleSelf(d), "the controller identifies its own container")
	require.NotNil(t, d.self)
	d.pathMap = agentWorkPolicy{}

	root, err := relocateRoot(d, crossingCtlView, "")
	require.NoError(t, err)
	assert.Equal(t, crossingChildView, root.root.Engine, "the child's view is the policy's, reached by ToChild")

	spec := RunSpec{
		Image: "alpine:latest", Name: "xl-crossing-child-" + randToken(),
		WorkDir: root.root.Engine, Mounts: []mount{root.mount},
		Command: []string{"sh", "-c", "echo crossed > out.txt"},
	}
	args, err := d.RunArgs(spec)
	require.NoError(t, err)
	joined := strings.Join(args, " ")
	assert.Contains(t, joined, "source="+hostH+",target="+crossingChildView, "the daemon is handed H, the host's name")
	assert.Contains(t, joined, "-w "+crossingChildView)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	require.NoError(t, err, "the child:\n%s", out)

	got, err := os.ReadFile(filepath.Join(crossingCtlView, "out.txt"))
	require.NoError(t, err, "the controller reads the child's file at its own path")
	assert.Equal(t, "crossed\n", string(got))

	c, err := crossingOver(d, root.mount)
	require.NoError(t, err)
	child, host, err := c.ToChild(crossingCtlView + "/out.txt")
	require.NoError(t, err)
	assert.Equal(t, crossingChildView+"/out.txt", child)
	assert.Equal(t, filepath.Join(hostH, "out.txt"), host)
	back, err := c.FromChild(crossingChildView + "/out.txt")
	require.NoError(t, err)
	assert.Equal(t, crossingCtlView+"/out.txt", back)
}

// daemonVisibleDir creates a directory this process can write and the daemon
// can name under primary's fixture root (FixtureRoot), returning both names.
// Created world-writable: under a rootful daemon the controller and its child
// run as root.
func daemonVisibleDir(t *testing.T, primary Layer, prefix string) (host, ctl string) {
	t.Helper()
	root, err := FixtureRoot(primary, dockergate.FixtureCandidates()...)
	if err != nil {
		dockergate.SkipCapability(t, "no directory this process writes is one the daemon can name: "+err.Error())
	}
	dir, err := os.MkdirTemp(root, prefix)
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	require.NoError(t, os.Chmod(dir, 0o777))
	host, err = primary.Reverse(dir)
	require.NoError(t, err)
	return host, dir
}

// daemonSocket is the daemon's socket as the HOST names it.
func daemonSocket(t *testing.T, primary Layer) string {
	t.Helper()
	sock := "/var/run/docker.sock"
	if h, ok := strings.CutPrefix(os.Getenv("DOCKER_HOST"), "unix://"); ok {
		sock = h
	}
	host, err := primary.Reverse(sock)
	if err != nil {
		dockergate.SkipCapability(t, "the daemon socket is not a mount the daemon can name: "+err.Error())
	}
	return host
}

// buildCrossingController builds this package's test binary statically into
// dir and the controller image the cell runs it in.
func buildCrossingController(t *testing.T, dir string) {
	t.Helper()
	root, err := sourcedir.RepoRoot()
	require.NoError(t, err)
	build := exec.Command("go", "test", "-c", "-tags", "docker_integration", "-o", filepath.Join(dir, "isolation.test"), "./internal/adapters/isolation")
	build.Dir = root
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+runtime.GOARCH, "GOWORK=off")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build the controller's test binary: %v\n%s", err, out)
	}
	ctxDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(ctxDir, "Dockerfile"), []byte("FROM alpine:latest\nRUN apk add --no-cache docker-cli\n"), 0o644))
	if out, err := exec.Command("docker", "build", "-q", "-t", crossingImage, ctxDir).CombinedOutput(); err != nil {
		t.Fatalf("build the controller image: %v\n%s", err, out)
	}
}
