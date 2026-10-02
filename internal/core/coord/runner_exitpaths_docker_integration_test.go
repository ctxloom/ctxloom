//go:build docker_integration

// Every way a container runner ends must END its container: the runner
// process exits (or is removed), --rm or the teardown's remove takes the
// container, and the coordinator — when one is left — sees the run end.
//
//	GOWORK=off just test-pkg ./internal/core/coord/ -tags docker_integration -run RunnerExitPaths
package coord_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/coordgrpc"
	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/adapters/spawn"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/testsupport/dockergate"
)

// exitSpawner is directBusSpawner on a NAMED runtime, launching on a
// container axis (so the runner is handed the container-reachable listener,
// not the coordinator's own loopback), with the run's identity (so the
// runner's reach-back names its run) and its composed first turn on the
// launch.
type exitSpawner struct {
	directBusSpawner
	runtime string
	killed  atomic.Bool
}

func (s *exitSpawner) Resolve(ctx context.Context, agentName string) (*coord.SpawnPlan, error) {
	plan, err := s.directBusSpawner.Resolve(ctx, agentName)
	if err != nil {
		return nil, err
	}
	plan.Runtime = containerAxes(s.runtime).Runtime
	return plan, nil
}

func (s *exitSpawner) ResolveLaunch(ctx context.Context, plan *coord.SpawnPlan, start coord.SpawnStart) (coord.Resolved, error) {
	env := sessions.HookEnv(start.Identity)
	cenv, err := preparedContainer(ctx, s.runtime, coord.ContainerStoryBackend(plan), s.image, s.projectDir, isolation.SessionStateFromEnv(env))
	if err != nil {
		return coord.Resolved{}, err
	}
	s.mu.Lock()
	if s.cells == nil {
		s.cells = map[string]preparedContainerCell{}
	}
	s.cells[start.Identity.Harp] = preparedContainerCell{env: cenv, backend: plan.Backend, label: plan.Label}
	s.mu.Unlock()
	l := coord.OwnerLaunch(start.Identity.Harp, plan.Backend, plan.Label, "mock", cenv.Placement().Paths.Paths().ProjectRoot.Host, agent.PermissionBypass)
	l.Identity = start.Identity
	l.Prompt = start.Prompt
	l.Cell.Env = env
	l.Cell.Listen = cenv.Listen()
	l.Axes.Runtime = containerAxes(s.runtime).Runtime
	l.MCP = sessions.Endpoint{URL: "http://127.0.0.1:0/mcp", Credential: "child-itest-bearer"}
	plan.Launch = l
	return coord.Resolved{Launch: l}, nil
}

// Start launches through the production spawn.StartRunner (so the runner env
// it builds — reach-back plus the operator's owner-loss override — is what
// reaches the container) and records the teardown it hands the coordinator:
// the spawn's Kill is the ONE door a live owner releases a run's cell through
// (runtime remove, then the workspace's Cleanup), so a live-owner end path
// that never reached it would leak every ephemeral the cell holds.
func (s *exitSpawner) Start(ctx context.Context, l launch.Launch, reach sessions.Endpoint) (*coord.EngineSpawn, error) {
	s.mu.Lock()
	cell := s.cells[l.Identity.Harp]
	s.mu.Unlock()
	h, err := spawn.StartRunner(ctx, cellStarter{cell: cell}, l, reach)
	if err != nil {
		_ = cell.env.Cleanup()
		return nil, err
	}
	kill := sync.OnceFunc(func() {
		s.killed.Store(true)
		h.Kill()
		_ = cell.env.Cleanup()
	})
	s.mu.Lock()
	s.containers = append(s.containers, h.Name)
	s.cleanups = append(s.cleanups, kill)
	s.mu.Unlock()
	return &coord.EngineSpawn{Kill: kill}, nil
}

// cellStarter is spawn.Runtimes over one prepared container cell.
type cellStarter struct{ cell preparedContainerCell }

