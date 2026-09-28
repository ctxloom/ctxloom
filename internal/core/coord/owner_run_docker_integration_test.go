//go:build docker_integration

// queer-shrug Phase 2a-B's docker-gated proof that a TOP-LEVEL structured or
// oneshot container run rides Transport 2 / EngineHost — an OWNER-OWNED run
// (Coordinator.StartOwnedRun) whose in-container `ctxloom runner` dials home
// and drives the engine, watched host-side via WatchRuns, and opens NO network
// port inside the container.
//
// Unlike container_direct_docker_integration_test.go (the DELEGATED path via
// AgentRun/StartEngine), this exercises the TOP-LEVEL owner-owned path the
// `ctxloom run --structured` / `--one-shot` container arm wires to (run_owned.go):
// StartOwnedRun mints the parent-less run and issues StartRun over the same
// RunnerChannel; the container is launched through the production
// the container Environment's Start (the identical call the host uses).
// Every assertion reads a delivered PAYLOAD or a live container fact.
//
//	just test-docker-integration
//	GOWORK=off just test-pkg ./internal/core/coord/... -tags docker_integration -run CoordOwnerRun
package coord_test

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/engines"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/coordgrpc"
	"github.com/ctxloom/ctxloom/internal/adapters/fsstore"
	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/testsupport/dockergate"
)

// containerOwnerLaunch is the owner's launch as a container run resolves it:
// the container runtime axis (the runner dials the container-reachable
// listener) and a session endpoint for the runner to bind — the endpoint
// Resolve mints in production, here any free loopback port inside the
// container (nothing outside dials it; the runner's bind is what the launch
// exercises).
func containerOwnerLaunch(harp string, mode engine.Mode) launch.Launch {
	l := coord.OwnerLaunch(harp, "mock", "fast", "mock", "/work", agent.PermissionBypass)
	l.Mode = mode
	l.Axes.Runtime = launch.RuntimeRootless
	l.MCP = sessions.Endpoint{URL: "http://127.0.0.1:0/mcp", Credential: "owner-itest-bearer"}
	return l
}

// dockerOwnerRunStarter builds an OwnedRunStarter that launches a REAL container
// through the production Environment.Start (the container runner → docker-
// direct `ctxloom llm host mock` WITH the per-run trio StartOwnedRun mints), the
// same primitive run.go's container arm uses. It records the container name for
// the zero-listener assertions and the workspace for teardown.
type dockerOwnerRunStarter struct {
	image      string
	projectDir string
	harp       string

	env isolation.Environment

	mu         sync.Mutex
	containers []string
	cleanups   []func()
}

// ownerRunBackend is the one engine these starters run: it keys the container
// AUTH (NewContainerFor) and names the runner backend (StartRunner), so the two
// cannot drift into asking for one engine's credentials while launching
// another's.
const ownerRunBackend = "mock"

// prepare stands the cell up BEFORE StartOwnedRun, as the host's Resolve
// does, so the listen requirement it names rides the launch the coordinator
// honours before the runner starts.
func (s *dockerOwnerRunStarter) prepare(ctx context.Context, t *testing.T, l launch.Launch) launch.Launch {
	t.Helper()
	env, err := preparedContainer(ctx, "docker", ownerRunBackend, s.image, s.projectDir, isolation.SessionState{Harp: s.harp})
	require.NoError(t, err)
	s.env = env
	l.Cell.Listen = env.Listen()
	return l
}

