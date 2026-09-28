// Package coordtest is the in-process RUNNER DOUBLE for tests that drive the
// production coordinator and spawner (coord.Options.Starter): each spawn
// stands up the real runner half — an EngineHost and a Home dialing the
// coordinator's live listeners with the per-spawn env a real runner process
// would read — around the real engine kind, driven one process per turn.
// Only the process boundary is faked: no container, no `ctxloom runner`.
//
// It lives beside coord rather than inside it because it is built from
// coord's EXPORTED surface (NewEngineHost, NewHome, BindHome,
// RunnerCapabilities) and must be importable by every package that hosts a
// coordinator in its tests (internal/adapters/mcp foremost). coord's own in-package
// tests cannot import it — that would be a cycle — and do not need to: they
// reach unexported state and keep their own fake.
//
// The mock engine kind (internal/engines/mock) is the intended engine: it is
// a deterministic echo, and it is admitted for delegated children ONLY when
// a Starter like this one is injected — see prodSpawner.Resolve.
package coordtest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/adapters/runner"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch/launchtest"
	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// Runners is the set of runner doubles one coordinator spawned, in spawn
// order. Its Starter is what a test hands to coord.Options.Starter.
type Runners struct {
	// Reporter is handed to every Home and EngineHost the double stands up;
	// nil discards, as a test that asserts nothing about diagnostics wants.
	Reporter report.Sink
	// Engines are the kinds the double hosts, handed in by the test: the
	// double resolves a launch's engine by name and imports no engine
	// package.
	Engines engine.Registry
	ctx     context.Context
	cancel  context.CancelFunc

	mu      sync.Mutex
	engines []*Engine
	homes   []*runner.Home
	hosts   []*runner.EngineHost
}

// NewRunners returns an empty set; Close tears down every runner it spawned.
func NewRunners(reg engine.Registry) *Runners {
	ctx, cancel := context.WithCancel(context.Background())
	return &Runners{ctx: ctx, cancel: cancel, Engines: reg}
}

// Starter is coord.Options.Starter: the EngineStarter for one spawn, which
// stands the runner up in-process when the spawner calls it.
func (r *Runners) Starter(backend string, runnerEnv map[string]string) isolation.EngineStarter {
	return func(context.Context) (*isolation.RunnerHandle, error) {
		return r.start(backend, runnerEnv)
	}
}

func (r *Runners) start(backend string, runnerEnv map[string]string) (*isolation.RunnerHandle, error) {
	kind, ok := r.Engines.Lookup(engine.Name(backend))
	if !ok {
		return nil, fmt.Errorf("coordtest: no engine kind %q is composed", backend)
	}
	engine := &Engine{}
	rctx, cancel := context.WithCancel(r.ctx)
	host := runner.NewEngineHost(rctx, r.Reporter, backend, runnerEnv[coord.EnvRunID])
	// The runner tail over the double: the wire launch is decoded and its
	// package opened for real; delivery is a no-op (the fake spawner's cell
	// is not a directory), and the host drives the real kind's driver, with
	// what the runner handed it recorded on the way.
	host.BindRunner(runner.Host{Deps: runner.Deps{
		Kind:       recordingKind{Engine: kind, rec: engine},
		Inline:     composite.Inline{Max: composite.DefaultInlineMax},
		ClaimCheck: composite.ClaimCheck{Store: launchtest.MemStore{}},
		Static:     noDelivery{},
		Driver:     recordingDriver{Driver: host, rec: engine},
		// The double hosts many runs in ONE test process: removing a
		// variable here would leak into every other test, so the removal is
		// accepted and not applied. The real runner's is os.Unsetenv.
		Unsetenv: func(string) error { return nil },
	}})
	home, err := runner.NewHome(rctx, runner.HomeConfig{
		URL:          runnerEnv[coord.EnvCoordURL],
		Token:        runnerEnv[coord.EnvCoordCred],
		RunID:        runnerEnv[coord.EnvRunID],
		Harness:      backend,
		Version:      "coordtest",
		Engine:       host.Handle,
		Capabilities: coord.RunnerCapabilities(true),
		Reporter:     r.Reporter,
	})
	if err != nil {
		cancel()
		host.Close()
		return nil, err
	}
	host.BindHome(home)

	exited := make(chan struct{})
	var once sync.Once
	kill := func() {
		once.Do(func() {
			// A killed runner process reports nothing; the coordinator
			// synthesizes the loss from the dropped channels.
			cancel()
			host.Close()
			home.Crash()
			close(exited)
		})
	}
	r.mu.Lock()
	r.engines = append(r.engines, engine)
	r.homes = append(r.homes, home)
	r.hosts = append(r.hosts, host)
	r.mu.Unlock()
	return &isolation.RunnerHandle{
		Name: "coordtest-" + runnerEnv[coord.EnvRunID],
		Kill: kill,
		Wait: func() error {
			<-exited
			return errors.New("coordtest: runner killed")
		},
		StderrTail: func() string { return "" },
	}, nil
}

