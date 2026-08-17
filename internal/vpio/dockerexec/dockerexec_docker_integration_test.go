//go:build docker_integration

// queer-shrug Phase 2a-A's docker-gated proof that a top-level INTERACTIVE
// container run works docker-direct: the StartRunner keepalive container (`llm
// host mock`, no plugin listener) plus an interactive turn driven over
// `docker exec -it … ctxloom llm turn mock` behind the real dockerexec
// vpio.Launcher. Every assertion reads a delivered PAYLOAD (the engine's echo
// of typed input) or a live container fact (docker inspect / /proc/net/tcp*) —
// never just an exit status.
//
//	just test-docker-integration
//	GOWORK=off just test-pkg ./internal/vpio/dockerexec/... -tags docker_integration -run DockerExecInteractive
package dockerexec

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"

	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/lm/isolation"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/testsupport/dockergate"
	"github.com/ctxloom/ctxloom/internal/vpio"
)

const dockerExecIntegrationImage = "ctxloom-dockerexec-itest:latest"

// TestDockerExecInteractive_TurnEchoesNoListener is the headline proof:
//
//  1. an interactive turn over docker-exec -it accepts typed stdin and the mock
//     engine echoes it back through the full chain (host pty → exec → llm turn →
//     mock → back) — a PAYLOAD, not an exit code;
//  2. a host-side terminal resize propagates to the in-container turn (the mock
//     reflects the winsize it saw);
//  3. the turn exits clean with the engine's own code;
//  4. the keepalive container publishes NO port, exposes NO port, and holds NO
//     TCP LISTEN socket — the direct mauve-state negative for this path.
func TestDockerExecInteractive_TurnEchoesNoListener(t *testing.T) {
	dockergate.RequireRuntime(t, (isolation.Docker{}).Available(), "the docker-exec interactive integration test")
	// NO ANTHROPIC_API_KEY is set on purpose: this run's engine is mock, and
	// mock's container-auth resolver (resolveMockContainerAuth) resolves
	// unconditionally because mock authenticates against no vendor. Needing a
	// borrowed Anthropic key here would mean auth was being keyed on something
	// other than the engine.

	image := buildDockerExecIntegrationImage(t)
	projectDir := testsupport.ProjectDir(t) // isolated HOME + cwd; never the real ~/.ctxloom

	harp := "dockerexec-itest-" + randSuffix()
	env := map[string]string{"CTXLOOM_SESSION_HARP": harp, "CTXLOOM_PROJECT_ID": "dockerexec-itest"}

	rt := isolation.ProbeRuntime("docker")
	pol := isolation.NewContainerFor(rt, "mock").WithImage(image).WithSessionState(isolation.SessionStateFromEnv(env))

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	ws, err := pol.PrepareWorkspace(ctx, projectDir, "dockerexec-worker")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ws.Cleanup() })

	// Launch the keepalive container: `ctxloom llm host mock`, no reach-back
	// trio → standup+block, no plugin listener. Real production primitive.
	handle, err := isolation.StarterForWorkspace(pol, ws, "mock", "", 0, map[string]string{"CTXLOOM_SESSION_HARP": harp})(ctx)
	require.NoError(t, err)
	t.Cleanup(handle.Kill)

	// Handoff RunStart by file in the bind-mounted persist dir (the interactive
	// mock-echo flag rides in Options.Env, reaching the turn as req.Env).
	req := &pb.RunStart{
		Prompt: &pb.Fragment{Content: "interactive session"},
		Options: &pb.RunOptions{
			WorkDir: ws.Dir(),
			Mode:    pb.ExecutionMode_INTERACTIVE,
			Env:     map[string]string{"CTXLOOM_MOCK_ECHO_STDIN": "1", "CTXLOOM_MOCK_EXIT_CODE": "0"},
		},
	}
	writeHandoff(t, harp, req)
	startPath := path.Join(operations.ContainerPersistDirForPolicy(pol, harp), "runstart.json")

	// Wait for the keepalive container to actually be up before exec-ing.
	require.Eventually(t, func() bool {
		out, _ := exec.Command("docker", "ps", "--filter", "name="+handle.Name, "--format", "{{.Names}}").Output()
		return strings.TrimSpace(string(out)) == handle.Name
	}, 60*time.Second, 250*time.Millisecond, "the keepalive container must be running")

	// (4) The mauve-state negative, asserted on the LIVE keepalive container.
	assertNoPublishedOrExposedPorts(t, handle.Name)
	assertNoTCPListenSocket(t, handle.Name)

	// Drive the interactive turn over the REAL dockerexec Launcher.
	stdinR, stdinW := io.Pipe()
	var out syncBuf
	launcher := NewLauncher(rt, handle.Name, TurnSpec{Backend: "mock", StartPath: startPath})
	sess, err := launcher.Start(ctx, vpio.ProcessSpec{Stdin: stdinR, Stdout: &out})
	require.NoError(t, err)

	// (1) PAYLOAD: a typed line echoes back through the whole chain (host pty →
	// exec → llm turn → mock → back). Wait for it before resizing so the turn is
	// definitely up and attached.
	_, _ = stdinW.Write([]byte("hello-exec\n"))
	require.Eventually(t, func() bool { return strings.Contains(out.String(), "mock echo: hello-exec") }, 60*time.Second, 100*time.Millisecond,
		"the typed stdin must echo back through host pty → exec → llm turn → mock; saw:\n%s", out.String())

	// (2) PAYLOAD: a host-side resize propagates all the way to the in-container
	// turn (host pty.Setsize → docker CLI SIGWINCH → daemon → exec TTY →
	// llm turn's SIGWINCH → mock). Send it BETWEEN two lines so the daemon has
	// time to forward it; the second echo reflects the winsize the turn saw.
	sess.Resize(40, 120)
	require.Eventually(t, func() bool {
		_, _ = stdinW.Write([]byte("check\n"))
		return strings.Contains(out.String(), "mock winsize: 40x120")
	}, 30*time.Second, 500*time.Millisecond,
		"a host-side resize must propagate to the in-container turn; saw:\n%s", out.String())

	// (3) Clean exit with the engine's own code (the "quit" sentinel ends the loop).
	_, _ = stdinW.Write([]byte("quit\n"))
	status, werr := sess.Wait()
	require.NoError(t, werr, "a clean interactive turn is not a transport error")
	assert.Equal(t, int32(0), status.Code, "the turn exits with the engine's code")
	_ = stdinW.Close()
}

