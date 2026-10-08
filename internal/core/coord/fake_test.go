package coord

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/spf13/afero"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/launch/launchtest"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/core/spool"
	"github.com/ctxloom/ctxloom/internal/testsupport/scriptedchat"
)

// fakeSpawner is the hermetic Spawner: no config, no engines, no isolation.
// It mints deterministic harps and scripts one fakeEngine per launch.
type fakeSpawner struct {
	// ownerLossWindow is every runner half's HomeConfig.OwnerLossWindow (0 =
	// the runner's default).
	ownerLossWindow time.Duration
	// degraded is the posture the fake resolves children under, the way the
	// production spawner reads its composition's strictness.Mode.
	degraded bool
	mu       sync.Mutex
	harpSeq  int
	agents   map[string]fakeAgent // agent name → resolved plan bits
	resolved []string
	assigned []string
	// identities records each ResolveLaunch's start.Identity, in call order:
	// what the child's engine env is stamped from.
	identities []Identity
	// sessionsEnded records each MarkSessionEnded call's harp, in call order.
	sessionsEnded []string
	// native stands in for each harp's sessions.Entry native session key:
	// what BindNativeSession writes and NativeSession reads.
	native map[string]string
	// dropBinds discards every BindNativeSession, so the harp holds no
	// resume key even after its engine announced one: a forced state, used to
	// reach the no-key fallback that an engine dying before it announces
	// leaves.
	dropBinds bool
	// launchErr, when set, fails every legacy Launch with it — a child that
	// is admitted (it holds an execution slot) and then dies at standup,
	// which is the shape that separates "queued behind the cap" from
	// "started and failed".
	launchErr error
	// startGrants records, per run, the grants its StartRun carried as the
	// runner received them.
	startGrants map[string][]string
	perms       []string
	// nextChat scripts the MIGRATED (StartRun) path's engine; StartEngine
	// spawns a REAL runner half (Home + EngineHost over the coordinator's
	// live gRPC listeners) around it. chats/kills record per spawn.
	nextChat func() *scriptedChat
	chats    []*scriptedChat
	// credentialFor, when set, names each agent's credential source on the
	// launch it resolves (launch.Cell.Credential), as the cells adapter does
	// from the credentials it resolved.
	credentialFor func(agentName string) engine.Credentials
	// secretsDir and secretAgents, when set, give each named agent's run a
	// real secrets file under secretsDir holding its credential, named on
	// its cell as a container's is (Placement.SecretFiles, Cell.SecretsFile):
	// its runner reads the file at every turn.
	secretsDir   string
	secretAgents map[string]bool
	kills        []func()
	// killHook, when set, is called with the spawn's index as its engine's
	// Kill begins, before the runner is torn down — the seam that holds a
	// production teardown's latency (a docker stop takes seconds) open, so a
	// test can act while the coordinator has ended a run whose runner is
	// still connected.
	killHook func(i int)
	// released[i] closes when the i-th engine's Kill fired — the seam a
	// production child's container teardown hangs off. A test that must
	// prove a stop RELEASED the child watches this rather than inferring it
	// from the roster.
	released []chan struct{}
	// nextBackend, when set, supplies a REAL engine instance
	// for the MIGRATED path instead of nextChat's scripted double — the
	// seam a live-path reproduction uses to put a genuine driver, spawning a
	// genuine engine subprocess, under the genuine
	// EngineHost/Home/Coordinator stack. engineWorkDir
	// and engineEnv ride into the HarnessSpec the runner decodes, so that
	// subprocess gets a real cwd and its own marker env.
	nextBackend   func() engine.Instance
	engineWorkDir string
	engineEnv     map[string]string
	// spoolSweepInterval is handed to every in-process Home this fake builds
	// (HomeConfig.SpoolSweepInterval). A cutover test that has to prove the
	// SWEEP recovers a dropped doorbell sets it small; everything else leaves
	// it zero and gets the production cadence, which no test waits on.
	spoolSweepInterval time.Duration
	// engineHomes records each StartEngine call's runner-side Home, in spawn
	// order — the seam a test needs to drive a child's runner half.
	engineHomes []TestHome
	// attachWaiting, when set, is signalled by awaitCutoverChild as it begins
	// waiting for the child's run channel to attach — the seam that lets a
	// test holding the attach (Coordinator.attachRunHook) release it only once
	// the fixture is committed to waiting for it.
	attachWaiting chan struct{}
	// engineStderrTail, when set, is threaded onto each EngineSpawn.StderrTail
	// — the runner's captured stderr tail. It stands in for a docker-direct
	// runner's ring (the container's streamed stderr): a runner-loss test uses
	// it to prove terminateRun surfaces the container's dying words when the
	// engine emitted no FAILED RunCompleted.
	engineStderrTail func() string
	// workspaces records each Launch/StartEngine call's plan.Workspace, in
	// spawn order — GAP 2's threading proof (AgentRun -> SpawnPlan.Workspace
	// -> here), without needing real isolation machinery.
	workspaces []launch.WorkspaceAxis
	// dirtyTreeHandlers records each Launch/StartEngine call's
	// plan.DirtyTreeHandler, in spawn order — the identical threading proof
	// as workspaces, for AgentRun's dirty_tree_handler override.
	dirtyTreeHandlers []launch.DirtyTreeHandler
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
	// startGate, when non-nil, holds Start — the runner's spawn, and so its
	// dial-home — until the test closes it. It is the POST-registration slow
	// step: the run is enqueued, its slot held and the roster already says
	// executing, but no runner exists yet. That is the span a loaded host
	// stretches, and the one a test must be able to stand a child in.
	startGate chan struct{}
	// launches records every launch ResolveLaunch resolved, in order;
	// endpointSeq numbers the endpoint each resolve mints, as Resolve mints
	// one per launch.
	launches    []launch.Launch
	endpointSeq int
	// refuseBinds is how many launches the runner tail refuses with
	// delivery.ErrEndpointUnavailable before binding normally.
	refuseBinds int
	// refuse, when set, is asked about every runner request before the
	// engine host handles it: a non-nil answer is the runner's refusal of
	// that request, and nothing of it is executed.
	refuse func(req *agentcoordpb.RunnerRequest) error
	// bindHold, when non-nil, holds every launch inside the runner tail's
	// bind seam — StartRun is on the wire, the runner is up, the reply has
	// not been sent — until it is closed or the spawn is killed. bindEntered
	// (buffered) is signalled each time a launch reaches the hold.
	bindHold    chan struct{}
	bindEntered chan struct{}
}

