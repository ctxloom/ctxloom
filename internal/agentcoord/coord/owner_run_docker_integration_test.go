//go:build docker_integration

// queer-shrug Phase 2a-B's docker-gated proof that a TOP-LEVEL structured or
// oneshot container run rides Transport 2 / EngineHost — an OWNER-OWNED run
// (Coordinator.StartOwnedRun) whose in-container `ctxloom llm host` runner
// dials home and drives the engine, watched host-side via WatchRuns — instead
// of a go-plugin client, and opens NO network port inside the container.
//
// Unlike container_direct_docker_integration_test.go (the DELEGATED path via
// AgentRun/StartEngine), this exercises the TOP-LEVEL owner-owned path the
// `ctxloom run --structured` / `--one-shot` container arm wires to (run_owned.go):
// StartOwnedRun mints the parent-less run and issues StartRun over the same
// RunnerChannel; the container is launched through the production
// isolation.Container.StartRunner (the identical primitive the host uses).
// Every assertion reads a delivered PAYLOAD or a live container fact.
//
//	just test-docker-integration
//	GOWORK=off just test-pkg ./internal/agentcoord/coord/... -tags docker_integration -run CoordOwnerRun
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

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
	"github.com/ctxloom/ctxloom/internal/lm/isolation"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/testsupport/dockergate"
)

// dockerOwnerRunStarter builds an OwnedRunStarter that launches a REAL container
// through the production isolation starter (Container.StartRunner → docker-
// direct `ctxloom llm host mock` WITH the per-run trio StartOwnedRun mints), the
// same primitive run.go's container arm uses. It records the container name for
// the zero-listener assertions and the workspace for teardown.
type dockerOwnerRunStarter struct {
	image      string
	projectDir string
	harp       string

	mu         sync.Mutex
	containers []string
	cleanups   []func()
}

func (s *dockerOwnerRunStarter) start(ctx context.Context, spawnEnv map[string]string) (func(), error) {
	rt := isolation.ProbeRuntime("docker")
	// The session harp drives the session-state mounts (transcript survival),
	// exactly as the host resolves it into the runner spawn env.
	stateEnv := map[string]string{"CTXLOOM_SESSION_HARP": s.harp}
	// One name for the engine this starter runs: it keys the container AUTH
	// (NewContainerFor) and names the runner backend (StartRunner below), so
	// the two cannot drift into asking for one engine's credentials while
	// launching another's.
	const backend = "mock"
	pol := isolation.NewContainerFor(rt, backend).WithImage(s.image).WithSessionState(isolation.SessionStateFromEnv(stateEnv))
	ws, err := pol.PrepareWorkspace(ctx, s.projectDir, s.harp)
	if err != nil {
		return nil, err
	}
	handle, err := pol.StartRunner(ctx, backend, "fast", 0, ws, spawnEnv)
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
	return kill, nil
}

func (s *dockerOwnerRunStarter) containerNames() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.containers...)
}