// noDelivery is the double's static writer: the fake spawner's cell is no
// directory, so nothing lands; what the tests observe is the drive.
type noDelivery struct{}

func (noDelivery) Deliver(context.Context, delivery.Loadout, engine.Base, delivery.Target) (delivery.Delivered, error) {
	return delivery.Delivered{}, nil
}

// Close kills every runner spawned so far.
func (r *Runners) Close() {
	r.cancel()
	r.mu.Lock()
	homes := append([]*runner.Home(nil), r.homes...)
	hosts := append([]*runner.EngineHost(nil), r.hosts...)
	r.mu.Unlock()
	for _, h := range hosts {
		h.Close()
	}
	for _, h := range homes {
		h.Crash()
	}
}

// Engine returns the n-th spawned engine, or nil.
func (r *Runners) Engine(n int) *Engine {
	r.mu.Lock()
	defer r.mu.Unlock()
	if n >= len(r.engines) {
		return nil
	}
	return r.engines[n]
}

// Home returns the runner Home hosting runID, or nil before it was spawned.
func (r *Runners) Home(runID string) *runner.Home {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, h := range r.homes {
		if h.RunID() == runID {
			return h
		}
	}
	return nil
}

// Count reports how many runners have been spawned.
func (r *Runners) Count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.homes)
}

// Engine is one spawned engine: the real kind, with what the runner handed
// it recorded — the Request the launch composed (permissions, workdir, MCP
// servers) and every turn text, in order.
type Engine struct {
	mu       sync.Mutex
	req      Request
	gotDrive bool
	texts    []string
}

// Request is what the runner composed for the engine: the floored
// permission, the cell's working directory and the MCP servers the engine
// was pointed at.
type Request struct {
	Permissions agent.PermissionMode
	WorkDir     string
	MCPServers  []agent.ChatMCPServer
}

// Request returns what the runner composed, and whether the drive has been
// asked for at all.
func (e *Engine) Request() (Request, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.req, e.gotDrive
}

// Texts returns every turn text the engine received, in order.
func (e *Engine) Texts() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.texts...)
}

// recordingDriver records the turn the runner composed, then lets the
// engine host drive it.
type recordingDriver struct {
	runner.Driver
	rec *Engine
}

func (d recordingDriver) Drive(ctx context.Context, t runner.Turn) error {
	d.rec.mu.Lock()
	d.rec.req = Request{Permissions: t.Launch.Permission, WorkDir: t.Exec.WorkDir, MCPServers: t.MCPServers}
	d.rec.gotDrive = true
	d.rec.mu.Unlock()
	return d.Driver.Drive(ctx, t)
}

// recordingKind is the real kind with each instance's driver wrapped to
// record the turns it is handed.
type recordingKind struct {
	engine.Engine
	rec *Engine
}

func (k recordingKind) Instance(s engine.Session) (engine.Instance, error) {
	inst, err := k.Engine.Instance(s)
	if err != nil {
		return nil, err
	}
	return recordingInstance{Instance: inst, rec: k.rec}, nil
}

type recordingInstance struct {
	engine.Instance
	rec *Engine
}

func (i recordingInstance) Drivers() []engine.StructuredDriver {
	var out []engine.StructuredDriver
	for _, d := range i.Instance.Drivers() {
		out = append(out, recordingTurns{StructuredDriver: d, rec: i.rec})
	}
	return out
}

type recordingTurns struct {
	engine.StructuredDriver
	rec *Engine
}

func (d recordingTurns) Turn(ctx context.Context, ex engine.Exec, in engine.Turn, out chan<- engine.Event) (engine.TurnResult, error) {
	d.rec.mu.Lock()
	d.rec.texts = append(d.rec.texts, in.Prompt)
	d.rec.mu.Unlock()
	return d.StructuredDriver.Turn(ctx, ex, in, out)
}

// AwaitTimeout bounds the Await* helpers.
const AwaitTimeout = 10 * time.Second