type fakeAgent struct {
	perm string // the declared mode; "" takes the default
	// mayDelegate is the binding's may_delegate (SpawnPlan.MayDelegate).
	mayDelegate []string
	runtime     launch.RuntimeAxis
	profiles    []string
	unknown     bool
	// backend is the SpawnPlan.Backend this agent resolves to (rides into
	// HarnessSpec.harness on the StartRun path). Empty defaults to "mock" —
	// most tests don't care and the coordinator's own mechanics are
	// backend-agnostic (C3 recon); tests pinning per-backend parity (e.g.
	// TestStartRun_BackendParity) set it explicitly.
	backend string
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

// newFakeSpawner builds the fake for t, and ends every runner half it starts
// when t ends. A runner half is detached from the coordinator on purpose (a
// restarted coordinator re-adopts it), so nothing else ends it: one left
// running redials an absent coordinator for the whole owner-loss window and
// warns through clidiag's PROCESS-WIDE sink — into whatever unsynchronised
// buffer a later test installed there.
func newFakeSpawner(t testing.TB, agents map[string]fakeAgent, next func() *scriptedChat) *fakeSpawner {
	s := &fakeSpawner{nextChat: next, agents: agents}
	t.Cleanup(s.killAll)
	return s
}

// killAll kills every runner half this fake started (see newFakeSpawner).
func (s *fakeSpawner) killAll() {
	s.mu.Lock()
	kills := slices.Clone(s.kills)
	s.mu.Unlock()
	for _, kill := range kills {
		kill()
	}
}

func (s *fakeSpawner) Resolve(ctx context.Context, agentName string) (*SpawnPlan, error) {
	s.awaitResolveGate(ctx, agentName)
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.agents[agentName]
	if !ok || a.unknown {
		return nil, fmt.Errorf("agent %q not found", agentName)
	}
	s.resolved = append(s.resolved, agentName)
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
		Permission:  a.perm,
		MayDelegate: a.mayDelegate,
		MCPServers:  a.mcpServers,
		ResumeMode:  resumeMode,
	}, nil
}

