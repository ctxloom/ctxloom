//go:build docker_integration

// queer-shrug Phase 1's docker-gated proof that the DELEGATED container spawn
// now goes docker-direct — `ctxloom llm host <backend>` launched through the
// REAL isolation.Container.StartRunner (no go-plugin handshake, no plugin
// listener) — and still round-trips a real turn while opening NO network port.
//
// Unlike container_bus_docker_integration_test.go (whose dockerBusSpawner
// hand-rolls `docker run … llm serve mock` + the plugin magic cookie), this
// spawner routes StartEngine through the production seam
// (isolation.StarterForWorkspace → Container.StartRunner), so it exercises
// buildRunnerSpec, the docker-direct launch, and the preserved session-state
// mounts end to end. Every assertion reads a delivered PAYLOAD or a live
// container fact — never just an exit status.
//
//	just test-docker-integration
//	GOWORK=off just test-pkg ./internal/agentcoord/coord/... -tags docker_integration -run CoordContainerDirect
package coord

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/lm/isolation"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/testsupport/dockergate"
)

// directAgentName is the one agent directBusSpawner resolves.
const directAgentName = "direct-container-worker"

// directBusSpawner is the docker_integration Spawner whose StartEngine launches
// the child through the REAL production isolation starter — the docker-direct
// Container.StartRunner (`ctxloom llm host mock`), NOT a hand-rolled `docker
// run`. Everything else mirrors dockerBusSpawner.
type directBusSpawner struct {
	image      string
	projectDir string

	mu         sync.Mutex
	containers []string
	cleanups   []func()
}

func (s *directBusSpawner) Resolve(_ context.Context, agentName string) (*SpawnPlan, error) {
	if agentName != directAgentName {
		return nil, assertUnknownAgent(agentName)
	}
	perm := agent.PermissionBypass
	return &SpawnPlan{
		AgentName:   agentName,
		Backend:     "mock",
		Label:       "fast",
		Runtime:     "container",
		Perm:        perm,
		Ladder:      presetLadder(perm),
		ViaStartRun: true,
	}, nil
}

func assertUnknownAgent(name string) error {
	return &unknownAgentError{name}
}

type unknownAgentError struct{ name string }

func (e *unknownAgentError) Error() string { return "directBusSpawner: unknown agent " + e.name }

func (s *directBusSpawner) AssignSession(projectDir, backend string) (string, error) {
	entry, err := operations.AssignSession(projectDir, backend)
	if err != nil {
		return "", err
	}
	return entry.HarpName, nil
}

func (s *directBusSpawner) Launch(context.Context, *SpawnPlan, string, string, map[string]string, map[string]string) (*operations.AgentChatLaunch, error) {
	return nil, &unknownAgentError{"legacy Launch is unused (this agent always routes ViaStartRun)"}
}

// StartEngine is the whole point: build the REAL Container policy, prepare its
// workspace, and launch the runner via isolation.StarterForWorkspace →
// Container.StartRunner (docker-direct `ctxloom llm host mock`). The session
// harp on env drives the session-state mounts (transcript survival).
func (s *directBusSpawner) StartEngine(ctx context.Context, plan *SpawnPlan, env, runnerEnv map[string]string) (*EngineSpawn, error) {
	rt := isolation.SelectRuntime("docker")
	// Container auth keys on the ENGINE, resolved PER CALL from the plan
	// (containerAuthBackend — the same Backend field StarterForWorkspace below
	// already reads). The harness image is unrelated to the engine, so it is
	// named separately via WithImage.
	pol := isolation.NewContainerFor(rt, containerAuthBackend(plan)).WithImage(s.image).WithSessionState(isolation.SessionStateFromEnv(env))
	ws, err := pol.PrepareWorkspace(ctx, s.projectDir, plan.AgentName)
	if err != nil {
		return nil, err
	}
	starter := isolation.StarterForWorkspace(pol, ws, plan.Backend, plan.Label, 0, runnerEnv)
	handle, err := starter(ctx)
	if err != nil {
		_ = ws.Cleanup()
		return nil, err
	}
	kill := sync.OnceFunc(func() {
		handle.Kill()
		_ = ws.Cleanup()
	})
	s.mu.Lock()
	s.containers = append(s.containers, handle.Name)
	s.cleanups = append(s.cleanups, kill)
	s.mu.Unlock()
	return &EngineSpawn{WorkDir: ws.Dir(), Env: env, Model: "mock", MCPServers: plan.MCPServers, Kill: kill}, nil
}

func (s *directBusSpawner) ResumeContext(_ context.Context, plan *SpawnPlan, _ string) string {
	return plan.Context
}
func (s *directBusSpawner) MarkSessionEnded(string) {}

func (s *directBusSpawner) containerNames() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.containers...)
}

