package coord

import (
	"context"
	"fmt"
	"maps"
	"sync"
	"testing"
	"time"

	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// fakeEngine is one scripted child engine conversation. Turn texts are
// recorded as received; each turn completes automatically unless gated
// (turnGate non-nil — the test releases turns one by one), and the stream can
// end itself after endAfterTurns turns (an engine that exits).
type fakeEngine struct {
	mu            sync.Mutex
	texts         []string
	gotEnv        map[string]string
	gotRunnerEnv  map[string]string
	gotResumeID   string // the resumeSessionID this engine's Launch call carried
	turnGate      chan struct{}
	endAfterTurns int
	// oneshot marks the returned AgentChatLaunch as the no-structured-chat
	// fallback (operations.AgentChatLaunch.Oneshot) — scripts a fakeSpawner
	// child through children.go's oneshot bridging (PublishEvents + the
	// parent-mailbox "result" bridge) without a real backend.
	oneshot bool
	// sessionID, when set, scripts this fake as a LEGACY (non-viaStartRun)
	// backend that emits a native ChatEvent.Session on its first turn — the
	// production shape a legacy go-plugin-dial backend can take. handleChildEvent
	// must not drop it. Lets a test
	// prove the coordinator CAPTURES a legacy backend's native session id
	// (previously silently discarded — no ev.Session case existed).
	sessionID string
}

func (f *fakeEngine) recordedTexts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.texts...)
}

func (f *fakeEngine) runnerEnv() map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]string, len(f.gotRunnerEnv))
	for k, v := range f.gotRunnerEnv {
		out[k] = v
	}
	return out
}

func (f *fakeEngine) env() map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]string, len(f.gotEnv))
	for k, v := range f.gotEnv {
		out[k] = v
	}
	return out
}

// launch adapts the fake engine onto the operations.AgentChatLaunch shape the
// Spawner returns.
func (f *fakeEngine) launch(ctx context.Context, contextText, resumeSessionID string, env, runnerEnv map[string]string) *operations.AgentChatLaunch {
	f.mu.Lock()
	f.gotEnv = env
	f.gotRunnerEnv = runnerEnv
	f.gotResumeID = resumeSessionID
	f.mu.Unlock()
	in := make(chan agent.ChatMessage)
	// events is small-buffered (not unbuffered like `in`): the REAL client
	// wrapper (internal/lm/grpc/chat.go's GRPCClient.Chat) decouples a
	// backend's event emission from the caller's sendTurn/driveChild
	// ordering via its own independent in-pump/events-pump goroutines plus
	// gRPC's own stream buffering — a legacy backend that emits a pre-loop
	// Session event before ever reading `in` does not deadlock in production
	// because of that slack. This fake
	// wires straight to bare channels with none of that slack, so it needs
	// its own small buffer to avoid an artificial deadlock that has nothing
	// to do with the behavior under test.
	events := make(chan agent.ChatEvent, 4)
	errs := make(chan error, 1)
	turnCtx, cancel := context.WithCancel(ctx)
	first := true
	go func() {
		defer close(errs)
		defer close(events)
		if f.sessionID != "" {
			// Mirror a legacy backend's Chat: a Session event rides BEFORE
			// the first turn's entries — this fakeEngine's stand-in for a
			// legacy (non-viaStartRun) backend's native session id.
			select {
			case events <- agent.ChatEvent{Session: &agent.ChatSessionInfo{SessionID: f.sessionID}}:
			case <-turnCtx.Done():
				return
			}
		}
		turns := 0
		for msg := range in {
			text := msg.Text
			if first {
				// The real launch prepends the lead context to the first
				// turn; mirror it so lead-block assertions hold.
				if contextText != "" {
					text = contextText + "\n\n" + text
				}
				first = false
			}
			if text == "" {
				continue
			}
			f.mu.Lock()
			f.texts = append(f.texts, text)
			gate := f.turnGate
			f.mu.Unlock()
			if gate != nil {
				select {
				case <-gate:
				case <-turnCtx.Done():
					return
				}
			}
			select {
			case events <- agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeAssistant, Content: "ok"}}:
			case <-turnCtx.Done():
				return
			}
			select {
			case events <- agent.ChatEvent{Complete: &agent.TurnMeta{StopReason: "end_turn"}}:
			case <-turnCtx.Done():
				return
			}
			turns++
			if f.endAfterTurns > 0 && turns >= f.endAfterTurns {
				return
			}
		}
	}()
	return &operations.AgentChatLaunch{In: in, Events: events, Errs: errs, Close: cancel, Oneshot: f.oneshot}
}

