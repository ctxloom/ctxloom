//go:build docker_integration

// light-foe: the docker-gated proof that a container agent launched through the
// PRODUCTION delegated path actually makes PROGRESS — not that its plumbing is
// wired.
//
// This package's other container tests are honest proofs of TRANSPORT: a bus
// round-trip, socket discovery, a docker-direct spawn, a payload crossing back
// out. Container-runtime agents nonetheless shipped in a state where they never
// received their prompt at all — the loop re-delivered a byte-identical
// composed-context block every ~4-5s, seq pinned at 0, 100% "user" entries,
// zero assistant turns — and every one of those tests stayed green, because
// none of them asked the only question that would have caught it: did the agent
// produce a TURN?
//
// So these tests assert the SHARED LIVENESS DEFINITION (implemented in
// transcript_progress_test.go, and deliberately identical to the one the
// production liveness monitor is built against) against a real container child
// spawned via AgentRun → runChild → runChildViaStartRun → issueStartRun →
// awaitRunner → the in-container EngineHost.
//
// BOTH DIRECTIONS are proven here, because a test that only ever passes is the
// defect it exists to catch:
//
//   - GREEN: TestCoordContainerProgress_DelegatedChildAdvances — a healthy
//     container child's transcript satisfies every signal.
//   - RED: TestCoordContainerProgress_CatchesStalledEngine (the engine takes the
//     turn and never emits: the user-only / seq-pinned-at-0 signature) and
//     TestCoordContainerProgress_CatchesContainerThatNeverDialsHome (a real
//     container that is UP but never becomes a runner: no transcript at all).
//     Both drive the SAME assertion entry point as the green test —
//     awaitTranscriptProgress — and require it to return an error.
//
// ASSERTION SURFACE: the canonical transcript file, not operations.SessionFeed.
// See awaitTranscriptProgress's comment for why.
//
// The red direction was verified by literally inverting both fault tests'
// require.Error to require.NoError and running them (exit 1):
//
//	agent wispy-quick-equal is not making progress after 20s:
//	transcript .../sessions/wispy-quick-equal/persist/transcript.jsonl: present=true records=1 max_seq=0 parked=false
//	  entry.type user         x1
//	  FAIL: seq never advanced past 0 (max_seq=0): the agent recorded at most its own prompt and produced nothing
//	  FAIL: no entry.type=="assistant" record: the agent never produced a turn
//	  FAIL: no entry-type variety: every entry record is the same type ("user" x1); ...
//
//	agent nutty-loyal-acorn is not making progress after 45s:
//	transcript .../sessions/nutty-loyal-acorn/persist/transcript.jsonl: present=false records=0 max_seq=-1 parked=false
//	  FAIL: no transcript file: the recorder opens it lazily on the first event, ...
//
//	just test-docker-integration
//	GOWORK=off just test-pkg ./internal/agentcoord/coord/... -tags docker_integration -run CoordContainerProgress
package coord

import (
	"context"
	"fmt"
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
)

// progressAgentName is the one agent progressSpawner resolves. The healthy and
// faulted variants differ in the spawner's mode and in the PROMPT, never in the
// launch path — the whole point is that both take the identical production
// route.
const progressAgentName = "progress-container-worker"

// progressSpawnMode selects what progressSpawner.StartEngine actually launches.
type progressSpawnMode int

const (
	// progressSpawnReal launches the child through the production isolation
	// starter (isolation.StarterForWorkspace → Container.StartRunner →
	// docker-direct `ctxloom llm host mock`) — the same seam
	// container_direct_docker_integration_test.go proves.
	progressSpawnReal progressSpawnMode = iota
	// progressSpawnDark launches a REAL container from the same image that
	// never becomes a runner (it just sleeps), so it can never dial home. This
	// is the deliberate fault: the container is up, `docker ps` shows it, the
	// spawn returns success — and the agent produces nothing. It is the honest
	// stand-in for the shipped defect, whose every cheap signal also lied.
	progressSpawnDark
)

