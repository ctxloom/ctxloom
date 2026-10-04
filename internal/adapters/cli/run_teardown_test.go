package cli

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/coordgrpc"
	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/adapters/runner"
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/launch/launchtest"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/core/spool"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/testsupport/coordharness"
	"github.com/ctxloom/ctxloom/internal/testsupport/scriptedchat"
	"github.com/ctxloom/ctxloom/internal/testsupport/spooltest"
)

// A session exit must deliver a live child's terminal notice to its owner:
// the parent always learns of a child death. Coordinator.Close is the HARD
// teardown and refuses a late spool write, so the notice only lands if the
// teardown closure drains the child before it closes. Driven through the
// real closure runState.hostCoordinator returns, with a child whose runner is
// an in-process runner.Home dialing the coordinator's live listeners.
func TestHostCoordinatorTeardown_DeliversALiveChildsTerminalNoticeBeforeClose(t *testing.T) {
	testsupport.Isolate(t)
	spooltest.TeeHome(t)
	projectDir := t.TempDir()
	testApp(t)

	sp := &liveChildSpawner{}
	prev := theComposition.NewCoordinator
	theComposition.NewCoordinator = func(app *operations.App, opts coord.Options) (*coord.Coordinator, error) {
		opts.Spawner = sp
		return prev(app, opts)
	}
	t.Cleanup(func() { theComposition.NewCoordinator = prev })

	const owner = "teardown-owner"
	st := &runState{workDir: projectDir, activeHarp: owner}
	teardown := st.hostCoordinator()
	require.NotNil(t, st.sessionCoord, "precondition: the session hosts a coordinator")
	t.Cleanup(sp.killAll)

	res, err := st.sessionCoord.Spawn(context.Background(), coord.Identity{Harp: owner}, coord.SpawnRequest{Agent: "worker", Prompt: "work"})
	require.NoError(t, err)
	require.False(t, res.Queued, "precondition: the child is admitted, not queued behind a cap")

	teardown()

	var exited []string
	for _, e := range spooltest.Entries(t, owner, spool.DirIn) {
		if e.Message != nil && e.Message.Kind == coord.KindExited {
			exited = append(exited, e.Message.FromHarp)
		}
	}
	require.Contains(t, exited, res.Harp,
		"the child's terminal notice must reach its owner before the coordinator closes")
}

// liveChildSpawner launches each child as an in-process runner: a
// runner.Home dialing the coordinator with the spawn-injected trio, driving
// a scripted engine that answers every turn at once.
type liveChildSpawner struct {
	coordharness.NopSpawner
	mu    sync.Mutex
	seq   int
	kills []func()
}

func (s *liveChildSpawner) Resolve(_ context.Context, agentName string) (*coord.SpawnPlan, error) {
	return &coord.SpawnPlan{AgentName: agentName, Backend: "mock", Label: "fast", Permission: "plan", ResumeMode: coord.ResumeModePersistent}, nil
}

func (s *liveChildSpawner) AssignSession(_, _ string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	return fmt.Sprintf("teardown-child-%d", s.seq), nil
}

func (s *liveChildSpawner) ResolveLaunch(ctx context.Context, plan *coord.SpawnPlan, start coord.SpawnStart) (coord.Resolved, error) {
	enc, err := composite.Encode(composite.Package{Context: composite.Context{Text: "CTX"}})
	if err != nil {
		return coord.Resolved{}, err
	}
	carrier, err := composite.Inline{}.Carry(ctx, enc)
	if err != nil {
		return coord.Resolved{}, err
	}
	l := launch.Launch{
		Identity:   start.Identity,
		Engine:     engine.Name(plan.Backend),
		Label:      engine.LabelConfig{Label: plan.Label, Model: "test-model"},
		Mode:       engine.Structured,
		Permission: engine.PermissionPolicy{Posture: engine.Posture{Engine: engine.Name(plan.Backend), Document: map[string]any{"mode": plan.Permission}}, Sandbox: engine.SandboxFull},
		Axes:       launch.Axes{Workspace: plan.Workspace, Runtime: plan.Runtime},
		Cell:       launch.Cell{Placement: launch.Placement{Paths: present.OnHost(present.Paths{ProjectRoot: present.Root{Host: "/work"}}), Env: map[string]string{sessions.EnvHarp: start.Identity.Harp}}, Cleanup: func() error { return nil }},
		Package:    carrier,
		Prompt:     start.Prompt,
		Resume:     sessions.ResumeRef{Harp: start.Identity.Harp, NativeKey: start.ResumeKey},
	}
	plan.Launch = l
	return coord.Resolved{Launch: l}, nil
}

func (s *liveChildSpawner) Start(_ context.Context, l launch.Launch, reach sessions.Endpoint) (*coord.EngineSpawn, error) {
	env := sessions.EncodeReach(reach, l.Identity.RunID)
	sctx, cancel := context.WithCancel(context.Background())
	host := runner.NewEngineHost(sctx, coordharness.Sink(), string(l.Engine), env[sessions.EnvRunID])
	host.BindRunner(scriptedRunner{eh: host, inst: &scriptedchat.Chat{}})
	home, err := runner.NewHome(sctx, runner.HomeConfig{
		Reporter: coordharness.Sink(),
		URL:      env[sessions.EnvCoordURL],
		Token:    env[sessions.EnvCoordCred],
		RunID:    env[sessions.EnvRunID],
		Harness:  string(l.Engine),
		Version:  "test",
		Engine:   func(req *agentcoordpb.RunnerRequest) *agentcoordpb.RunnerResponse { return host.Handle(req) },
	})
	if err != nil {
		cancel()
		return nil, err
	}
	host.BindHome(home)
	var once sync.Once
	kill := func() {
		once.Do(func() {
			cancel()
			host.Close()
			home.Crash()
		})
	}
	s.mu.Lock()
	s.kills = append(s.kills, kill)
	s.mu.Unlock()
	return &coord.EngineSpawn{Kill: kill}, nil
}

func (s *liveChildSpawner) killAll() {
	s.mu.Lock()
	kills := s.kills
	s.mu.Unlock()
	for _, k := range kills {
		k()
	}
}

// scriptedRunner executes the launch a StartRun frame carries the way the
// runner does — decode, open the package, drive — over a scripted engine.
type scriptedRunner struct {
	eh   *runner.EngineHost
	inst engine.Instance
}

func (r scriptedRunner) Execute(ctx context.Context, wire *agentcoordpb.Launch) error {
	l, err := coordgrpc.DecodeLaunch(wire)
	if err != nil {
		return err
	}
	pkg, err := composite.Open(ctx, composite.Inline{}, composite.ClaimCheck{Store: launchtest.MemStore{}}, l.Package)
	if err != nil {
		return err
	}
	ex, err := r.inst.Exec(nil)
	if err != nil {
		return err
	}
	return r.eh.Drive(ctx, runner.Turn{Launch: l, Instance: r.inst, Exec: ex, Prompt: pkg.Context.Text + "\n\n" + l.Prompt})
}