// resumeSessionID returns the resumeSessionID this engine's Launch call
// carried (empty for a fresh, non-resumed launch) — Slice 0's threading
// proof on the fake side.
func (f *fakeEngine) resumeSessionID() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.gotResumeID
}

// fakeSpawner is the hermetic Spawner: no config, no engines, no isolation.
// It mints deterministic harps and scripts one fakeEngine per launch.
type fakeSpawner struct {
	mu       sync.Mutex
	next     func() *fakeEngine
	engines  []*fakeEngine
	harpSeq  int
	agents   map[string]fakeAgent // agent name → resolved plan bits
	resolved []string
	assigned []string
	// sessionsEnded records each MarkSessionEnded call's harp, in call order.
	sessionsEnded []string
	// launchErr, when set, fails every legacy Launch with it — a child that
	// is admitted (it holds an execution slot) and then dies at standup,
	// which is the shape that separates "queued behind the cap" from
	// "started and failed".
	launchErr error
	perms     []agent.PermissionMode
	// nextChat scripts the MIGRATED (StartRun) path's engine; StartEngine
	// spawns a REAL runner half (Home + EngineHost over the coordinator's
	// live gRPC listeners) around it. chats/kills record per spawn.
	nextChat func() *scriptedChat
	chats    []*scriptedChat
	kills    []func()
	// nextBackend, when set, supplies a REAL agent.StructuredChat backend
	// for the MIGRATED path instead of nextChat's scripted double — the
	// seam the live-path reproduction (acp_approval_test.go) uses to put
	// the genuine internal/acp driver, spawning a genuine ACP subprocess,
	// under the genuine EngineHost/Home/Coordinator stack. engineWorkDir
	// and engineEnv ride into the HarnessSpec the runner decodes, so that
	// subprocess gets a real cwd and its own marker env.
	nextBackend   func() agent.StructuredChat
	engineWorkDir string
	engineEnv     map[string]string
	// engineCaps is the Hello advertisement StartEngine's in-process Home
	// makes. It defaults to EMPTY — i.e. peer_messaging only — deliberately,
	// so every test written before plane 2's control verbs keeps exercising
	// the §5.6 MAILBOX route it was written against, which is what proves
	// Inject's fallback survives as a strict superset. A test that wants the
	// plane-2 path says so by setting this to RunnerCapabilities(true), the
	// advertisement a production migrated child actually makes
	// (llm_runner_common.go).
	engineCaps []string
	// spoolSweepInterval is handed to every in-process Home this fake builds
	// (HomeConfig.SpoolSweepInterval). A cutover test that has to prove the
	// SWEEP recovers a dropped doorbell sets it small; everything else leaves
	// it zero and gets the production cadence, which no test waits on.
	spoolSweepInterval time.Duration
	// engineHomes records each StartEngine call's runner-side Home, in spawn
	// order — the seam a plane-2 test needs to drain the runner-LOCAL
	// agent_recv a control body is parked in.
	engineHomes []*Home
	// engineStderrTail, when set, is threaded onto each EngineSpawn.StderrTail
	// — the runner's captured stderr tail. It stands in for a docker-direct
	// runner's ring (the container's streamed stderr): a runner-loss test uses
	// it to prove terminateRun surfaces the container's dying words when the
	// engine emitted no FAILED RunCompleted.
	engineStderrTail func() string
	// workspaces records each Launch/StartEngine call's plan.Workspace, in
	// spawn order — GAP 2's threading proof (AgentRun -> SpawnPlan.Workspace
	// -> here), without needing real isolation machinery.
	workspaces []string
	// dirtyTreeHandlers records each Launch/StartEngine call's
	// plan.DirtyTreeHandler, in spawn order — the identical threading proof
	// as workspaces, for AgentRun's dirty_tree_handler override.
	dirtyTreeHandlers []operations.DirtyTreeHandler
	// versionProbes records each RecordEngineVersion call's harp, in call
	// order — the SLOW, post-registration half of session start (the real
	// one execs a vendor CLI).
	versionProbes []string
	// versionProbeEntered, when non-nil, receives the harp the moment
	// RecordEngineVersion is entered, and versionProbeGate then holds it
	// there until the test closes the gate. Together they are the seam the
	// pre-existing fakes lacked entirely: a spawn step that is SLOW rather
	// than merely failing, which is what makes "the run is registered
	// before the slow step runs" assertable by ORDER instead of by clock.
	versionProbeEntered chan string
	versionProbeGate    chan struct{}
	// resolveEntered / resolveGate are the same slow-step seam over the
	// FIRST pre-registration step, agent resolution. Together with the
	// version-probe pair they are what lets a test park agent_run inside
	// the span that used to be traceless and look at what the world can
	// see from outside it.
	resolveEntered chan string
	resolveGate    chan struct{}
}