// TestCoordOwnerRun_StructuredAndOneshot_NoPluginNoPort is Phase 2a-B's headline
// docker-gated proof:
//
//  1. STRUCTURED: a top-level owner-owned container run completes a first turn
//     (StartRun input) and a SECOND turn (SendOwnedRunTurn) — both mock echoes
//     reach the host over WatchRuns (payloads, not exit codes), proving the
//     container dialed home over Transport 2 and drove the engine via EngineHost
//     with NO go-plugin client anywhere;
//  2. the run is PARENT-LESS (ParentRunID "") and owner-owned (the owner's harp
//     as role) — the §5.B2 collision decision, live;
//  3. the container publishes NO port, exposes NO port (docker inspect), and
//     holds NO TCP LISTEN socket (/proc/net/tcp*) — the direct mauve-state
//     negatives on the top-level path;
//  4. the host-side canonical transcript.jsonl survives the container and
//     carries the turn payload (the session-state-mount / silent-no-op guard).
func TestCoordOwnerRun_StructuredAndOneshot_NoPluginNoPort(t *testing.T) {
	dockergate.RequireRuntime(t, (isolation.Docker{}).Available(), "the owner-owned top-level container integration test")
	resetStrictness(t)
	// NO ANTHROPIC_API_KEY is set on purpose: this run's engine is mock, and
	// mock's container-auth resolver (resolveMockContainerAuth) resolves
	// unconditionally because mock authenticates against no vendor. Needing a
	// borrowed Anthropic key here would mean auth was being keyed on something
	// other than the engine.

	image := buildBusIntegrationImage(t)
	projectDir := testsupport.ProjectDir(t)

	// The owner's session harp (its address + the transcript-mount key), minted
	// through the same accounting the host uses.
	entry, err := operations.AssignSession(context.Background(), projectDir, "mock")
	require.NoError(t, err)
	ownerHarp := entry.HarpName

	starter := &dockerOwnerRunStarter{image: image, projectDir: projectDir, harp: ownerHarp}
	c, err := New(Options{ProjectDir: projectDir, ProjectKey: "owner-itest", Spawner: newFakeSpawner(nil, nil)})
	require.NoError(t, err)
	require.NoError(t, c.Serve())
	t.Cleanup(c.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Second)
	defer cancel()

	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		for _, name := range starter.containerNames() {
			logs, _ := exec.Command("docker", "logs", name).CombinedOutput()
			t.Logf("container %s logs:\n%s", name, logs)
		}
	})

	token, err := c.RegisterSessionOwner(ownerHarp)
	require.NoError(t, err)
	owner, ok := c.Identify(token)
	require.True(t, ok)

	// Watch BEFORE the run so the first turn's deltas are not missed.
	_, events, wcancel, _ := c.WatchRuns(nil)
	defer wcancel()
	collector := newDeltaCollector(events)
	defer collector.stop()

	seed := "OWNER-STRUCT-" + randID("", 6)
	outcome, err := c.StartOwnedRun(ctx, owner, OwnerRunSpec{
		Harp:       ownerHarp,
		Backend:    "mock",
		Label:      "fast",
		Model:      "mock",
		WorkDir:    "/work",
		Permission: agent.PermissionBypass,
	}, starter.start, seed)
	require.NoError(t, err)
	require.Equal(t, ownerHarp, outcome.Harp)

	// (2) Parent-less, owner-owned.
	var info *agentcoordpb.ListRunsResult_RunInfo
	for _, r := range c.ListRuns(true, "").GetRuns() {
		if r.GetRunId() == outcome.RunID {
			info = r
		}
	}
	require.NotNil(t, info)
	assert.Equal(t, "", info.GetParentRunId(), "a top-level owner-owned run is parent-less")

	// (1) PAYLOAD: the first-turn mock echo crosses back over Transport 2.
	wantFirst := "mock chat: " + seed
	require.True(t, collector.await(outcome.RunID, wantFirst, 120*time.Second),
		"the container's mock engine never echoed the first turn over Transport 2 (want %q); saw:\n%s", wantFirst, collector.snapshot(outcome.RunID))

	// (1) A SECOND turn via SendOwnedRunTurn (the mailbox → pushMail → EngineHost
	// delivery-by-state path) also round-trips. Follow-up turns ride the
	// PeerMessage delivery the plan (§5.B) specifies, so the engine sees the
	// text inside the coordinator-delivery framing (frameCoordinatorMessage);
	// the payload substring reaching the engine's echo is the round-trip proof.
	second := "OWNER-STRUCT2-" + randID("", 6)
	require.NoError(t, c.SendOwnedRunTurn(outcome.RunID, second))
	require.True(t, collector.await(outcome.RunID, second, 90*time.Second),
		"the second turn (SendOwnedRunTurn) payload never echoed over Transport 2 (want substring %q); saw:\n%s", second, collector.snapshot(outcome.RunID))

	// (3) The mauve-state negatives, on the LIVE container.
	require.Eventually(t, func() bool { return len(starter.containerNames()) > 0 && starter.containerNames()[0] != "" }, 10*time.Second, 50*time.Millisecond)
	containerName := starter.containerNames()[0]
	assertNoPublishedOrExposedPorts(t, containerName)
	assertNoTCPListenSocket(t, containerName)

	// (4) The host canonical transcript.jsonl survives the container and carries
	// the turn payload — the session-state mount + silent-no-op guard.
	transcriptPath, err := paths.HarpCanonicalTranscriptPath(ownerHarp)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		b, rerr := os.ReadFile(transcriptPath)
		return rerr == nil && strings.Contains(string(b), seed)
	}, 30*time.Second, 250*time.Millisecond,
		"the host-side canonical transcript.jsonl (%s) must exist and contain the turn payload", transcriptPath)

	// Teardown removes the container.
	c.Close()
	assert.Eventually(t, func() bool {
		psOut, _ := exec.Command("docker", "ps", "-a", "--filter", "name="+containerName, "--format", "{{.Names}}").Output()
		return strings.TrimSpace(string(psOut)) == ""
	}, 15*time.Second, 250*time.Millisecond, "closing the coordinator must force-remove the owner-owned run's container")
}