func (c cellStarter) Start(ctx context.Context, _ launch.Launch, env map[string]string) (coord.RunnerHandle, error) {
	handle, err := c.cell.env.Start(ctx, isolation.RunnerRequest{Engine: c.cell.backend, Label: c.cell.label, Env: env})
	if err != nil {
		return coord.RunnerHandle{}, err
	}
	return coord.RunnerHandle{Name: handle.Name, Kill: handle.Kill}, nil
}

// exitRun is one live container child: its coordinator, harp and container.
type exitRun struct {
	sp        *exitSpawner
	c         *coord.Coordinator
	harp      string
	container string
	bin       string
}

// startExitRun stands a coordinator up over its own project, delegates one
// container child and waits until that child's runner has dialled home and
// finished its first turn (the run is idle): the runner is ATTACHED, so what
// follows is a loss of something that was really there.
//
// ownerLossWindow, when non-zero, is the operator's override the runner is
// launched under (sessions.EnvRunnerOwnerLossWindow) — set AFTER the test's
// environment is isolated, which clears every CTXLOOM_* variable.
func startExitRun(t *testing.T, runtimeName, image string, ownerLossWindow time.Duration) exitRun {
	t.Helper()
	coord.ResetStrictness(t)
	projectDir := testsupport.ProjectDir(t)
	if ownerLossWindow > 0 {
		t.Setenv(sessions.EnvRunnerOwnerLossWindow, ownerLossWindow.String())
	}
	sp := &exitSpawner{directBusSpawner: directBusSpawner{image: image, projectDir: projectDir}, runtime: runtimeName}
	coord.TeeHome(t)
	var logs syncBuffer
	c, err := coord.New(coord.Options{ProjectDir: projectDir, ProjectID: "exit-itest", Spawner: sp, OwnerHarp: coord.OwnerIdentity().Harp,
		Reporter: report.SinkFunc(func(f report.Finding) { fmt.Fprintf(&logs, "coordinator: %v\n", f) })})
	require.NoError(t, err)
	require.NoError(t, coordgrpc.Serve(c))
	t.Cleanup(c.Close)

	bin := isolation.ProbeRuntime(runtimeName).Binary()
	// The runner's log is followed from the moment the container exists: a
	// container --rm has already taken has no log left to ask for.
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("coordinator findings and runner container log:\n%s", logs.String())
		}
		for _, name := range sp.containerNames() {
			_ = exec.Command(bin, "rm", "-f", name).Run()
		}
	})

	out, err := c.AgentRun(context.Background(), coord.OwnerIdentity(), directAgentName, "exit-path seed", "", "")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return len(sp.containerNames()) > 0 }, 30*time.Second, 50*time.Millisecond)
	name := sp.containerNames()[0]
	require.Eventually(t, func() bool { return len(dockergate.ContainersNamed(t, bin, name)) > 0 }, 60*time.Second, 50*time.Millisecond,
		"the child's container must be created")
	follow := exec.Command(bin, "logs", "-f", name)
	follow.Stdout, follow.Stderr = &logs, &logs
	require.NoError(t, follow.Start())
	t.Cleanup(func() { _ = follow.Process.Kill(); _ = follow.Wait() })
	// Done waiting as soon as the answer is known either way: a runner that
	// cannot reach its coordinator gives up after the owner-loss window and
	// takes its container with it, and waiting past that proves nothing.
	waitFor(120*time.Second, func() bool {
		st := rosterState(c, out.Harp)
		return st == coord.StateIdle || st == coord.StateEnded || len(dockergate.ContainersNamed(t, bin, name)) == 0
	})
	if st := rosterState(c, out.Harp); st != coord.StateIdle {
		t.Fatalf("the container child's runner never dialled home and finished its first turn: run %s, container %v",
			st, dockergate.ContainersNamed(t, bin, name))
	}
	require.NotEmpty(t, dockergate.ContainersNamed(t, bin, name), "the child's container is up")
	return exitRun{sp: sp, c: c, harp: out.Harp, container: name, bin: bin}
}

func waitFor(within time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return cond()
}

func rosterState(c *coord.Coordinator, harp string) string {
	for _, e := range c.Roster(coord.OwnerIdentity()) {
		if e.Harp == harp {
			return e.State
		}
	}
	return ""
}