type fakeAgent struct {
	perm        string // headless permission enum; "" refuses (D3)
	runtime     agent.RuntimeAxis
	profiles    []string
	unknown     bool
	viaStartRun bool // route this agent over the migrated StartRun path
	// backend is the SpawnPlan.Backend this agent resolves to (rides into
	// HarnessSpec.harness on the StartRun path). Empty defaults to "mock" —
	// most tests don't care and the coordinator's own mechanics are
	// backend-agnostic (C3 recon); tests pinning per-backend parity (e.g.
	// TestStartRun_BackendParity) set it explicitly.
	backend string
	// ladder (C2) is an explicit test escalation ladder; nil derives the
	// perm preset (mirrors prodSpawner.Resolve's buildLadder-or-preset
	// call), so tests that don't care about approvals see prod-shaped
	// defaults.
	ladder Ladder
	// mcpServers is the child's resolved MCP server set (mirrors
	// prodSpawner.Resolve composing plan.MCPServers via childMCPServers) —
	// nil for tests that don't care what a child's privilege journal shows.
	mcpServers []agent.ChatMCPServer
	// oneshot resolves this agent to SpawnPlan.ResumeMode == ResumeModeOneShot
	// (the one-shot turn loop, Slice 4). The fake sets it directly rather than
	// running resolveResumeMode + the production "not yet available" gate —
	// tests drive the mechanism the real Resolve() only starts returning once
	// piece 5 deletes that blanket gate for resume-capable backends.
	oneshot bool
}

func newFakeSpawner(agents map[string]fakeAgent, next func() *fakeEngine) *fakeSpawner {
	if next == nil {
		next = func() *fakeEngine { return &fakeEngine{} }
	}
	return &fakeSpawner{next: next, agents: agents}
}

func (s *fakeSpawner) Resolve(ctx context.Context, agentName string) (*SpawnPlan, error) {
	s.awaitResolveGate(ctx, agentName)
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.agents[agentName]
	if !ok || a.unknown {
		return nil, fmt.Errorf("agent %q not found", agentName)
	}
	// Mirror the production spawner's D3 strictness window: the headless
	// gate's finding must abort the resolve in strict mode.
	var (
		perm     agent.PermissionMode
		degraded []string
	)
	if gerr := func() error {
		mark := strictness.Checkpoint()
		perm, degraded = headlessSafePermission(agentName, a.perm)
		return strictness.FindingsError(mark)
	}(); gerr != nil {
		return nil, gerr
	}
	s.resolved = append(s.resolved, agentName)
	ladder := a.ladder
	if ladder == nil {
		ladder = presetLadder(perm)
	}
	backend := a.backend
	if backend == "" {
		backend = "mock"
	}
	resumeMode := ResumeModePersistent
	if a.oneshot {
		resumeMode = ResumeModeOneShot
	}
	return &SpawnPlan{
		AgentName:   agentName,
		Backend:     backend,
		Label:       "fast",
		Profiles:    a.profiles,
		Runtime:     a.runtime,
		Context:     "FRAG-ONE",
		Perm:        perm,
		Ladder:      ladder,
		Degraded:    degraded,
		ViaStartRun: a.viaStartRun,
		MCPServers:  a.mcpServers,
		ResumeMode:  resumeMode,
	}, nil
}

func (s *fakeSpawner) AssignSession(_, _ string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.harpSeq++
	harp := fmt.Sprintf("child-harp-%d", s.harpSeq)
	s.assigned = append(s.assigned, harp)
	return harp, nil
}