// progressSpawner is the docker_integration Spawner for these tests.
type progressSpawner struct {
	image      string
	projectDir string
	mode       progressSpawnMode

	mu         sync.Mutex
	containers []string
	cleanups   []func()
}

func (s *progressSpawner) Resolve(_ context.Context, agentName string) (*SpawnPlan, error) {
	if agentName != progressAgentName {
		return nil, &unknownAgentError{agentName}
	}
	perm := agent.PermissionBypass
	return &SpawnPlan{
		AgentName: agentName,
		Backend:   "mock",
		Label:     "fast",
		Runtime:   "container",
		Perm:      perm,
		Ladder:    presetLadder(perm),
		// The production resolver reads viaStartRunBackends, which does NOT
		// list "mock" (it is an allowlist of VERIFIED ACP-driven backends). A
		// mock agent therefore cannot take the StartRun path by configuration
		// alone; this plan asserts the flag directly, exactly as
		// directBusSpawner does, so the test drives the migrated path with a
		// deterministic, credential-free engine.
		ViaStartRun: true,
	}, nil
}

func (s *progressSpawner) AssignSession(projectDir, backend string) (string, error) {
	entry, err := operations.AssignSession(projectDir, backend)
	if err != nil {
		return "", err
	}
	return entry.HarpName, nil
}

func (s *progressSpawner) Launch(context.Context, *SpawnPlan, string, string, map[string]string, map[string]string) (*operations.AgentChatLaunch, error) {
	return nil, &unknownAgentError{"legacy Launch is unused (this agent always routes ViaStartRun)"}
}

func (s *progressSpawner) StartEngine(ctx context.Context, plan *SpawnPlan, env, runnerEnv map[string]string) (*EngineSpawn, error) {
	if s.mode == progressSpawnDark {
		return s.startDark(ctx, plan, env)
	}
	rt := isolation.SelectRuntime("docker")
	pol := isolation.NewContainer(rt, s.image).WithSessionState(isolation.SessionStateFromEnv(env))
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
	s.record(handle.Name, kill)
	return &EngineSpawn{WorkDir: ws.Dir(), Env: env, Model: "mock", MCPServers: plan.MCPServers, Kill: kill}, nil
}

// startDark launches a live container from the SAME image that never runs the
// runner — the injected fault. Hand-rolled `docker run` is correct here
// precisely because this path is not meant to exercise the production starter:
// it is meant to break it in the one way the shipped defect broke it, while
// keeping every cheap signal (a spawn that returns success, a container in
// `docker ps`) truthful-looking.
func (s *progressSpawner) startDark(ctx context.Context, plan *SpawnPlan, env map[string]string) (*EngineSpawn, error) {
	name := "ctxloom-progress-dark-" + randID("", 8)
	run := exec.CommandContext(ctx, "docker", "run", "-d", "--name", name, s.image, "sleep", "300")
	if out, err := run.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("start dark container: %v\n%s", err, out)
	}
	kill := sync.OnceFunc(func() {
		_ = exec.Command("docker", "rm", "-f", name).Run()
	})
	s.record(name, kill)
	return &EngineSpawn{WorkDir: "/work", Env: env, Model: "mock", MCPServers: plan.MCPServers, Kill: kill}, nil
}

func (s *progressSpawner) record(name string, kill func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.containers = append(s.containers, name)
	s.cleanups = append(s.cleanups, kill)
}

func (s *progressSpawner) ResumeContext(_ context.Context, plan *SpawnPlan, _ string) string {
	return plan.Context
}
func (s *progressSpawner) MarkSessionEnded(string) {}

func (s *progressSpawner) containerNames() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.containers...)
}