// requireContainerGone waits up to within for the run's container to be gone
// in ANY state (`ps -a`): an exited container that --rm did not take is as
// much a leak as a running one.
func (r exitRun) requireContainerGone(t *testing.T, within time.Duration, why string) {
	t.Helper()
	require.Eventually(t, func() bool {
		out, err := exec.Command(r.bin, "ps", "-a", "--filter", "name="+r.container, "--format", "{{.Names}}").Output()
		return err == nil && strings.TrimSpace(string(out)) == ""
	}, within, 250*time.Millisecond, "%s: container %s still listed by `%s ps -a`", why, r.container, r.bin)
}

func (r exitRun) requireRunEnded(t *testing.T, why string) {
	t.Helper()
	require.Eventually(t, func() bool { return rosterState(r.c, r.harp) == coord.StateEnded }, 60*time.Second, 100*time.Millisecond,
		"%s: the coordinator must see the run end", why)
}

// requireCellReleased: the owner reached the spawn's teardown for this run.
func (r exitRun) requireCellReleased(t *testing.T, why string) {
	t.Helper()
	require.Eventually(t, r.sp.killed.Load, 30*time.Second, 50*time.Millisecond,
		"%s: the coordinator must release the run's cell through the spawn's Kill", why)
}

// persistentMembers is the run's Persist members (persistentMembers).
func (r exitRun) persistentMembers(t *testing.T) sessionMembers {
	t.Helper()
	return persistentMembers(t, r.harp)
}

// requirePersistentKept: every Persist member present before the end is
// still there after it (lostPersistent). Cleanup releases ephemerals only.
func (r exitRun) requirePersistentKept(t *testing.T, before sessionMembers, why string) {
	t.Helper()
	require.NotEmpty(t, before.Paths, "the run left no persistent session state to check")
	t.Logf("%s: %d persistent session member(s) checked: %v", why, len(before.Paths), before.Paths)
	require.Empty(t, lostPersistent(before), "%s: persistent session member(s) removed", why)
}

// runtimeExec starts argv inside the run's container, detached: the argv kills
// the container's own runner, and an attached exec races that death — podman
// exits 255 ("container has already been removed") when --rm takes the
// container out from under it. Whether the kill landed is what the caller's
// assertions that follow establish.
func (r exitRun) runtimeExec(t *testing.T, args ...string) {
	t.Helper()
	out, err := exec.Command(r.bin, append([]string{"exec", "-d", r.container}, args...)...).CombinedOutput()
	require.NoError(t, err, "%s exec %v: %s", r.bin, args, out)
}

// syncBuffer is a bytes.Buffer safe for a follower process to write while
// a failing test reads it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// exitPathOwnerLossWindow is the owner-loss window the owner-loss leg runs
// its runner under, through sessions.EnvRunnerOwnerLossWindow.
const exitPathOwnerLossWindow = 20 * time.Second

// removalSlack bounds how long a container takes to go once its runner has
// exited or its remove was issued: process teardown plus --rm.
const removalSlack = 30 * time.Second