func buildDockerExecIntegrationImage(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "ctxloom")
	build := exec.Command("go", "build", "-o", bin, "github.com/ctxloom/ctxloom/cmd/ctxloom")
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+runtime.GOARCH, "GOWORK=off")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build static ctxloom: %v\n%s", err, out)
	}
	// WORKDIR /work (never bare "/", which trips resolveCellPath — see the bus
	// integration test's note); the ctxloom user's HOME is /home/ctxloom, the
	// container persist mount target's root.
	dockerfile := "FROM alpine:latest\nRUN adduser -D -h /home/ctxloom ctxloom || true\nWORKDIR /work\nCOPY ctxloom /usr/local/bin/ctxloom\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(dockerfile), 0o644))
	img := exec.Command("docker", "build", "-t", dockerExecIntegrationImage, dir)
	if out, err := img.CombinedOutput(); err != nil {
		t.Fatalf("docker build dockerexec integration image: %v\n%s", err, out)
	}
	return dockerExecIntegrationImage
}

func writeHandoff(t *testing.T, harp string, req *pb.RunStart) {
	t.Helper()
	persist, err := paths.HarpPersistDir(harp)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(persist, 0o755))
	data, err := protojson.Marshal(req)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(persist, "runstart.json"), data, 0o600))
}

func randSuffix() string {
	return strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")
}

// assertNoPublishedOrExposedPorts fails if the container publishes or exposes
// ANY port — the docker-exec path opens no plugin listener.
func assertNoPublishedOrExposedPorts(t *testing.T, name string) {
	t.Helper()
	bindings, err := exec.Command("docker", "inspect", "--format", "{{json .HostConfig.PortBindings}}", name).Output()
	require.NoError(t, err)
	b := strings.TrimSpace(string(bindings))
	assert.True(t, b == "null" || b == "{}", "the container must publish NO port (HostConfig.PortBindings = %s)", b)

	exposed, err := exec.Command("docker", "inspect", "--format", "{{json .Config.ExposedPorts}}", name).Output()
	require.NoError(t, err)
	e := strings.TrimSpace(string(exposed))
	assert.True(t, e == "null" || e == "{}", "the container must EXPOSE no port (Config.ExposedPorts = %s)", e)
}

// assertNoTCPListenSocket fails if any process inside the container holds a TCP
// LISTEN socket (/proc/net/tcp{,6} state 0A) — the direct proof the turn/runner
// binds no plugin port at all (the mauve-state class cannot recur here).
func assertNoTCPListenSocket(t *testing.T, name string) {
	t.Helper()
	out, err := exec.Command("docker", "exec", name, "sh", "-c", "cat /proc/net/tcp /proc/net/tcp6 2>/dev/null").Output()
	require.NoError(t, err, "must read the container's /proc/net/tcp*")
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		if fields[3] == "0A" { // TCP_LISTEN
			t.Fatalf("the docker-exec keepalive container holds a TCP LISTEN socket (mauve-state hole): %q", line)
		}
	}
}