// cleanup force-removes every container this spawner created. Registered by
// every test here so a failed run never leaves debris behind — leftover
// containers and worktrees are how environment-dependent measurements start
// lying.
func (s *progressSpawner) cleanup() {
	s.mu.Lock()
	fns := append([]func(){}, s.cleanups...)
	s.mu.Unlock()
	for _, fn := range fns {
		fn()
	}
}

// awaitTranscriptProgress is the ONE assertion entry point all three tests
// here share: poll harp's canonical transcript until it satisfies the shared
// liveness definition, or the budget expires. It returns the final verdict
// either way, and an error naming every failing signal when the agent is not
// progressing.
//
// WHY THE TRANSCRIPT FILE AND NOT operations.SessionFeed. The live feed is the
// surface this package's existing container tests use (feedTail /
// waitForFeedText), and it is the right one for "did this payload cross the
// wire". It is the WRONG one for this question, for four reasons:
//
//  1. three of the four progress signals are defined on ENVELOPE fields — seq,
//     ts (receipt time), and kind/entry.type. feedTail reads only an entry's
//     Content; the feed carries no seq at all, so "seq pinned at 0" — the
//     signature failure — is not expressible on it.
//  2. "a MISSING transcript is a failure signal" has no analogue on a live
//     feed. An empty feed is ambiguous between "the agent produced nothing" and
//     "we subscribed after it finished", and that ambiguity is exactly the
//     blind spot being closed.
//  3. the transcript is the DURABLE artifact that survives the container. A
//     feed assertion cannot distinguish an agent that ran from one whose output
//     never reached the host-side session state.
//  4. the production liveness monitor is being built against this same
//     definition, on this same file. A test that measured a different surface
//     could drift away from the monitor silently.
//
// The feed keeps its job (payload round-trip) in the tests that already use it;
// this one asks a different question and reads a different surface.
func awaitTranscriptProgress(harp string, timeout time.Duration) (progressVerdict, error) {
	path, err := paths.HarpCanonicalTranscriptPath(harp)
	if err != nil {
		return progressVerdict{}, fmt.Errorf("resolve canonical transcript path for %s: %w", harp, err)
	}
	deadline := time.Now().Add(timeout)
	var (
		v    progressVerdict
		verr error
	)
	for {
		v, verr = evaluateTranscriptProgress(path)
		if verr != nil {
			// A half-written final line is normal while the recorder appends;
			// only a persistently corrupt file is a broken measurement.
			if time.Now().After(deadline) {
				return v, verr
			}
		} else if v.progressing() {
			return v, nil
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if verr != nil {
		return v, verr
	}
	return v, fmt.Errorf("agent %s is not making progress after %s:\n%s", harp, timeout, v)
}

// startProgressChild is the shared setup: build the image, stand a coordinator
// up on an isolated HOME, and AgentRun one container child with prompt. Returns
// the child's harp and the live spawner.
func startProgressChild(t *testing.T, mode progressSpawnMode, awaitBudget time.Duration, prompt string) (string, *progressSpawner) {
	t.Helper()
	if !(isolation.Docker{}).Available() {
		t.Skip("docker unavailable; skipping the container-progress integration test")
	}
	resetStrictness(t)
	// The container profile authenticates the in-container engine via the
	// scoped ANTHROPIC_* passthrough; mock ignores the value, but a resolvable
	// auth is what clears PrepareWorkspace's gate under an isolated HOME.
	t.Setenv("ANTHROPIC_API_KEY", "itest-mock-key")

	// Build BEFORE isolating cwd (ProjectDir chdirs outside the module).
	image := buildBusIntegrationImage(t)
	projectDir := testsupport.ProjectDir(t) // isolated HOME + cwd; never the real ~/.ctxloom

	sp := &progressSpawner{image: image, projectDir: projectDir, mode: mode}
	t.Cleanup(sp.cleanup)

	c, err := New(Options{
		ProjectDir:         projectDir,
		ProjectKey:         "progress-itest",
		Spawner:            sp,
		RunnerAwaitTimeout: awaitBudget,
	})
	require.NoError(t, err)
	require.NoError(t, c.Serve())
	t.Cleanup(c.Close)

	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		for _, name := range sp.containerNames() {
			logs, _ := exec.Command("docker", "logs", name).CombinedOutput()
			t.Logf("container %s logs:\n%s", name, logs)
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	out, err := c.AgentRun(ctx, ownerIdentity(), progressAgentName, prompt, "", "")
	require.NoError(t, err)
	require.NotEmpty(t, out.Harp)
	require.Equal(t, "container", out.Runtime)
	return out.Harp, sp
}

// TestCoordContainerProgress_DelegatedChildAdvances is the GREEN direction: a
// container child launched through the production delegated path (AgentRun →
// runChildViaStartRun → issueStartRun → awaitRunner) produces a real turn, and
// its canonical transcript satisfies every signal of the shared liveness
// definition.
//
// The prompt carries the mock backend's TOOLS marker, so the turn emits the
// full entry vocabulary (thinking → tool_use → tool_result → assistant) on top
// of the host-recorded user turn. That matters: an entry-type-VARIETY assertion
// satisfied only by {user, assistant} would pass against a stub too weak to
// exercise it, which is the same blind spot one level up.
func TestCoordContainerProgress_DelegatedChildAdvances(t *testing.T) {
	seed := "PROGRESS-SEED-" + randID("", 6)
	harp, sp := startProgressChild(t, progressSpawnReal, 0, "TOOLS "+seed)

	// A real container is up (the cheap signal — necessary, never sufficient).
	require.Eventually(t, func() bool { return len(sp.containerNames()) > 0 && sp.containerNames()[0] != "" },
		10*time.Second, 50*time.Millisecond)
	containerName := sp.containerNames()[0]
	require.Eventually(t, func() bool {
		psOut, _ := exec.Command("docker", "ps", "--filter", "name="+containerName, "--format", "{{.Names}}").Output()
		return strings.TrimSpace(string(psOut)) == containerName
	}, 30*time.Second, 250*time.Millisecond, "the child's container must actually be running")

	// The sufficient signal: PROGRESS.
	v, err := awaitTranscriptProgress(harp, 120*time.Second)
	require.NoError(t, err, "the container child must make progress")

	// Pin the individual signals so a future weakening of the definition is a
	// visible diff, not a quietly-passing test.
	assert.True(t, v.Present, "the transcript file must exist")
	assert.Greater(t, v.MaxSeq, 0, "seq must advance past 0")
	assert.False(t, v.stalled())
	assert.Positive(t, v.EntryTypes["assistant"], "at least one assistant entry")
	assert.Greater(t, len(v.EntryTypes), 1, "entry-type variety")
	for _, want := range []string{"user", "thinking", "tool_use", "tool_result", "assistant"} {
		assert.Positive(t, v.EntryTypes[want],
			"the full entry vocabulary must survive the container round trip; missing %q in:\n%s", want, v)
	}
	// The payload itself, so this is never a shape-only assertion.
	assert.Contains(t, transcriptContents(t, harp), seed,
		"the transcript must carry the delivered prompt's payload")
}

// TestCoordContainerProgress_CatchesStalledEngine is the RED direction against
// a STALLED engine: the container is healthy, the runner dials home, StartRun
// completes, the prompt is delivered and recorded — and the engine then takes
// the turn and never emits anything (the mock backend's HANG marker).
//
// That leaves the exact on-disk signature the shipped defect left: a transcript
// whose only record is the user turn, seq pinned at 0, no assistant entry, one
// distinct entry type. The plumbing-only assertions this package already had
// would call this a healthy launch; awaitTranscriptProgress must call it dead.
func TestCoordContainerProgress_CatchesStalledEngine(t *testing.T) {
	seed := "STALL-SEED-" + randID("", 6)
	harp, _ := startProgressChild(t, progressSpawnReal, 0, "HANG "+seed)

	// The transcript must first EXIST — otherwise this would be testing the
	// missing-file signal by accident rather than the pinned-seq one.
	require.Eventually(t, func() bool {
		v, err := evaluateTranscriptProgress(mustTranscriptPath(t, harp))
		return err == nil && v.Present
	}, 120*time.Second, 250*time.Millisecond,
		"the delivered prompt must be recorded (this proves the child really did launch and get its turn)")

	// The SAME assertion the green test uses must reject this agent.
	v, err := awaitTranscriptProgress(harp, 20*time.Second)
	require.Error(t, err, "a stalled engine must NOT read as progress; verdict was:\n%s", v)
	assert.True(t, v.stalled())
	assert.True(t, v.Present, "the file exists — this is the pinned-seq signature, not the missing-file one")
	assert.Equal(t, 0, v.MaxSeq, "seq pinned at 0 is the signature failure")
	assert.Zero(t, v.EntryTypes["assistant"], "no assistant turn was ever produced")

	joined := strings.Join(v.Failures, "\n")
	assert.Contains(t, joined, "seq never advanced past 0")
	assert.Contains(t, joined, "no entry.type==\"assistant\"")
	assert.Contains(t, joined, "no entry-type variety")
	assert.Contains(t, err.Error(), "is not making progress")
}

// TestCoordContainerProgress_CatchesContainerThatNeverDialsHome is the RED
// direction against the shipped defect's own shape: a REAL container comes up
// from the real image, `docker ps` shows it, StartEngine returns success — and
// it never becomes a runner, so StartRun is never issued, the in-container
// EngineHost never runs, and the recorder never opens a file.
//
// Every cheap signal here is green. Only the progress question is red, and only
// because a missing transcript is treated as a VERDICT rather than an error.
func TestCoordContainerProgress_CatchesContainerThatNeverDialsHome(t *testing.T) {
	seed := "DARK-SEED-" + randID("", 6)
	// A deliberately tiny dial-home budget: the fault is permanent, so waiting
	// out the production 5-minute budget would only make the test slow.
	harp, sp := startProgressChild(t, progressSpawnDark, 15*time.Second, "TOOLS "+seed)

	// The lying cheap signal, asserted so the fault's realism is on the record:
	// a container really is up.
	require.Eventually(t, func() bool { return len(sp.containerNames()) > 0 && sp.containerNames()[0] != "" },
		10*time.Second, 50*time.Millisecond)
	containerName := sp.containerNames()[0]
	require.Eventually(t, func() bool {
		psOut, _ := exec.Command("docker", "ps", "--filter", "name="+containerName, "--format", "{{.Names}}").Output()
		return strings.TrimSpace(string(psOut)) == containerName
	}, 30*time.Second, 250*time.Millisecond, "the dark container must actually be running (the signal that lies)")

	v, err := awaitTranscriptProgress(harp, 45*time.Second)
	require.Error(t, err, "a container that never dials home must NOT read as progress; verdict was:\n%s", v)
	assert.False(t, v.Present, "no transcript file at all — the recorder opens lazily on the first event")
	assert.True(t, v.stalled())
	require.Len(t, v.Failures, 1)
	assert.Contains(t, v.Failures[0], "no transcript file")
}

// mustTranscriptPath resolves harp's canonical transcript path or fails.
func mustTranscriptPath(t *testing.T, harp string) string {
	t.Helper()
	p, err := paths.HarpCanonicalTranscriptPath(harp)
	require.NoError(t, err)
	return p
}

// transcriptContents reads harp's canonical transcript, failing loud if it is
// absent. Deliberately NOT evaluateTranscriptProgress's missing-is-a-verdict
// handling: by the time this is called the file's existence has already been
// asserted, so absence here is a broken test, not a dead agent.
func transcriptContents(t *testing.T, harp string) string {
	t.Helper()
	b, err := os.ReadFile(mustTranscriptPath(t, harp))
	require.NoError(t, err)
	return string(b)
}
