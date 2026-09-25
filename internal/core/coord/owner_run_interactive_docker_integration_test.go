//go:build docker_integration

// The GATE for a container INTERACTIVE turn (Part 4.1, slice 13): the runner
// is the container's FOREGROUND process, attached to the pty the originator
// holds — no keepalive to exec into, no handoff file, no listener. Every
// assertion reads a delivered PAYLOAD (the engine's echo of typed input over
// the pty) or a live fact (the container's command, the process table, the
// persist dir) — never an exit status alone.
//
//	just test-docker-integration
//	GOWORK=off just test-pkg ./internal/core/coord/... -tags docker_integration -run InteractiveContainer

package coord_test

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/attach"
	"github.com/ctxloom/ctxloom/internal/adapters/coordgrpc"
	"github.com/ctxloom/ctxloom/internal/adapters/fsstore"
	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/testsupport/dockergate"
)

// dockerInteractiveStarter is an OwnedRunStarter that attaches a REAL
// container through the production primitive `ctxloom run` uses for a
// container INTERACTIVE launch: Container.InteractiveRunner (`docker run -i
// -t … ctxloom runner mock`, the trio on the run process's env) started on
// a pty this test holds (adapters/attach). It records the session for the
// typed round trip and the container's name for the live assertions.
type dockerInteractiveStarter struct {
	image      string
	projectDir string
	harp       string

	pol isolation.Container
	ws  isolation.Workspace

	mu       sync.Mutex
	session  *attach.Session
	name     string
	cleanups []func()
	// out is everything the pty carried from the moment the attach started
	// — the docker CLI's own words when the container never came up.
	out lockedBuffer
}

// prepare stands the cell up BEFORE StartOwnedRun, as the host's Resolve
// does, so the listen requirement it names rides the launch the coordinator
// honours before the runner starts.
func (s *dockerInteractiveStarter) prepare(ctx context.Context, t *testing.T, l launch.Launch) launch.Launch {
	t.Helper()
	rt := isolation.ProbeRuntime("docker")
	stateEnv := map[string]string{"CTXLOOM_SESSION_HARP": s.harp}
	s.pol = isolation.NewContainerFor(rt, ownerRunBackend).WithImage(s.image).WithSessionState(isolation.SessionStateFromEnv(stateEnv))
	ws, err := s.pol.PrepareWorkspace(ctx, s.projectDir, s.harp)
	require.NoError(t, err)
	s.ws = ws
	l.Cell.Listen = isolation.WorkspaceListen(ws)
	return l
}

func (s *dockerInteractiveStarter) start(ctx context.Context, spawnEnv map[string]string) (coord.OwnedRunner, error) {
	pol, ws := s.pol, s.ws
	// The mock's interactive echo loop is what holds the turn open and
	// reflects typed input; it reads the knob off the runner's environment.
	env := map[string]string{"CTXLOOM_MOCK_ECHO_STDIN": "1"}
	for k, v := range spawnEnv {
		env[k] = v
	}
	cmd, name, err := pol.InteractiveRunner(ctx, ownerRunBackend, ws, env)
	if err != nil {
		_ = ws.Cleanup()
		return coord.OwnedRunner{}, err
	}
	sess, err := attach.Start(context.Background(), cmd, name, func(runExited <-chan struct{}) { pol.Remove(name, runExited) })
	if err != nil {
		_ = ws.Cleanup()
		return coord.OwnedRunner{}, err
	}
	go func() { _, _ = io.Copy(&s.out, sess.Master()) }()
	kill := sync.OnceFunc(func() {
		sess.Kill()
		_ = ws.Cleanup()
	})
	s.mu.Lock()
	s.session, s.name = sess, name
	s.cleanups = append(s.cleanups, kill)
	s.mu.Unlock()
	return coord.OwnedRunner{Kill: kill, Wait: sess.ExitErr, ContainerName: name}, nil
}