// floorChild is the fake's stand-in for the launch resolver's floor on a
// delegated child (launch.Resolve's permission floor): a declared posture
// passes as declared, none takes the host default, and only a declaration
// that does not parse is refused — or dropped to plan under --degraded. The
// fake applies it where the real spawner's StartEngine resolves the launch,
// so a test observes the outcome at the same point production reaches it.
func floorChild(degraded bool, agentName, declared string) (string, error) {
	if declared == "" {
		return "default", nil
	}
	if slices.Contains([]string{"default", "acceptEdits", "plan", "bypass"}, declared) {
		return declared, nil
	}
	if degraded {
		return "plan", nil
	}
	return "", fmt.Errorf("%w: agent %q declares permissions %q, which is not a posture", launch.ErrPermissionUnhonoured, agentName, declared)
}

func (s *fakeSpawner) AssignSession(_, _ string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.harpSeq++
	harp := fmt.Sprintf("child-harp-%d", s.harpSeq)
	s.assigned = append(s.assigned, harp)
	return harp, nil
}

// ResolveLaunch resolves the child's launch the way the production spawner
// does — the floor applied, the axes and handler recorded, the package (the
// context every conformance test looks for) carried inline, and the MCP
// endpoint minted fresh for every launch.
func (s *fakeSpawner) ResolveLaunch(ctx context.Context, plan *SpawnPlan, start SpawnStart) (Resolved, error) {
	perm, err := floorChild(s.degraded, plan.AgentName, plan.Permission)
	if err != nil {
		return Resolved{}, err
	}
	// The child's engine env is what the launch stamps: the identity
	// carriers. The fake stamps the same two.
	env := map[string]string{sessions.EnvHarp: start.Identity.Harp}
	if start.Identity.Project != "" {
		env[sessions.EnvProjectID] = start.Identity.Project
	}
	s.mu.Lock()
	s.identities = append(s.identities, start.Identity)
	s.perms = append(s.perms, perm)
	s.workspaces = append(s.workspaces, plan.Workspace)
	s.dirtyTreeHandlers = append(s.dirtyTreeHandlers, plan.DirtyTreeHandler)
	s.endpointSeq++
	ep := sessions.Endpoint{URL: fmt.Sprintf("http://127.0.0.1:%d/mcp", 40000+s.endpointSeq), Credential: fmt.Sprintf("bearer-%d", s.endpointSeq)}
	workDir := s.engineWorkDir
	engineEnv := s.engineEnv
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
	enc, err := composite.Encode(composite.Package{Context: composite.Context{Text: "FRAG-ONE"}})
	if err != nil {
		return Resolved{}, err
	}
	carrier, err := composite.Inline{}.Carry(ctx, enc)
	if err != nil {
		return Resolved{}, err
	}
	l := launch.Launch{
		Identity:   start.Identity,
		Engine:     engine.Name(plan.Backend),
		Label:      engine.LabelConfig{Label: plan.Label, Model: "test-model"},
		Mode:       engine.Structured,
		Permission: engine.PermissionPolicy{Posture: engine.Posture{Engine: engine.Name(plan.Backend), Document: map[string]any{"mode": perm}}, Sandbox: engine.SandboxFull},
		Axes:       launch.Axes{Workspace: plan.Workspace, Runtime: plan.Runtime},
		Cell:       launch.Cell{Placement: launch.Placement{Paths: present.OnHost(present.Paths{ProjectRoot: present.Root{Host: workDir}}), Env: spawnedEnv}, Cleanup: func() error { return nil }},
		Package:    carrier,
		MCP:        ep,
		Prompt:     start.Prompt,
		Resume:     sessions.ResumeRef{Harp: start.Identity.Harp, NativeKey: start.ResumeKey},
	}
	creds := s.credentialOf(plan.AgentName)
	l.Cell.Credential, l.Cell.CredentialFingerprint = creds.Source("mock"), creds.Fingerprint()
	if err := s.secretsCell(&l.Cell, plan.AgentName, start.Identity.Harp, creds); err != nil {
		return Resolved{}, err
	}
	plan.Launch = l
	s.mu.Lock()
	s.launches = append(s.launches, l)
	s.mu.Unlock()
	return Resolved{Launch: l}, nil
}