func (s *fakeSpawner) Launch(ctx context.Context, plan *SpawnPlan, contextText, resumeSessionID string, env, runnerEnv map[string]string) (*operations.AgentChatLaunch, error) {
	s.mu.Lock()
	e := s.next()
	s.engines = append(s.engines, e)
	s.perms = append(s.perms, plan.Perm)
	s.workspaces = append(s.workspaces, plan.Workspace)
	s.dirtyTreeHandlers = append(s.dirtyTreeHandlers, plan.DirtyTreeHandler)
	launchErr := s.launchErr
	s.mu.Unlock()
	if launchErr != nil {
		return nil, launchErr
	}
	return e.launch(ctx, contextText, resumeSessionID, env, runnerEnv), nil
}

// StartEngine spawns the MIGRATED path's runner half for real: an in-process
// Home dialing the coordinator's live listeners with the spawn-injected trio,
// an EngineHost wired as its RunnerRequest handler, and a scripted
// StructuredChat as the engine. Kill models SIGKILL (docker-stop): the
// shared context dies — no RunExited, no clean teardown; the coordinator's
// loss synthesis is what must notice.
func (s *fakeSpawner) StartEngine(ctx context.Context, plan *SpawnPlan, env, runnerEnv map[string]string) (*EngineSpawn, error) {
	s.mu.Lock()
	var backend agent.StructuredChat
	if s.nextBackend != nil {
		backend = s.nextBackend()
	} else {
		mk := s.nextChat
		if mk == nil {
			mk = func() *scriptedChat { return &scriptedChat{} }
		}
		sc := mk()
		s.chats = append(s.chats, sc)
		backend = sc
	}
	s.perms = append(s.perms, plan.Perm)
	s.workspaces = append(s.workspaces, plan.Workspace)
	s.dirtyTreeHandlers = append(s.dirtyTreeHandlers, plan.DirtyTreeHandler)
	workDir := s.engineWorkDir
	engineEnv := s.engineEnv
	caps := s.engineCaps
	sweepInterval := s.spoolSweepInterval
	s.mu.Unlock()

	sctx, cancel := context.WithCancel(ctx)
	host := NewEngineHost(sctx, backend, plan.Backend, runnerEnv[EnvRunID])
	home, err := NewHome(sctx, HomeConfig{
		URL:          runnerEnv[EnvCoordURL],
		Token:        runnerEnv[EnvCoordCred],
		RunID:        runnerEnv[EnvRunID],
		Harness:      plan.Backend,
		Version:      "test",
		Engine:       host.Handle,
		Capabilities: caps,
		// Read out of the STAMPED runner env rather than handed in by the
		// test, mirroring production's consumeCoordinatorReachBack
		// (llm_runner_common.go) field for field. That makes every test using
		// this fake a live check that the coordinator's per-spawn stamp
		// actually reaches the runner: a tee enabled only on the coordinator
		// would show up here as a runner that never mirrors, which is exactly
		// the half-populated spool the soak must not have to diagnose.
		Harp:     runnerEnv["CTXLOOM_SESSION_HARP"],
		SpoolTee: runnerEnv[EnvRunSpoolTee] == "true",
		// The CUTOVER stamp, read the same way for the same reason: a run cut
		// over on the coordinator only would deliver nothing, and that is a
		// failure every test using this fake should be able to catch.
		SpoolDelivery:      runnerEnv[EnvRunSpoolDelivery] == "true",
		SpoolSweepInterval: sweepInterval,
	})
	if err != nil {
		cancel()
		return nil, err
	}
	host.BindHome(home)
	kill := func() {
		cancel()
		home.crash()
	}
	s.mu.Lock()
	s.kills = append(s.kills, kill)
	s.engineHomes = append(s.engineHomes, home)
	s.mu.Unlock()
	if workDir == "" {
		workDir = "/work"
	}
	spawnedEnv := env
	if len(engineEnv) > 0 {
		spawnedEnv = make(map[string]string, len(env)+len(engineEnv))
		maps.Copy(spawnedEnv, env)
		maps.Copy(spawnedEnv, engineEnv)
	}
	return &EngineSpawn{
		WorkDir:    workDir,
		Env:        spawnedEnv,
		Model:      "test-model",
		Kill:       kill,
		StderrTail: s.engineStderrTail,
	}, nil
}

// engineHome returns the i-th spawned runner-side Home, nil if unspawned.
func (s *fakeSpawner) engineHome(i int) *Home {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i >= len(s.engineHomes) {
		return nil
	}
	return s.engineHomes[i]
}

// chat returns the i-th scripted (StartRun-path) engine, nil if unspawned.
func (s *fakeSpawner) chat(i int) *scriptedChat {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i >= len(s.chats) {
		return nil
	}
	return s.chats[i]
}

