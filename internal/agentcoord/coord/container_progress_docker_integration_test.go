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
// So these tests assert the SHARED LIVENESS DEFINITION — which lives in exactly
// one place, internal/liveness, and is reached here through the thin adapter in
// transcript_progress_test.go — against a real container child spawned via
// AgentRun → runChild → runChildViaStartRun → issueStartRun → awaitRunner → the
// in-container EngineHost.
//
// BOTH DIRECTIONS are proven here, because a test that only ever passes is the
// defect it exists to catch:
//
//   - GREEN: TestCoordContainerProgress_DelegatedChildAdvances — a healthy
//     container child's transcript satisfies every signal, under the PRODUCTION
//     thresholds (liveness.DefaultThresholds), not a test-friendly loosening.
//   - RED: TestCoordContainerProgress_CatchesStalledEngine (the engine takes the
//     turn and never emits: the user-only / seq-pinned-at-0 signature) and
//     TestCoordContainerProgress_CatchesContainerThatNeverDialsHome (a real
//     container that is UP but never becomes a runner: no transcript at all).
//     Both require liveness to FIRE, and then additionally require the green
//     test's own entry point — awaitTranscriptProgress — to refuse them.
//
// WHY THE RED TESTS COMPRESS TWO THRESHOLDS. The rules are unchanged; the two
// TIME graces are not. liveness will not infer a stall from silence until the
// launch grace (5m) and the quiet grace (10m) have passed, and it is right not
// to — that gate is precisely the fix for the old, unconditional "no assistant
// entry ⇒ dead" rule, which called a healthy tool-only turn dead. A test cannot
// wait 10 minutes to watch a 10-minute rule, so progressTightThresholds
// compresses those two durations and leaves every other tuning at its
// production default. The green test needs no such help and takes none.
//
// ASSERTION SURFACE: the canonical transcript file, not operations.SessionFeed.
// See pollProgress's comment for why.
//
// The red direction was verified by literally inverting both fault tests'
// require.NoError on the stall await to require.Error and running them (exit 1);
// the verdicts they produce read:
//
//	transcript .../sessions/<harp>/persist/transcript.jsonl: state=stalled present=true records=1 max_seq=0 seq_pinned=false
//	  entry types: user (assistant x0, entries x1)
//	  reason: quiet for 6s with zero assistant turns in 1 records (entry types: user) — the engine has never produced a turn
//
//	transcript .../sessions/<harp>/persist/transcript.jsonl: state=stalled present=false records=0 max_seq=0 seq_pinned=false
//	  reason: no canonical transcript exists 3s after launch — the engine has emitted zero events ...
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

	"github.com/ctxloom/ctxloom/internal/liveness"
	"github.com/ctxloom/ctxloom/internal/lm/isolation"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/testsupport/dockergate"
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
	// Auth keys on the plan's Backend (the engine), never on plan.AgentName —
	// see directBusSpawner.StartEngine's comment in
	// container_direct_docker_integration_test.go.
	pol := isolation.NewContainerFor(rt, plan.Backend).WithImage(s.image).WithSessionState(isolation.SessionStateFromEnv(env))
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

// progressTightThresholds is the PRODUCTION liveness ruleset with its two TIME
// graces compressed, for the red directions only. Every field left zero
// normalizes to liveness's own default inside liveness.New, so this loosens the
// clock and nothing else — the repeat threshold, the jitter model and the CPU
// floor are all still the shipped ones.
var progressTightThresholds = liveness.Thresholds{
	// The coordinator's real dial-home budget is minutes; the dark container's
	// fault is permanent, so 2s is all the benefit of the doubt it needs.
	StartGrace: 2 * time.Second,
	// The real quiet grace (10m) is sized for a legitimately silent long tool
	// call. A hung mock engine has no such excuse, and a test cannot wait ten
	// minutes to watch a ten-minute rule fire.
	QuietGrace: 5 * time.Second,
}