func (s *dockerOwnerRunStarter) start(ctx context.Context, spawnEnv map[string]string) (coord.OwnedRunner, error) {
	cell := s.env
	handle, err := cell.Start(ctx, isolation.RunnerRequest{Engine: ownerRunBackend, Label: "fast", Env: spawnEnv})
	if err != nil {
		_ = cell.Cleanup()
		return coord.OwnedRunner{}, err
	}
	kill := sync.OnceFunc(func() {
		handle.Kill()
		_ = cell.Cleanup()
	})
	s.mu.Lock()
	s.containers = append(s.containers, handle.Name)
	s.cleanups = append(s.cleanups, kill)
	s.mu.Unlock()
	return coord.OwnedRunner{Kill: kill, Wait: isolation.WaitOf(handle), ContainerName: handle.Name}, nil
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
//     container dialed home over Transport 2 and drove the engine via
//     EngineHost;
//  2. the run is PARENT-LESS (ParentRunID "") and owner-owned (the owner's harp
//     as role) — the §5.B2 collision decision, live;
//  3. the container publishes NO port, exposes NO port (docker inspect), and
//     holds NO TCP LISTEN socket (/proc/net/tcp*) — the direct mauve-state
//     negatives on the top-level path;
//  4. the host-side canonical transcript.jsonl survives the container and
//     carries the turn payload (the session-state-mount / silent-no-op guard).
func TestCoordOwnerRun_StructuredAndOneshot_NoPluginNoPort(t *testing.T) {
	dockergate.RequireRuntime(t, (isolation.Docker{}).Available(), "the owner-owned top-level container integration test")
	coord.ResetStrictness(t)
	// NO credential is set on purpose: this run's engine is mock, which
	// declares no Auth because it authenticates against no vendor. Needing a
	// borrowed Anthropic key here would mean auth was being keyed on something
	// other than the engine.

	image := buildBusIntegrationImage(t)
	projectDir := testsupport.ProjectDir(t)

	// The owner's session harp (its address + the transcript-mount key), minted
	// through the same accounting the host uses.
	entry, err := operations.OpenedApp(nil, operations.Handed{Engines: engines.Registry(), SessionClaims: fsstore.SessionClaims}).AssignSession(context.Background(), projectDir, "mock")
	require.NoError(t, err)
	ownerHarp := entry.HarpName

	starter := &dockerOwnerRunStarter{image: image, projectDir: projectDir, harp: ownerHarp}
	coord.TeeHome(t)
	c, err := coord.New(coord.Options{ProjectDir: projectDir, ProjectID: "owner-itest", Spawner: coord.NewFakeSpawner(nil, nil), OwnerHarp: coord.OwnerIdentity().Harp})
	require.NoError(t, err)
	require.NoError(t, coordgrpc.Serve(c))
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

	seed := "OWNER-STRUCT-" + coord.RandID("", 6)
	outcome, err := c.StartOwnedRun(ctx, owner, coord.OwnedRunOf(starter.prepare(ctx, t, containerOwnerLaunch(ownerHarp, engine.Structured)), false), starter.start, seed)
	require.NoError(t, err)
	require.Equal(t, ownerHarp, outcome.Harp)

	// (2) Parent-less, owner-owned.
	var info *coord.RunInfo
	for _, r := range c.ListRuns(true, "").Runs {
		if r.RunID == outcome.RunID {
			info = &r
		}
	}
	require.NotNil(t, info)
	assert.Equal(t, "", info.ParentRunID, "a top-level owner-owned run is parent-less")

	// (1) PAYLOAD: the first-turn mock echo crosses back over Transport 2.
	wantFirst := "mock chat: " + seed
	require.True(t, collector.await(outcome.RunID, wantFirst, 120*time.Second),
		"the container's mock engine never echoed the first turn over Transport 2 (want %q); saw:\n%s", wantFirst, collector.snapshot(outcome.RunID))

	// (1) A SECOND turn via SendOwnedRunTurn (the spool write → doorbell →
	// EngineHost delivery-by-state path) also round-trips. Follow-up turns ride the
	// PeerMessage delivery the plan (§5.B) specifies, so the engine sees the
	// text inside the coordinator-delivery framing (frameCoordinatorMessage);
	// the payload substring reaching the engine's echo is the round-trip proof.
	second := "OWNER-STRUCT2-" + coord.RandID("", 6)
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
	coord.ResetStrictness(t)
	// NO credential is set on purpose: this run's engine is mock, which
	// declares no Auth because it authenticates against no vendor. Needing a
	// borrowed Anthropic key here would mean auth was being keyed on something
	// other than the engine.

	image := buildBusIntegrationImage(t)
	projectDir := testsupport.ProjectDir(t)

	entry, err := operations.OpenedApp(nil, operations.Handed{Engines: engines.Registry(), SessionClaims: fsstore.SessionClaims}).AssignSession(context.Background(), projectDir, "mock")
	require.NoError(t, err)
	ownerHarp := entry.HarpName

	starter := &dockerOwnerRunStarter{image: image, projectDir: projectDir, harp: ownerHarp}
	coord.TeeHome(t)
	c, err := coord.New(coord.Options{ProjectDir: projectDir, ProjectID: "owner-oneshot-itest", Spawner: coord.NewFakeSpawner(nil, nil), OwnerHarp: coord.OwnerIdentity().Harp})
	require.NoError(t, err)
	require.NoError(t, coordgrpc.Serve(c))
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

	seed := "OWNER-ONESHOT-" + coord.RandID("", 6)
	outcome, err := c.StartOwnedRun(ctx, owner, coord.OwnedRunOf(starter.prepare(ctx, t, containerOwnerLaunch(ownerHarp, engine.Structured)), true), starter.start, seed)
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

func newDeltaCollector(events <-chan coord.Event) *deltaCollector {
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

func (dc *deltaCollector) consume(ev coord.Event) {
	dc.mu.Lock()
	defer dc.mu.Unlock()
	switch p := ev.Payload.(type) {
	case coord.MessageStarted:
		if p.Channel == coord.ChannelFinal {
			dc.final[p.MessageID] = true
		}
	case coord.MessageDelta:
		if !dc.final[p.MessageID] {
			return
		}
		b := dc.byRun[ev.RunID]
		if b == nil {
			b = &strings.Builder{}
			dc.byRun[ev.RunID] = b
		}
		b.WriteString(p.Text)
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