// chatCount reports how many StartRun-path engines were spawned.
func (s *fakeSpawner) chatCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.chats)
}

// killEngine simulates an EXTERNAL death of the i-th StartRun-path engine
// (docker stop / kill -9): context torn down, connection severed, nothing
// clean sent.
func (s *fakeSpawner) killEngine(i int) {
	s.mu.Lock()
	kill := s.kills[i]
	s.mu.Unlock()
	kill()
}

// lastPerm returns the permission the most recent launch carried.
func (s *fakeSpawner) lastPerm() agent.PermissionMode {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.perms) == 0 {
		return agent.PermissionDefault
	}
	return s.perms[len(s.perms)-1]
}

// lastWorkspace returns the SpawnPlan.Workspace the most recent Launch/
// StartEngine call carried (GAP 2 threading proof).
func (s *fakeSpawner) lastWorkspace() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.workspaces) == 0 {
		return ""
	}
	return s.workspaces[len(s.workspaces)-1]
}

// lastDirtyTreeHandler returns the SpawnPlan.DirtyTreeHandler the most
// recent Launch/StartEngine call carried.
func (s *fakeSpawner) lastDirtyTreeHandler() operations.DirtyTreeHandler {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.dirtyTreeHandlers) == 0 {
		return ""
	}
	return s.dirtyTreeHandlers[len(s.dirtyTreeHandlers)-1]
}

func (s *fakeSpawner) ResumeContext(_ context.Context, plan *SpawnPlan, _ string) string {
	return plan.Context
}

// awaitResolveGate parks Resolve, WITHOUT holding s.mu (a gated resolve that
// held the fake's own mutex would wedge every other observation the test
// wants to make while it is parked).
func (s *fakeSpawner) awaitResolveGate(ctx context.Context, agentName string) {
	s.mu.Lock()
	entered, gate := s.resolveEntered, s.resolveGate
	s.mu.Unlock()
	if entered != nil {
		select {
		case entered <- agentName:
		default:
		}
	}
	if gate == nil {
		return
	}
	select {
	case <-gate:
	case <-ctx.Done():
	}
}

func (s *fakeSpawner) RecordEngineVersion(ctx context.Context, harp, _ string) {
	s.mu.Lock()
	s.versionProbes = append(s.versionProbes, harp)
	entered, gate := s.versionProbeEntered, s.versionProbeGate
	s.mu.Unlock()
	if entered != nil {
		select {
		case entered <- harp:
		default:
		}
	}
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
		}
	}
}

// probedVersions returns the harps RecordEngineVersion has been called for.
func (s *fakeSpawner) probedVersions() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.versionProbes...)
}

// MarkSessionEnded records the harps whose session accounting was ended —
// the Spawner's ONLY release primitive, and therefore what an aborted spawn
// must call to give back a harp AssignSession already committed.
func (s *fakeSpawner) MarkSessionEnded(harp string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessionsEnded = append(s.sessionsEnded, harp)
}

// endedSessions is MarkSessionEnded's recording, in call order.
func (s *fakeSpawner) endedSessions() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.sessionsEnded...)
}

// assignedSessions is AssignSession's recording, in call order.
func (s *fakeSpawner) assignedSessions() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.assigned...)
}

func (s *fakeSpawner) spawnCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.engines)
}

func (s *fakeSpawner) engine(i int) *fakeEngine {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i >= len(s.engines) {
		return nil
	}
	return s.engines[i]
}

// newTestCoordinator builds a coordinator over a fake spawner in a temp state
// dir, serving loopback listeners (so host children resolve a reach-back URL).
// The Clock defaults to real time unless overridden. Runs at the package
// default concurrency cap (agentConcurrencyCap, children.go) — tests pinning
// D4 QUEUEING behavior at a SPECIFIC cap (most commonly 1, to exercise "past
// the cap enqueues") must use newTestCoordinatorCap instead: the default is a
// configurable resource ceiling, not a correctness serializer, and is not
// guaranteed to be 1.
func newTestCoordinator(t *testing.T, sp Spawner, clock func() time.Time) *Coordinator {
	t.Helper()
	return newTestCoordinatorCap(t, sp, clock, 0)
}

// newTestCoordinatorCap is newTestCoordinator with an explicit concurrency cap
// (Options.ConcurrencyCap; <= 0 keeps the package default).
func newTestCoordinatorCap(t *testing.T, sp Spawner, clock func() time.Time, cap int) *Coordinator {
	t.Helper()
	return newTestCoordinatorOpts(t, sp, clock, cap, 0)
}