// TestCoordOwnerRun_Oneshot_NoPluginNoPort is the --one-shot ONESHOT arm's
// docker-gated proof: an owner-owned run marked Oneshot delivers its single
// turn's answer over Transport 2 with the same zero-listener guarantee. The
// Oneshot flag changes only the HOST's wait mode (runOneshotViaCoord collects
// the FINAL text at the turn boundary); the coordinator/runner mechanism is
// identical, so this asserts the payload + the negatives.
func TestCoordOwnerRun_Oneshot_NoPluginNoPort(t *testing.T) {
	dockergate.RequireRuntime(t, (isolation.Docker{}).Available(), "the owner-owned oneshot container integration test")
	resetStrictness(t)
	// NO ANTHROPIC_API_KEY is set on purpose: this run's engine is mock, and
	// mock's container-auth resolver (resolveMockContainerAuth) resolves
	// unconditionally because mock authenticates against no vendor. Needing a
	// borrowed Anthropic key here would mean auth was being keyed on something
	// other than the engine.

	image := buildBusIntegrationImage(t)
	projectDir := testsupport.ProjectDir(t)

	entry, err := operations.AssignSession(context.Background(), projectDir, "mock")
	require.NoError(t, err)
	ownerHarp := entry.HarpName

	starter := &dockerOwnerRunStarter{image: image, projectDir: projectDir, harp: ownerHarp}
	c, err := New(Options{ProjectDir: projectDir, ProjectKey: "owner-oneshot-itest", Spawner: newFakeSpawner(nil, nil)})
	require.NoError(t, err)
	require.NoError(t, c.Serve())
	t.Cleanup(c.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Second)
	defer cancel()

	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		for _, name := range starter.containerNames() {
			logs, _ := exec.Command("docker", "logs", name).CombinedOutput()
			t.Logf("container %s logs:\n%s", name, logs)
		}
	})

	token, err := c.RegisterSessionOwner(ownerHarp)
	require.NoError(t, err)
	owner, ok := c.Identify(token)
	require.True(t, ok)

	_, events, wcancel, _ := c.WatchRuns(nil)
	defer wcancel()
	collector := newDeltaCollector(events)
	defer collector.stop()

	seed := "OWNER-ONESHOT-" + randID("", 6)
	outcome, err := c.StartOwnedRun(ctx, owner, OwnerRunSpec{
		Harp:       ownerHarp,
		Backend:    "mock",
		Label:      "fast",
		Model:      "mock",
		WorkDir:    "/work",
		Permission: agent.PermissionBypass,
		Oneshot:    true,
	}, starter.start, seed)
	require.NoError(t, err)

	want := "mock chat: " + seed
	require.True(t, collector.await(outcome.RunID, want, 120*time.Second),
		"the oneshot container's mock engine never echoed over Transport 2 (want %q); saw:\n%s", want, collector.snapshot(outcome.RunID))

	require.Eventually(t, func() bool { return len(starter.containerNames()) > 0 && starter.containerNames()[0] != "" }, 10*time.Second, 50*time.Millisecond)
	containerName := starter.containerNames()[0]
	assertNoPublishedOrExposedPorts(t, containerName)
	assertNoTCPListenSocket(t, containerName)

	transcriptPath, err := paths.HarpCanonicalTranscriptPath(ownerHarp)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		b, rerr := os.ReadFile(transcriptPath)
		return rerr == nil && strings.Contains(string(b), seed)
	}, 30*time.Second, 250*time.Millisecond,
		"the host-side canonical transcript.jsonl (%s) must exist and contain the oneshot turn payload", transcriptPath)
}

// deltaCollector accumulates FINAL-channel MessageDelta text per run from a live
// WatchRuns stream, so a test can await a payload substring that may have
// arrived before it asks.
type deltaCollector struct {
	mu     sync.Mutex
	byRun  map[string]*strings.Builder
	final  map[string]bool
	cancel chan struct{}
}

func newDeltaCollector(events <-chan *agentcoordpb.AgentEvent) *deltaCollector {
	dc := &deltaCollector{byRun: map[string]*strings.Builder{}, final: map[string]bool{}, cancel: make(chan struct{})}
	go func() {
		for {
			select {
			case <-dc.cancel:
				return
			case ev, ok := <-events:
				if !ok {
					return
				}
				dc.consume(ev)
			}
		}
	}()
	return dc
}

func (dc *deltaCollector) consume(ev *agentcoordpb.AgentEvent) {
	dc.mu.Lock()
	defer dc.mu.Unlock()
	switch p := ev.GetPayload().(type) {
	case *agentcoordpb.AgentEvent_MessageStarted:
		if p.MessageStarted.GetChannel() == agentcoordpb.MessageChannel_MESSAGE_CHANNEL_FINAL {
			dc.final[p.MessageStarted.GetMessageId()] = true
		}
	case *agentcoordpb.AgentEvent_MessageDelta:
		if !dc.final[p.MessageDelta.GetMessageId()] {
			return
		}
		b := dc.byRun[ev.GetRunId()]
		if b == nil {
			b = &strings.Builder{}
			dc.byRun[ev.GetRunId()] = b
		}
		b.WriteString(p.MessageDelta.GetText())
	}
}

func (dc *deltaCollector) snapshot(runID string) string {
	dc.mu.Lock()
	defer dc.mu.Unlock()
	if b := dc.byRun[runID]; b != nil {
		return b.String()
	}
	return ""
}

func (dc *deltaCollector) await(runID, want string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if strings.Contains(dc.snapshot(runID), want) {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return strings.Contains(dc.snapshot(runID), want)
}

func (dc *deltaCollector) stop() { close(dc.cancel) }