// secretsCell gives agentName's cell a real secrets file holding creds, named
// as a container's is, when secretsDir and secretAgents ask for one.
func (s *fakeSpawner) secretsCell(cell *launch.Cell, agentName, harp string, creds engine.Credentials) error {
	if s.secretsDir == "" || !s.secretAgents[agentName] || len(creds.Env) == 0 {
		return nil
	}
	file := filepath.Join(s.secretsDir, harp+".env")
	b, err := sessions.EncodeSecrets(creds.Env)
	if err != nil {
		return err
	}
	if err := os.WriteFile(file, b, 0o600); err != nil {
		return err
	}
	cell.SecretFiles = make(map[string]string, len(creds.Env))
	for v := range creds.Env {
		cell.SecretFiles[v] = file
	}
	cell.SecretsFile = file
	return nil
}

// credentialOf is agentName's credentials: credentialFor's answer, none
// without one.
func (s *fakeSpawner) credentialOf(agentName string) engine.Credentials {
	if s.credentialFor == nil {
		return engine.Credentials{}
	}
	return s.credentialFor(agentName)
}

// Start spawns the runner half for real: an in-process Home dialing the
// coordinator's live listeners with the spawn-injected trio, an EngineHost
// wired as its RunnerRequest handler, and a scripted structured driver as the
// engine. The runner's context is NOT the caller's: a runner is its own
// process and outlives the coordinator that started it (a restart re-adopts
// it); only Kill ends it. Kill models SIGKILL (docker-stop): the shared
// context dies — no RunExited, no clean teardown; the coordinator's loss
// synthesis is what must notice.
func (s *fakeSpawner) Start(ctx context.Context, l launch.Launch, reach sessions.Endpoint) (*EngineSpawn, error) {
	s.mu.Lock()
	gate := s.startGate
	s.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	runnerEnv := sessions.EncodeReach(reach, l.Identity.RunID)
	s.mu.Lock()
	var inst engine.Instance
	if s.nextBackend != nil {
		inst = s.nextBackend()
	} else {
		mk := s.nextChat
		if mk == nil {
			mk = func() *scriptedChat { return &scriptedChat{} }
		}
		sc := mk()
		sc.Mu.Lock()
		sc.GotEnv = l.Cell.Env
		sc.GotRunnerEnv = runnerEnv
		sc.Mu.Unlock()
		s.chats = append(s.chats, sc)
		inst = sc
	}
	sweepInterval := s.spoolSweepInterval
	s.mu.Unlock()

	sctx, cancel := context.WithCancel(context.Background())
	host := runnerHooks.NewEngineHost(sctx, nil, string(l.Engine), runnerEnv[EnvRunID])
	runnerHooks.BindTestRunner(host, inst, func() bool { return s.refuseBind(sctx) })
	home, err := runnerHooks.NewHome(sctx, TestHomeConfig{
		Reporter: termSink(),
		URL:      runnerEnv[EnvCoordURL],
		Token:    runnerEnv[EnvCoordCred],
		RunID:    runnerEnv[EnvRunID],
		Harness:  string(l.Engine),
		Version:  "test",
		Engine: func(req *agentcoordpb.RunnerRequest) *agentcoordpb.RunnerResponse {
			if sr := req.GetStartRun(); sr != nil {
				s.mu.Lock()
				if s.startGrants == nil {
					s.startGrants = make(map[string][]string)
				}
				s.startGrants[sr.GetRunId()] = sr.GetGrants()
				s.mu.Unlock()
			}
			s.mu.Lock()
			refuse := s.refuse
			s.mu.Unlock()
			if refuse != nil {
				if refusal := refuse(req); refusal != nil {
					resp := runnerHooks.RunnerRefusal(refusal)
					resp.RequestId = req.GetRequestId()
					return resp
				}
			}
			return host.Handle(req)
		},
		// The trio is read out of the STAMPED runner env rather than handed
		// in by the test, mirroring production's consumeCoordinatorReachBack
		// (llm_runner_common.go). The run's identity is NOT here: it arrives
		// on the Launch, and the engine host binds it as it drives.
		SpoolSweepInterval: sweepInterval,
		OwnerLossWindow:    s.ownerLossWindow,
	})
	if err != nil {
		cancel()
		return nil, err
	}
	host.BindHome(home)
	released := make(chan struct{})
	var killOnce sync.Once
	var idx int
	var hook func(int) // both set below, with kill's registration, before kill can be called
	// Once: a test's own killEngine and the end-of-test killAll both reach it.
	kill := func() {
		killOnce.Do(func() {
			if hook != nil {
				hook(idx)
			}
			cancel()
			// The runner's own teardown order: the engine host is joined
			// before the Home crashes, so no turn goroutine of the host
			// reaches the Home's spool after the Home is gone.
			host.Close()
			home.Crash()
			close(released)
		})
	}
	s.mu.Lock()
	idx, hook = len(s.kills), s.killHook
	s.kills = append(s.kills, kill)
	s.released = append(s.released, released)
	s.engineHomes = append(s.engineHomes, home)
	s.mu.Unlock()
	return &EngineSpawn{
		Kill:       kill,
		StderrTail: s.engineStderrTail,
	}, nil
}

