package operations

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
)

// stubEngine is a scripted engine behind a fakeRunHost: what one turn
// answers (out, or the prompt echoed, or the launch's assembled context —
// the lead a real runner puts ahead of the prompt), or the failure it
// reports. gotPrompt is the last turn's prompt; gotLaunch the launch the run
// started with.
type stubEngine struct {
	out  string
	echo bool // when true, answer with the prompt
	// exitCode and stderr are what a failing engine reports.
	exitCode int
	stderr   string
	// emitContext answers with the launch's assembled context, so a composed
	// profile-context is observable in the answer. Wins over echo/out.
	emitContext bool

	mu        sync.Mutex
	gotPrompt string
	gotLaunch *launch.Launch
	turns     int
}

func (s *stubEngine) prompt() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gotPrompt
}

func (s *stubEngine) launched() *launch.Launch {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gotLaunch
}

// fakeRunHost is an operations.RunHost whose runs are stubEngines: every
// StartOwnedRun invokes the starter (so the cell's transport is exercised)
// and every Turn answers from the engine. It records what it was asked.
type fakeRunHost struct {
	deps   launch.Deps
	engine *stubEngine
	mu     sync.Mutex
	starts int
	runIDs []string
}

func (h *fakeRunHost) Owner() coord.Identity { return coord.Identity{Harp: "owner-harp"} }

func (h *fakeRunHost) StartOwnedRun(ctx context.Context, _ coord.Identity, spec coord.OwnerRun, start coord.OwnedRunStarter, _ string) (*coord.RunOutcome, error) {
	if _, _, err := start(ctx, map[string]string{"CTXLOOM_COORD_CRED": "cred"}); err != nil {
		return nil, err
	}
	l := spec.Launch
	h.mu.Lock()
	h.starts++
	runID := fmt.Sprintf("run-%d", h.starts)
	h.runIDs = append(h.runIDs, runID)
	h.mu.Unlock()
	h.engine.mu.Lock()
	h.engine.gotLaunch = &l
	h.engine.mu.Unlock()
	return &coord.RunOutcome{RunID: runID, Harp: l.Identity.Harp}, nil
}

func (h *fakeRunHost) Turn(ctx context.Context, _ string, t engine.Turn) (engine.TurnResult, error) {
	e := h.engine
	e.mu.Lock()
	e.gotPrompt = t.Prompt
	e.turns++
	l := e.gotLaunch
	e.mu.Unlock()
	if e.exitCode != 0 {
		return engine.TurnResult{}, fmt.Errorf("engine exited with code %d: %s", e.exitCode, strings.TrimSpace(e.stderr))
	}
	switch {
	case e.emitContext:
		opened, err := OpenLaunch(ctx, ForSession(h.deps, l.Identity.Harp), *l)
		if err != nil {
			return engine.TurnResult{}, err
		}
		return engine.TurnResult{Answer: opened.Package.Context.Text}, nil
	case e.echo:
		return engine.TurnResult{Answer: t.Prompt}, nil
	default:
		return engine.TurnResult{Answer: e.out}, nil
	}
}

// hostsFor is the RunHosts a test hands StartOneShot: one fakeRunHost over
// deps and the scripted engine.
func hostsFor(deps launch.Deps, e *stubEngine) (*fakeRunHost, RunHosts) {
	h := &fakeRunHost{deps: deps, engine: e}
	return h, RunHostFunc(func(context.Context, string, string) (RunHost, error) { return h, nil })
}