// TestCoordContainerDirect_NoPluginNoPort is queer-shrug Phase 1's headline
// docker-gated proof:
//
//  1. a delegated container child spawned through the REAL docker-direct
//     starter completes a real turn — its mock echo of the composed prompt
//     reaches the parent's live tap (payload, not an exit code);
//  2. the host-side canonical transcript.jsonl survives the container and
//     carries the turn payload (the §6.4 session-state-mount guard);
//  3. the child's container publishes NO port and exposes NO port
//     (docker inspect), and holds NO TCP LISTEN socket (/proc/net/tcp*) — the
//     direct mauve-state negative: no in-container plugin listener exists.
func TestCoordContainerDirect_NoPluginNoPort(t *testing.T) {
	dockergate.RequireRuntime(t, (isolation.Docker{}).Available(), "the docker-direct delegated-spawn integration test")
	resetStrictness(t)
	// NO ANTHROPIC_API_KEY is set on purpose: this run's engine is mock, and
	// mock's container-auth resolver (resolveMockContainerAuth) resolves
	// unconditionally because mock authenticates against no vendor. Needing a
	// borrowed Anthropic key here would mean auth was being keyed on something
	// other than the engine.

	// Build BEFORE isolating cwd (ProjectDir chdirs outside the module).
	image := buildBusIntegrationImage(t)
	projectDir := testsupport.ProjectDir(t) // isolated HOME + cwd; never the real ~/.ctxloom

	sp := &directBusSpawner{image: image, projectDir: projectDir}
	c, err := New(Options{ProjectDir: projectDir, ProjectKey: "direct-itest", Spawner: sp})
	require.NoError(t, err)
	require.NoError(t, c.Serve())
	t.Cleanup(c.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		for _, name := range sp.containerNames() {
			logs, _ := exec.Command("docker", "logs", name).CombinedOutput()
			t.Logf("container %s logs:\n%s", name, logs)
		}
	})

	owner := ownerIdentity()
	seedPayload := "DIRECT-SEED-" + randID("", 6)
	out, err := c.AgentRun(ctx, owner, directAgentName, seedPayload, "", "")
	require.NoError(t, err)
	require.NotEmpty(t, out.Harp)
	require.Equal(t, "container", out.Runtime)
	childHarp := out.Harp

	// Subscribe to the live tap before the container has even been run.
	var feed *operations.SessionFeed
	require.Eventually(t, func() bool {
		f, ferr := operations.WatchSessionFeed(ctx, operations.SessionFeedRequest{Harp: childHarp})
		if ferr != nil || f.Source != "live" {
			return false
		}
		feed = f
		return true
	}, 10*time.Second, 50*time.Millisecond, "the live tap must resolve for the just-enqueued container child")
	tail := startFeedTail(feed)

	// (1) A real container, launched docker-direct.
	require.Eventually(t, func() bool { return len(sp.containerNames()) > 0 && sp.containerNames()[0] != "" }, 10*time.Second, 50*time.Millisecond)
	containerName := sp.containerNames()[0]
	require.Eventually(t, func() bool {
		psOut, _ := exec.Command("docker", "ps", "--filter", "name="+containerName, "--format", "{{.Names}}").Output()
		return strings.TrimSpace(string(psOut)) == containerName
	}, 30*time.Second, 250*time.Millisecond, "the child's container must actually be running")

	// (1) PAYLOAD: the mock echo of the composed prompt crosses back out — proof
	// the docker-direct runner dialed home over Transport 2, completed StartRun,
	// and ran a real turn through EngineHost/Mock.Chat, with NO go-plugin
	// handshake anywhere.
	wantSeedEcho := "mock chat: " + seedPayload
	if snap, ok := waitForFeedText(tail, wantSeedEcho, 90*time.Second); !ok {
		t.Fatalf("the docker-direct container's mock engine never echoed the seed prompt; want substring %q, saw:\n%s", wantSeedEcho, snap)
	}

	// (3) The direct mauve-state negative: NO published/exposed port, NO TCP
	// LISTEN socket. Asserted on the LIVE container (it is up from the echo).
	assertNoPublishedOrExposedPorts(t, containerName)
	assertNoTCPListenSocket(t, containerName)

	// (2) The §6.4 guard: the host canonical transcript.jsonl survives the
	// container (the session-state mount) and carries the turn payload.
	transcriptPath, err := paths.HarpCanonicalTranscriptPath(childHarp)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		b, rerr := os.ReadFile(transcriptPath)
		return rerr == nil && strings.Contains(string(b), seedPayload)
	}, 30*time.Second, 250*time.Millisecond,
		"the host-side canonical transcript.jsonl (%s) must exist and contain the turn payload (the session-state mount preserved the transcript)", transcriptPath)

	// Teardown removes the container.
	c.Close()
	assert.Eventually(t, func() bool {
		psOut, _ := exec.Command("docker", "ps", "-a", "--filter", "name="+containerName, "--format", "{{.Names}}").Output()
		return strings.TrimSpace(string(psOut)) == ""
	}, 15*time.Second, 250*time.Millisecond, "closing the coordinator must force-remove the docker-direct child's container")
}

// assertNoPublishedOrExposedPorts fails if the container publishes or exposes
// ANY port — the docker-direct runner opens no plugin listener, so there is
// nothing to publish (the fork's -p 127.0.0.1:P:P is gone on this path).
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

	ports, err := exec.Command("docker", "inspect", "--format", "{{json .NetworkSettings.Ports}}", name).Output()
	require.NoError(t, err)
	p := strings.TrimSpace(string(ports))
	assert.NotContains(t, p, "HostPort", "no host port binding may appear in NetworkSettings.Ports (%s)", p)
}

// assertNoTCPListenSocket fails if any process inside the container holds a TCP
// LISTEN socket (/proc/net/tcp{,6} state 0A) — the direct proof that the runner
// binds no plugin port at all (the mauve-state class cannot recur on this
// path). The runner-local MCP surface is a UNIX socket, never TCP, so a
// correct docker-direct runner shows zero TCP listeners.
func assertNoTCPListenSocket(t *testing.T, name string) {
	t.Helper()
	out, err := exec.Command("docker", "exec", name, "sh", "-c", "cat /proc/net/tcp /proc/net/tcp6 2>/dev/null").Output()
	require.NoError(t, err, "must read the container's /proc/net/tcp*")
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		// Column 3 (st) == 0A is TCP_LISTEN. The header row's field[3] is "st"
		// (skipped by the equality check).
		if fields[3] == "0A" {
			t.Fatalf("the docker-direct runner container holds a TCP LISTEN socket (mauve-state hole): %q", line)
		}
	}
}