// refuseBind is the runner tail's bind seam: while refuseBinds is positive,
// each launch the runner executes is refused with
// delivery.ErrEndpointUnavailable (the port was taken between incarnations),
// and the count goes down by one. With bindHold set it first holds the
// launch there (see bindHold); spawnCtx is the spawn's own context, which
// the fake's kill cancels before joining the engine host, so a held launch
// never outlives its runner.
func (s *fakeSpawner) refuseBind(spawnCtx context.Context) bool {
	s.mu.Lock()
	hold, entered := s.bindHold, s.bindEntered
	s.mu.Unlock()
	if hold != nil {
		if entered != nil {
			select {
			case entered <- struct{}{}:
			default:
			}
		}
		select {
		case <-hold:
		case <-spawnCtx.Done():
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.refuseBinds > 0 {
		s.refuseBinds--
		return true
	}
	return false
}

// resolvedLaunches returns every launch ResolveLaunch produced, in order.
func (s *fakeSpawner) resolvedLaunches() []launch.Launch {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]launch.Launch(nil), s.launches...)
}

// engineHome returns the i-th spawned runner-side Home, nil if unspawned.
func (s *fakeSpawner) engineHome(i int) TestHome {
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
func (s *fakeSpawner) lastPerm() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.perms) == 0 {
		return "default"
	}
	return s.perms[len(s.perms)-1]
}

// lastWorkspace returns the SpawnPlan.Workspace the most recent Launch/
// StartEngine call carried (GAP 2 threading proof).
func (s *fakeSpawner) lastWorkspace() launch.WorkspaceAxis {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.workspaces) == 0 {
		return ""
	}
	return s.workspaces[len(s.workspaces)-1]
}

// lastDirtyTreeHandler returns the SpawnPlan.DirtyTreeHandler the most
// recent Launch/StartEngine call carried.
func (s *fakeSpawner) lastDirtyTreeHandler() launch.DirtyTreeHandler {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.dirtyTreeHandlers) == 0 {
		return ""
	}
	return s.dirtyTreeHandlers[len(s.dirtyTreeHandlers)-1]
}

func (s *fakeSpawner) ResumeHistory(context.Context, string) string { return "" }

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

// BindNativeSession mirrors sessions.Store.BindSession with no transcript
// path: it binds an unbound entry and never displaces a bound one.
func (s *fakeSpawner) BindNativeSession(harp, key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.native == nil {
		s.native = map[string]string{}
	}
	if key != "" && s.native[harp] == "" && !s.dropBinds {
		s.native[harp] = key
	}
}

// NativeSession is the key harp's entry holds, "" while unbound.
func (s *fakeSpawner) NativeSession(harp string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.native[harp]
}

// rebindNativeSession displaces harp's bound key the way a SessionStart hook
// carrying a transcript path does — the entry moving without the
// coordinator having learned the new key over the wire.
func (s *fakeSpawner) rebindNativeSession(harp, key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.native == nil {
		s.native = map[string]string{}
	}
	s.native[harp] = key
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
	return s.chatCount()
}

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

// newTestCoordinatorOpts is the constructor both cap-specific helpers above
// wrap.
func newTestCoordinatorOpts(t *testing.T, sp Spawner, clock func() time.Time, concurrencyCap, depthCap int) *Coordinator {
	t.Helper()
	return newTestCoordinatorWith(t, sp, func(o *Options) {
		o.Clock = clock
		o.ConcurrencyCap = concurrencyCap
		o.Depth = depthCap
	})
}