// TestCoordOwnerRun_InteractiveContainerIsTheForegroundRunner is the slice-13
// GATE for the container interactive turn:
//
//  1. PAYLOAD: a line typed on the pty the originator holds reaches the
//     mock's echo loop INSIDE the container and its echo comes back over the
//     same pty — the full chain, pty → docker -it → runner → engine → back;
//  2. the container's command IS the runner (`ctxloom runner mock`): the
//     foreground process, not a keepalive;
//  3. NO exec-into: the process table holds no `docker exec` for it;
//  4. NO handoff: nothing under the session's persist/ carries a run-start.
func TestCoordOwnerRun_InteractiveContainerIsTheForegroundRunner(t *testing.T) {
	dockergate.RequireRuntime(t, (isolation.Docker{}).Available(), "the container interactive turn integration test")
	coord.ResetStrictness(t)

	image := buildBusIntegrationImage(t)
	projectDir := testsupport.ProjectDir(t)

	entry, err := operations.OpenedApp(nil, operations.Handed{Engines: engines.Registry(), SessionClaims: fsstore.SessionClaims}).AssignSession(context.Background(), projectDir, "mock")
	require.NoError(t, err)
	ownerHarp := entry.HarpName

	starter := &dockerInteractiveStarter{image: image, projectDir: projectDir, harp: ownerHarp}
	coord.TeeHome(t)
	c, err := coord.New(coord.Options{ProjectDir: projectDir, ProjectID: "owner-interactive-itest", Spawner: coord.NewFakeSpawner(nil, nil), OwnerHarp: coord.OwnerIdentity().Harp})
	require.NoError(t, err)
	require.NoError(t, coordgrpc.Serve(c))
	t.Cleanup(c.Close)
	t.Cleanup(func() {
		starter.mu.Lock()
		defer starter.mu.Unlock()
		for _, k := range starter.cleanups {
			k()
		}
		if t.Failed() && starter.name != "" {
			t.Logf("pty output for %s:\n%s", starter.name, starter.out.String())
			logs, _ := exec.Command("docker", "logs", starter.name).CombinedOutput()
			t.Logf("container %s logs:\n%s", starter.name, logs)
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Second)
	defer cancel()

	token, err := c.RegisterSessionOwner(ownerHarp)
	require.NoError(t, err)
	owner, ok := c.Identify(token)
	require.True(t, ok)

	outcome, err := c.StartOwnedRun(ctx, owner, coord.OwnedRunOf(starter.prepare(ctx, t, containerOwnerLaunch(ownerHarp, engine.Interactive)), false), starter.start, "")
	require.NoError(t, err)
	require.Equal(t, ownerHarp, outcome.Harp)

	starter.mu.Lock()
	sess, name := starter.session, starter.name
	starter.mu.Unlock()
	require.NotNil(t, sess)

	// (1) The typed round trip over the pty.
	out := &starter.out
	sentinel := "INTERACTIVE-CTR-" + coord.RandID("", 6)
	_, err = io.WriteString(sess.Master(), sentinel+"\n")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return strings.Contains(out.String(), "mock echo: "+sentinel) }, 120*time.Second, 100*time.Millisecond,
		"the container's mock never echoed the typed line back over the pty; saw:\n%s", out.String())

	// (2) The container's command is the runner, in the foreground.
	cmdOut, err := exec.Command("docker", "inspect", "--format", "{{join .Config.Cmd \" \"}}", name).Output()
	require.NoError(t, err)
	assert.Contains(t, strings.TrimSpace(string(cmdOut)), "runner mock", "the container's foreground process is `ctxloom runner mock`")
	assert.NotContains(t, string(cmdOut), "llm host", "no keepalive")

	// (3) No exec-into anywhere in the process table.
	ps, _ := exec.Command("ps", "-eo", "args").Output()
	for _, line := range strings.Split(string(ps), "\n") {
		if strings.Contains(line, name) && strings.Contains(line, " exec ") {
			t.Errorf("an exec into the runner container is in the process table: %s", line)
		}
	}

	// (4) No handoff file under the session's persist dir.
	persist, err := paths.HarpPersistDir(ownerHarp)
	require.NoError(t, err)
	entries, _ := os.ReadDir(persist)
	for _, e := range entries {
		assert.False(t, strings.Contains(e.Name(), "runstart"), "a run-start handoff landed under persist/: %s", filepath.Join(persist, e.Name()))
	}
}

// lockedBuffer is a goroutine-safe io.Writer for the pty copier.
type lockedBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