// newTestCoordinatorDepthCap is newTestCoordinator with an explicit
// delegation-depth cap (Options.Depth; <= 0 keeps the package default,
// currently 1). Tests exercising a specific depth cap (raising it above the
// default to permit a deeper tree, or pinning it at 1 to assert flat fan-out)
// use this instead of relying on the package default's current value.
func newTestCoordinatorDepthCap(t *testing.T, sp Spawner, clock func() time.Time, depthCap int) *Coordinator {
	t.Helper()
	return newTestCoordinatorOpts(t, sp, clock, 0, depthCap)
}

// newTestCoordinatorOpts is the shared constructor both cap-specific helpers
// above wrap.
func newTestCoordinatorOpts(t *testing.T, sp Spawner, clock func() time.Time, concurrencyCap, depthCap int) *Coordinator {
	t.Helper()
	c, err := New(Options{
		ProjectDir:     t.TempDir(),
		StateDir:       t.TempDir(),
		Spawner:        sp,
		Clock:          clock,
		ConcurrencyCap: concurrencyCap,
		Depth:          depthCap,
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	if err := c.Serve(); err != nil {
		t.Fatalf("serve coordinator: %v", err)
	}
	t.Cleanup(c.Close)
	return c
}

// recvKind drains the OWNER's mailbox for up to wait and returns only the
// messages of the given kind.
//
// Selecting by kind (rather than assuming the mailbox holds one thing) is
// required because of the automatic result bridge
// (children.go's bridgeTurnResult): every child turn also delivers the
// child's own output to its parent as `kind: "result"`, so a test pinning a
// SPECIFIC conversation — an approval relay, an injection mirror, one
// agent_send — must say which conversation it means.
func recvKind(t *testing.T, c *Coordinator, kind string, wait time.Duration) []Message {
	t.Helper()
	return recvWhere(t, c, func(m Message) bool { return m.Kind == kind }, wait)
}

// recvBody is recvKind's sibling for tests whose selector is the message TEXT
// (the scripted engines bridge their own "ok" as kind "result", so a test
// pinning a specific result body cannot select on kind alone).
func recvBody(t *testing.T, c *Coordinator, body string, wait time.Duration) []Message {
	t.Helper()
	return recvWhere(t, c, func(m Message) bool { return m.Body == body }, wait)
}

// recvWhere drains the owner's mailbox until at least one message satisfies
// keep, or wait elapses.
func recvWhere(t *testing.T, c *Coordinator, keep func(Message) bool, wait time.Duration) []Message {
	t.Helper()
	deadline := time.Now().Add(wait)
	var out []Message
	for {
		msgs, err := c.AgentRecv(context.Background(), ownerIdentity(), 10*time.Millisecond)
		if err == nil {
			for _, m := range msgs {
				if keep(m) {
					out = append(out, m)
				}
			}
			if len(out) > 0 {
				return out
			}
		}
		if time.Now().After(deadline) {
			return out
		}
	}
}

// assertNoMailKind drains the owner's mailbox for window and fails if any
// message of kind ever appears — the "it was never relayed" assertion, now
// that the mailbox legitimately carries bridged results too.
func assertNoMailKind(t *testing.T, c *Coordinator, kind string, window time.Duration) {
	t.Helper()
	if got := recvKind(t, c, kind, window); len(got) > 0 {
		t.Fatalf("expected no %q mail, got %d (first body: %q)", kind, len(got), got[0].Body)
	}
}

// mkTempDir returns a stable temp dir NOT auto-cleaned per sub-coordinator, so
// adoption tests can relaunch coordinators over the SAME state dir.
func mkTempDir(t *testing.T) string { return t.TempDir() }

// newTestCoordinatorAt builds a mailbox-only test coordinator over a FIXED
// state dir (for adoption/restart tests) with a no-op spawner and no
// listeners — the mailbox verbs need neither. The caller closes it explicitly
// (adoption tests relaunch over the same dir).
func newTestCoordinatorAt(t *testing.T, stateDir string) *Coordinator {
	t.Helper()
	c, err := New(Options{
		ProjectDir: stateDir,
		StateDir:   stateDir,
		Spawner:    newFakeSpawner(nil, nil),
		Clock:      nil,
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	return c
}