// pollProgress is the ONE assertion entry point all three tests here share:
// poll harp's canonical transcript through internal/liveness until `until`
// accepts the verdict, or the budget expires. It returns the final verdict
// either way plus whether `until` was ever satisfied.
//
// It reaches its verdict entirely through liveness.Monitor.Assess — there is no
// signal logic in this file or in transcript_progress_test.go, so a container
// test and the production monitor can no longer disagree about what "alive"
// means.
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
//  4. the production liveness monitor reads this same file. A test that
//     measured a different surface could drift away from the monitor silently.
//
// The feed keeps its job (payload round-trip) in the tests that already use it;
// this one asks a different question and reads a different surface.
func pollProgress(harp string, timeout time.Duration, thr liveness.Thresholds, startedAt time.Time,
	until func(progressVerdict) bool) (progressVerdict, bool, error) {
	path, err := paths.HarpCanonicalTranscriptPath(harp)
	if err != nil {
		return progressVerdict{}, false, fmt.Errorf("resolve canonical transcript path for %s: %w", harp, err)
	}
	mon := liveness.New(liveness.Options{Thresholds: thr})
	deadline := time.Now().Add(timeout)
	for {
		v := assessTranscriptProgress(mon, harp, path, startedAt)
		if until(v) {
			return v, true, nil
		}
		if time.Now().After(deadline) {
			return v, false, nil
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// awaitTranscriptProgress is the GREEN direction: wait for liveness to reach a
// positive HEALTHY verdict. An expired budget is an error carrying the final
// verdict's full reason, so a failure is always auditable.
func awaitTranscriptProgress(harp string, timeout time.Duration, thr liveness.Thresholds, startedAt time.Time) (progressVerdict, error) {
	v, ok, err := pollProgress(harp, timeout, thr, startedAt, progressVerdict.progressing)
	switch {
	case err != nil:
		return v, err
	case !ok:
		return v, fmt.Errorf("agent %s is not making progress after %s:\n%s", harp, timeout, v)
	}
	return v, nil
}

// awaitTranscriptStall is the RED direction: wait for liveness to FIRE. Same
// monitor, same rules, opposite question — a monitor that never fires is the
// defect these tests exist to catch, so "it did not fire" is the error here.
func awaitTranscriptStall(harp string, timeout time.Duration, thr liveness.Thresholds, startedAt time.Time) (progressVerdict, error) {
	v, ok, err := pollProgress(harp, timeout, thr, startedAt, progressVerdict.stalled)
	switch {
	case err != nil:
		return v, err
	case !ok:
		return v, fmt.Errorf("agent %s was never reported as stalled within %s:\n%s", harp, timeout, v)
	}
	return v, nil
}

// startProgressChild is the shared setup: build the image, stand a coordinator
// up on an isolated HOME, and AgentRun one container child with prompt. Returns
// the child's harp, the instant the run was enqueued (liveness measures every
// age-gated rule from it) and the live spawner.
func startProgressChild(t *testing.T, mode progressSpawnMode, awaitBudget time.Duration, prompt string) (string, time.Time, *progressSpawner) {
	t.Helper()
	dockergate.RequireRuntime(t, (isolation.Docker{}).Available(), "the container-progress integration test")
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

	startedAt := time.Now()
	out, err := c.AgentRun(ctx, ownerIdentity(), progressAgentName, prompt, "", "")
	require.NoError(t, err)
	require.NotEmpty(t, out.Harp)
	require.Equal(t, "container", out.Runtime)
	return out.Harp, startedAt, sp
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
	harp, startedAt, sp := startProgressChild(t, progressSpawnReal, 0, "TOOLS "+seed)

	// A real container is up (the cheap signal — necessary, never sufficient).
	require.Eventually(t, func() bool { return len(sp.containerNames()) > 0 && sp.containerNames()[0] != "" },
		10*time.Second, 50*time.Millisecond)
	containerName := sp.containerNames()[0]
	require.Eventually(t, func() bool {
		psOut, _ := exec.Command("docker", "ps", "--filter", "name="+containerName, "--format", "{{.Names}}").Output()
		return strings.TrimSpace(string(psOut)) == containerName
	}, 30*time.Second, 250*time.Millisecond, "the child's container must actually be running")

	// The sufficient signal: PROGRESS — judged under the PRODUCTION thresholds,
	// so the green direction owes nothing to a test-only loosening.
	v, err := awaitTranscriptProgress(harp, 120*time.Second, liveness.DefaultThresholds(), startedAt)
	require.NoError(t, err, "the container child must make progress")

	// Pin the individual signals so a future weakening of the definition is a
	// visible diff, not a quietly-passing test.
	assert.True(t, v.present(), "the transcript file must exist")
	assert.Greater(t, v.maxSeq(), 0, "seq must advance past 0")
	assert.False(t, v.stalled())
	assert.Positive(t, v.assistantEntries(), "at least one assistant entry")
	assert.Greater(t, len(v.entryTypes()), 1, "entry-type variety")
	for _, want := range []string{"user", "thinking", "tool_use", "tool_result", "assistant"} {
		assert.Contains(t, v.entryTypes(), want,
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
// would call this a healthy launch; liveness must call it dead once the agent
// has also gone silent (see progressTightThresholds for why the silence is
// measured on a compressed clock, and the package comment for why the silence
// gate is REQUIRED rather than incidental).
func TestCoordContainerProgress_CatchesStalledEngine(t *testing.T) {
	seed := "STALL-SEED-" + randID("", 6)
	harp, startedAt, _ := startProgressChild(t, progressSpawnReal, 0, "HANG "+seed)

	// The transcript must first EXIST — otherwise this would be testing the
	// missing-file signal by accident rather than the silent-engine one.
	require.Eventually(t, func() bool {
		st, err := liveness.ReadTranscript(mustTranscriptPath(t, harp))
		return err == nil && st.Exists
	}, 120*time.Second, 250*time.Millisecond,
		"the delivered prompt must be recorded (this proves the child really did launch and get its turn)")

	v, err := awaitTranscriptStall(harp, 60*time.Second, progressTightThresholds, startedAt)
	require.NoError(t, err, "a stalled engine must be caught; verdict was:\n%s", v)
	assert.True(t, v.stalled())
	assert.True(t, v.present(), "the file exists — this is the silent-engine signature, not the missing-file one")
	assert.Equal(t, 0, v.maxSeq(), "seq pinned at 0 is the signature failure")
	assert.Zero(t, v.assistantEntries(), "no assistant turn was ever produced")
	assert.Contains(t, v.reason(), "zero assistant turns")

	// And the GREEN test's own entry point must refuse the same agent — a
	// verdict that fires but still reads as progress would be no verdict at all.
	pv, perr := awaitTranscriptProgress(harp, 2*time.Second, progressTightThresholds, startedAt)
	require.Error(t, perr, "a stalled engine must NOT read as progress; verdict was:\n%s", pv)
	assert.Contains(t, perr.Error(), "is not making progress")
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
	harp, startedAt, sp := startProgressChild(t, progressSpawnDark, 15*time.Second, "TOOLS "+seed)

	// The lying cheap signal, asserted so the fault's realism is on the record:
	// a container really is up.
	require.Eventually(t, func() bool { return len(sp.containerNames()) > 0 && sp.containerNames()[0] != "" },
		10*time.Second, 50*time.Millisecond)
	containerName := sp.containerNames()[0]
	require.Eventually(t, func() bool {
		psOut, _ := exec.Command("docker", "ps", "--filter", "name="+containerName, "--format", "{{.Names}}").Output()
		return strings.TrimSpace(string(psOut)) == containerName
	}, 30*time.Second, 250*time.Millisecond, "the dark container must actually be running (the signal that lies)")

	v, err := awaitTranscriptStall(harp, 60*time.Second, progressTightThresholds, startedAt)
	require.NoError(t, err, "a container that never dials home must be caught; verdict was:\n%s", v)
	assert.False(t, v.present(), "no transcript file at all — the recorder opens lazily on the first event")
	assert.True(t, v.stalled())
	assert.Zero(t, v.records())
	assert.Contains(t, v.reason(), "no canonical transcript exists")

	// And the GREEN test's own entry point must refuse it too.
	pv, perr := awaitTranscriptProgress(harp, 2*time.Second, progressTightThresholds, startedAt)
	require.Error(t, perr, "a container that never dials home must NOT read as progress; verdict was:\n%s", pv)
	assert.Contains(t, perr.Error(), "is not making progress")
}

// mustTranscriptPath resolves harp's canonical transcript path or fails.
func mustTranscriptPath(t *testing.T, harp string) string {
	t.Helper()
	p, err := paths.HarpCanonicalTranscriptPath(harp)
	require.NoError(t, err)
	return p
}

// transcriptContents reads harp's canonical transcript, failing loud if it is
// absent. Deliberately NOT liveness's missing-is-a-verdict handling: by the
// time this is called the file's existence has already been asserted, so
// absence here is a broken test, not a dead agent.
func transcriptContents(t *testing.T, harp string) string {
	t.Helper()
	b, err := os.ReadFile(mustTranscriptPath(t, harp))
	require.NoError(t, err)
	return string(b)
}