func TestRunnerExitPaths(t *testing.T) {
	for _, rtc := range []struct {
		name      string
		available func() bool
	}{
		{"docker", isolation.Docker{}.Available},
		{"podman", isolation.Podman{}.Available},
	} {
		t.Run(rtc.name, func(t *testing.T) {
			dockergate.RequireNamedRuntime(t, rtc.name, rtc.available(), "the runner exit-path tests")
			withPodmanSelected(t, rtc.name)
			// Rootless podman keeps its images under the invoking HOME, and
			// every subtest below isolates a HOME of its own, so the image
			// built here would be missing from the store each launch reads.
			// One store for the whole leg: this binary's sandbox home.
			t.Setenv("XDG_DATA_HOME", filepath.Join(os.Getenv("HOME"), ".local", "share"))
			image := buildIntegrationImageFor(t, rtc.name)

			// OWNER LOSS: the coordinator dies without tearing its child down
			// (SIGKILL, OOM, a closed terminal). Nothing on the host removes the
			// container; the runner must notice, exit, and let --rm take it.
			t.Run("owner-loss", func(t *testing.T) {
				// The operator's override, set where ctxloom runs: proof it
				// reaches a container runner, and a test that does not wait
				// out the two-minute default.
				r := startExitRun(t, rtc.name, image, exitPathOwnerLossWindow)
				persistent := r.persistentMembers(t)
				coord.CrashCoordinator(r.c)
				r.requireContainerGone(t, exitPathOwnerLossWindow+removalSlack,
					"a runner whose coordinator is gone must exit once its owner-loss window runs out")
				r.requirePersistentKept(t, persistent, "owner loss")
			})

			// AGENT STOP: the coordinator's own teardown door (terminateRun ->
			// the spawn's Kill -> remove by name).
			t.Run("agent-stop", func(t *testing.T) {
				r := startExitRun(t, rtc.name, image, 0)
				persistent := r.persistentMembers(t)
				_, err := r.c.AgentStop(coord.OwnerIdentity(), r.harp, "exit-path test", 0)
				require.NoError(t, err)
				r.requireRunEnded(t, "agent_stop")
				r.requireContainerGone(t, removalSlack, "agent_stop")
				r.requireCellReleased(t, "agent_stop")
				r.requirePersistentKept(t, persistent, "agent_stop")
			})

			// RUNTIME STOP: SIGTERM through the init to the runner, which
			// returns from Main and exits; --rm takes the container.
			t.Run("runtime-stop", func(t *testing.T) {
				r := startExitRun(t, rtc.name, image, 0)
				persistent := r.persistentMembers(t)
				out, err := exec.Command(r.bin, "stop", "-t", "20", r.container).CombinedOutput()
				require.NoError(t, err, "%s stop: %s", r.bin, out)
				r.requireContainerGone(t, removalSlack, "runtime stop (SIGTERM)")
				r.requireRunEnded(t, "runtime stop (SIGTERM)")
				r.requireCellReleased(t, "runtime stop (SIGTERM)")
				r.requirePersistentKept(t, persistent, "runtime stop (SIGTERM)")
			})

			// RUNTIME KILL: SIGKILL; nothing in the container runs, --rm still
			// takes it, and the coordinator synthesizes the loss from the drop.
			t.Run("runtime-kill", func(t *testing.T) {
				r := startExitRun(t, rtc.name, image, 0)
				persistent := r.persistentMembers(t)
				out, err := exec.Command(r.bin, "kill", r.container).CombinedOutput()
				require.NoError(t, err, "%s kill: %s", r.bin, out)
				r.requireContainerGone(t, removalSlack, "runtime kill (SIGKILL)")
				r.requireRunEnded(t, "runtime kill (SIGKILL)")
				r.requireCellReleased(t, "runtime kill (SIGKILL)")
				r.requirePersistentKept(t, persistent, "runtime kill (SIGKILL)")
			})

			// RUNNER CRASH: the runner process dies abruptly — SIGQUIT is a Go
			// process's dump-and-exit(2), the same process death an unrecovered
			// panic is, with no deferred teardown run — while ANOTHER process
			// is still alive in the container. PID 1 (the runtime's --init)
			// exits with its child and the kernel takes the rest, so no
			// surviving process can hold the container open.
			t.Run("runner-crash", func(t *testing.T) {
				r := startExitRun(t, rtc.name, image, 0)
				persistent := r.persistentMembers(t)
				out, err := exec.Command(r.bin, "exec", "-d", r.container, "sleep", "600").CombinedOutput()
				require.NoError(t, err, "%s exec -d sleep: %s", r.bin, out)
				r.runtimeExec(t, "sh", "-c", "kill -QUIT $(pidof ctxloom)")
				r.requireContainerGone(t, removalSlack, "runner crash with a surviving process")
				r.requireRunEnded(t, "runner crash")
				r.requireCellReleased(t, "runner crash")
				r.requirePersistentKept(t, persistent, "runner crash")
			})
		})
	}
}
