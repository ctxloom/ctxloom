package coord

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// fakeEngine is one scripted child engine conversation. Turn texts are
// recorded as received; each turn completes automatically unless gated
// (turnGate non-nil — the test releases turns one by one), and the stream can
// end itself after endAfterTurns turns (an engine that exits).
type fakeEngine struct {
	mu            sync.Mutex
	texts         []string
	gotEnv        map[string]string
	turnGate      chan struct{}
	endAfterTurns int
}

func (f *fakeEngine) recordedTexts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.texts...)
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
func (f *fakeEngine) launch(ctx context.Context, contextText string, env map[string]string) *operations.AgentChatLaunch {
	f.mu.Lock()
	f.gotEnv = env
	f.mu.Unlock()
	in := make(chan agent.ChatMessage)
	events := make(chan agent.ChatEvent)
	errs := make(chan error, 1)
	turnCtx, cancel := context.WithCancel(ctx)
	first := true
	go func() {
		defer close(errs)
		defer close(events)
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
	return &operations.AgentChatLaunch{In: in, Events: events, Errs: errs, Close: cancel}
}

// fakeSpawner is the hermetic Spawner: no config, no engines, no isolation.
// It mints deterministic harps and scripts one fakeEngine per launch.
type fakeSpawner struct {
	mu        sync.Mutex
	next      func() *fakeEngine
	engines   []*fakeEngine
	harpSeq   int
	agents    map[string]fakeAgent // agent name → resolved plan bits
	resolved  []string
	assigned  []string
	perms     []agent.PermissionMode
}

type fakeAgent struct {
	perm     string // headless permission enum; "" refuses (D3)
	runtime  string
	profiles []string
	unknown  bool
}

func newFakeSpawner(agents map[string]fakeAgent, next func() *fakeEngine) *fakeSpawner {
	if next == nil {
		next = func() *fakeEngine { return &fakeEngine{} }
	}
	return &fakeSpawner{next: next, agents: agents}
}

func (s *fakeSpawner) Resolve(_ context.Context, agentName string) (*SpawnPlan, error) {
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
		mark := strictnessCheckpoint()
		perm, degraded = headlessSafePermission(agentName, a.perm)
		return findingsError(mark)
	}(); gerr != nil {
		return nil, gerr
	}
	s.resolved = append(s.resolved, agentName)
	return &SpawnPlan{
		AgentName: agentName,
		Backend:   "mock",
		Label:     "fast",
		Profiles:  a.profiles,
		Runtime:   a.runtime,
		Context:   "FRAG-ONE",
		Perm:      perm,
		Degraded:  degraded,
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

func (s *fakeSpawner) Launch(ctx context.Context, plan *SpawnPlan, contextText string, env map[string]string) (*operations.AgentChatLaunch, error) {
	s.mu.Lock()
	e := s.next()
	s.engines = append(s.engines, e)
	s.perms = append(s.perms, plan.Perm)
	s.mu.Unlock()
	return e.launch(ctx, contextText, env), nil
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

func (s *fakeSpawner) ResumeContext(_ context.Context, plan *SpawnPlan, _ string) string {
	return plan.Context
}

func (s *fakeSpawner) MarkSessionEnded(string) {}

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
// The Clock defaults to real time unless overridden.
func newTestCoordinator(t *testing.T, sp Spawner, clock func() time.Time) *Coordinator {
	t.Helper()
	c, err := New(Options{
		ProjectDir: t.TempDir(),
		StateDir:   t.TempDir(),
		Spawner:    sp,
		Clock:      clock,
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	if err := c.Serve(nil); err != nil {
		t.Fatalf("serve coordinator: %v", err)
	}
	t.Cleanup(c.Close)
	return c
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