// newTestCoordinatorWith is the shared served test coordinator, with set
// adjusting its Options before New.
func newTestCoordinatorWith(t *testing.T, sp Spawner, set func(*Options)) *Coordinator {
	t.Helper()
	teeHome(t)
	opts := Options{
		ProjectDir: t.TempDir(),
		StateDir:   t.TempDir(),
		Spawner:    sp,
		OwnerHarp:  ownerIdentity().Harp,
		Reporter:   termSink(),
	}
	set(&opts)
	c, err := New(opts)
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	if err := runnerHooks.Serve(c); err != nil {
		t.Fatalf("serve coordinator: %v", err)
	}
	t.Cleanup(func() {
		c.Close()
		// Every runner half this fake stood up is the coordinator's to kill
		// — through the run's terminal, through Close, or (a terminal that
		// landed mid-launch) by the launch itself. One left alive keeps its
		// channels, its connection and its sweep for the life of the test
		// PROCESS, which is how a -count=N run of this package used to grow
		// its heap by megabytes per pass.
		if fs, ok := sp.(*fakeSpawner); ok {
			fs.mu.Lock()
			defer fs.mu.Unlock()
			for i, h := range fs.engineHomes {
				select {
				case <-h.Done():
				default:
					t.Errorf("runner half %d (run %s) is still alive after Coordinator.Close: nothing killed it", i, h.RunID())
				}
			}
		}
	})
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
		msgs, err := ownerMail(t, c, 10*time.Millisecond)
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
	teeHome(t)
	c, err := New(Options{
		ProjectDir: stateDir,
		StateDir:   stateDir,
		Spawner:    newFakeSpawner(t, nil, nil),
		Clock:      nil,
		OwnerHarp:  ownerIdentity().Harp,
		Reporter:   termSink(),
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	return c
}

// nativeSession reads the native session key harp's session entry holds.
func nativeSession(c *Coordinator, harp string) string {
	return c.spawner.NativeSession(harp)
}

// errNoOwnerMail is spoolMail's answer when nothing reached the spool within
// the wait.
var errNoOwnerMail = errors.New("test: no mail reached the spool")

// ownerMail reads the session owner's mail (spoolMail on the owner's harp).
func ownerMail(t *testing.T, c *Coordinator, wait time.Duration) ([]Message, error) {
	t.Helper()
	return spoolMail(t, c, ownerIdentity().Harp, wait)
}

// spoolMail reads harp's mail the way the owner's turn-start hook does
// (`ctxloom hook mail-drain`): claim what waits in its in/ spool and
// acknowledge each delivered file. It retries until something is delivered or
// wait elapses — one attempt for a wait of zero — and answers errNoOwnerMail
// when nothing came.
func spoolMail(t *testing.T, c *Coordinator, harp string, wait time.Duration) ([]Message, error) {
	t.Helper()
	deadline := time.Now().Add(wait)
	for {
		res, err := spool.Claim(afero.NewOsFs(), c.mapper, harp)
		if err != nil {
			return nil, err
		}
		var out []Message
		for _, e := range res.Entries {
			msg, err := MailFromSpool(e, e.Message.FromHarp)
			if err != nil {
				return nil, err
			}
			if err := spool.Deliver(afero.NewOsFs(), c.mapper, e.Ref, e.Identity(), time.Now()); err != nil && !errors.Is(err, spool.ErrAlreadyGone) {
				return nil, err
			}
			out = append(out, msg)
		}
		if len(out) > 0 {
			return out, nil
		}
		if !time.Now().Before(deadline) {
			return nil, errNoOwnerMail
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// ownerLaunch is the test's resolved owner launch: the fields StartOwnedRun
// reads off it, and nothing a resolver would decide.
func ownerLaunch(harp, backend, label, model, workDir, perm string) launch.Launch {
	return launchtest.Structured(harp, backend, label, model, workDir, perm)
}

// ownerRun is the owner-owned run over l: the launch, its wire form, and
// whether it is the --print single turn.
func ownerRun(l launch.Launch, oneShot bool) OwnerRun {
	return OwnerRun{Launch: l, OneShot: oneShot}
}

// scriptedChat is the shared scripted engine double, under the name this
// suite has always used for it.
type scriptedChat = scriptedchat.Chat

// grantsStartedWith is the grants run's StartRun carried to its runner, and
// whether the runner received one.
func (s *fakeSpawner) grantsStartedWith(runID string) ([]string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.startGrants[runID]
	return g, ok
}
