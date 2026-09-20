// Package coordtest is the in-process RUNNER DOUBLE for tests that drive the
// production coordinator and spawner (coord.Options.Starter): each spawn
// stands up the real runner half — an EngineHost and a Home dialing the
// coordinator's live listeners with the per-spawn env a real runner process
// would read — around a real agent.StructuredChat backend. Only the process
// boundary is faked: no container, no `ctxloom llm host`, no go-plugin.
//
// It lives beside coord rather than inside it because it is built from
// coord's EXPORTED surface (NewEngineHost, NewHome, BindHome,
// RunnerCapabilities) and must be importable by every package that hosts a
// coordinator in its tests (internal/adapters/mcp foremost). coord's own in-package
// tests cannot import it — that would be a cycle — and do not need to: they
// reach unexported state and keep their own fake.
//
// The mock backend (internal/lm/backends) is the intended engine: it is a
// deterministic echo, and it is admitted for delegated children ONLY when a
// Starter like this one is injected — see prodSpawner.Resolve.
package coordtest

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/adapters/runner"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	enginepkg "github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch/launchtest"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
)

// Runners is the set of runner doubles one coordinator spawned, in spawn
// order. Its Starter is what a test hands to coord.Options.Starter.
type Runners struct {
	ctx    context.Context
	cancel context.CancelFunc

	mu      sync.Mutex
	engines []*Engine
	homes   []*coord.Home
	hosts   []*coord.EngineHost
}

// NewRunners returns an empty set; Close tears down every runner it spawned.
func NewRunners() *Runners {
	ctx, cancel := context.WithCancel(context.Background())
	return &Runners{ctx: ctx, cancel: cancel}
}

// Starter is coord.Options.Starter: the EngineStarter for one spawn, which
// stands the runner up in-process when the spawner calls it.
func (r *Runners) Starter(backend string, runnerEnv map[string]string) isolation.EngineStarter {
	return func(context.Context) (*isolation.RunnerHandle, error) {
		return r.start(backend, runnerEnv)
	}
}

func (r *Runners) start(backend string, runnerEnv map[string]string) (*isolation.RunnerHandle, error) {
	chat, ok := backends.Get(backend).(agent.StructuredChat)
	if !ok {
		return nil, fmt.Errorf("coordtest: backend %q is not a StructuredChat, so no runner can host it", backend)
	}
	engine := &Engine{inner: chat}
	rctx, cancel := context.WithCancel(r.ctx)
	host := coord.NewEngineHost(rctx, engine, backend, runnerEnv[coord.EnvRunID])
	// The runner tail over the double: the wire launch is decoded and its
	// package opened for real; delivery is a no-op (the fake spawner's cell
	// is not a directory), and the host drives the recorded chat.
	host.BindRunner(runner.Host{Deps: runner.Deps{
		Engine:     enginepkg.Name(backend),
		Inline:     composite.Inline{Max: composite.DefaultInlineMax},
		ClaimCheck: composite.ClaimCheck{Store: launchtest.MemStore{}},
		Static:     noDelivery{},
		Driver:     host,
	}})
	home, err := coord.NewHome(rctx, coord.HomeConfig{
		URL:          runnerEnv[coord.EnvCoordURL],
		Token:        runnerEnv[coord.EnvCoordCred],
		RunID:        runnerEnv[coord.EnvRunID],
		Harness:      backend,
		Version:      "coordtest",
		Engine:       host.Handle,
		Capabilities: coord.RunnerCapabilities(true),
		Harp:         runnerEnv[sessions.EnvHarp],
		Depth:        runDepth(runnerEnv),
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

func (noDelivery) Setup(context.Context, *agent.SetupRequest) error { return nil }

// Close kills every runner spawned so far.
func (r *Runners) Close() {
	r.cancel()
	r.mu.Lock()
	homes := append([]*coord.Home(nil), r.homes...)
	hosts := append([]*coord.EngineHost(nil), r.hosts...)
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
func (r *Runners) Home(runID string) *coord.Home {
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

// Engine is one spawned engine: the real backend, with what the runner
// handed it recorded — the ChatRequest StartRun composed (permissions,
// workdir, MCP servers) and every turn text, in order.
type Engine struct {
	inner agent.StructuredChat

	mu      sync.Mutex
	req     agent.ChatRequest
	gotChat bool
	texts   []string
}

// Chat records the request and each turn's text, then lets the real backend
// answer.
func (e *Engine) Chat(ctx context.Context, req agent.ChatRequest, in <-chan agent.ChatMessage, out chan<- agent.ChatEvent) error {
	e.mu.Lock()
	e.req = req
	e.gotChat = true
	e.mu.Unlock()
	tee := make(chan agent.ChatMessage)
	go func() {
		defer close(tee)
		for msg := range in {
			if msg.Text != "" {
				e.mu.Lock()
				e.texts = append(e.texts, msg.Text)
				e.mu.Unlock()
			}
			select {
			case tee <- msg:
			case <-ctx.Done():
				return
			}
		}
	}()
	return e.inner.Chat(ctx, req, tee, out)
}

// Request returns the ChatRequest the runner composed, and whether Chat has
// been called at all.
func (e *Engine) Request() (agent.ChatRequest, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.req, e.gotChat
}

// Texts returns every turn text the engine received, in order.
func (e *Engine) Texts() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.texts...)
}

// runDepth reads the stamped depth the way the production runner does: an
// unparseable stamp is depth 0.
func runDepth(env map[string]string) int {
	d, err := strconv.Atoi(env[coord.EnvRunDepth])
	if err != nil {
		return 0
	}
	return d
}

// AwaitTimeout bounds the Await* helpers.
const AwaitTimeout = 10 * time.Second
