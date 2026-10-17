// Package livecoord is a live coordinating session for a frontend's suite: a
// REAL coord.Coordinator, served on its own loopback listener (so the
// endpoint discovery an out-of-process reader uses finds it), whose
// delegated children are REAL in-process runners — runner.EngineHost and
// runner.Home over a real RunChannel — driving a scripted engine
// (scriptedchat.Chat) per child. Only the engine process is a double; every
// hop between it and the coordinator is the production one.
package livecoord

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"

	"github.com/ctxloom/ctxloom/internal/adapters/coordgrpc"
	"github.com/ctxloom/ctxloom/internal/adapters/runner"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/launch/launchtest"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/engines/mock"
	"github.com/ctxloom/ctxloom/internal/testsupport/scriptedchat"
)

// OwnerHarp is the coordinating session's harp.
const OwnerHarp = "livecoord-owner"

// Agent is the one agent the session delegates to.
const Agent = "worker"

// Engine is the children's engine: a shipped one, so its approval codec
// judges what a grant covers.
const Engine = "claude"

// Session is the live coordinating session.
type Session struct {
	Coord *coord.Coordinator
	// ContextWindow is what each child's scripted turns report as their
	// context window.
	ContextWindow int

	mu    sync.Mutex
	chats map[string]*scriptedchat.Chat // harp → its engine
	homes map[string]*runner.Home       // run id → its runner's home
	idles map[string]int                // run id → turn ends the coordinator has broadcast
	harps map[string]string             // run id → its harp
}

// New starts a served coordinator over projectDir. The caller isolates HOME
// first (testsupport.Isolate): the coordinator writes its endpoint under it,
// and children are assigned real session-index harps there. Close runs at
// cleanup.
func New(t *testing.T, projectDir string, contextWindow int) *Session {
	t.Helper()
	s := &Session{ContextWindow: contextWindow, chats: map[string]*scriptedchat.Chat{}, homes: map[string]*runner.Home{}, idles: map[string]int{}, harps: map[string]string{}}
	c, err := coord.New(coord.Options{ProjectDir: projectDir, ProjectID: "livecoord-proj", Spawner: &spawner{s: s}, OwnerHarp: OwnerHarp})
	if err != nil {
		t.Fatalf("livecoord: new coordinator: %v", err)
	}
	if err := coordgrpc.Serve(c); err != nil {
		c.Close()
		t.Fatalf("livecoord: serve: %v", err)
	}
	t.Cleanup(c.Close)
	s.Coord = c
	_, events, cancel, _ := c.WatchRuns(nil)
	done := make(chan struct{})
	t.Cleanup(func() { close(done); cancel() })
	go s.countTurnEnds(events, done)
	return s
}

// countTurnEnds counts each run's turn ends as the coordinator broadcasts
// them, until done.
func (s *Session) countTurnEnds(events <-chan coord.Event, done <-chan struct{}) {
	for {
		select {
		case ev := <-events:
			if ce, ok := ev.Payload.(coord.CustomEvent); ok && ce.Name == coord.CustomTurnIdle {
				s.mu.Lock()
				s.idles[ev.RunID]++
				s.mu.Unlock()
			}
		case <-done:
			return
		}
	}
}

// Settle returns once harp has ended turns turns and the coordinator has
// taken the last one's end — the moment a request it asks is one of no
// turn that is still ending. The roster's idle state alone does not say
// that: until the coordinator takes a new turn's start it still shows the
// last turn's idle.
func (s *Session) Settle(t *testing.T, harp string, turns int) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if s.turnEnds(harp) >= turns && s.state(harp) == coord.StateIdle {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("livecoord: %s never settled after %d turn(s) (%d ended, state %q)", harp, turns, s.turnEnds(harp), s.state(harp))
}

// turnEnds is how many turn ends the coordinator has broadcast for harp's
// runs.
func (s *Session) turnEnds(harp string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for runID, h := range s.harps {
		if h == harp {
			n += s.idles[runID]
		}
	}
	return n
}

// runID is harp's current run ("" when the coordinator holds none).
func (s *Session) runID(harp string) string {
	for _, r := range s.Coord.ListRuns(true, "").Runs {
		if r.Agent != nil && r.Agent.AgentID == harp {
			return r.RunID
		}
	}
	return ""
}

// Delegate starts a child of the owner on prompt and returns its harp once
// its first turn has completed and its run is idle.
func (s *Session) Delegate(t *testing.T, prompt string) string {
	t.Helper()
	out, err := s.Coord.AgentRun(context.Background(), s.Coord.Owner(), Agent, prompt, "")
	if err != nil {
		t.Fatalf("livecoord: delegate: %v", err)
	}
	s.Settle(t, out.Harp, 1)
	return out.Harp
}

// state is harp's roster state ("" when the roster does not hold it).
func (s *Session) state(harp string) string {
	for _, e := range s.Coord.Roster(s.Coord.Owner()) {
		if e.Harp == harp {
			return e.State
		}
	}
	return ""
}

// Prompts is every turn text harp's engine has been handed.
func (s *Session) Prompts(harp string) []string {
	if c := s.chat(harp, false); c != nil {
		return c.RecordedTexts()
	}
	return nil
}

// FailNextTurn turns harp's next turn away with kind.
func (s *Session) FailNextTurn(harp string, kind agent.FailureKind) {
	c := s.chat(harp, true)
	c.Mu.Lock()
	defer c.Mu.Unlock()
	once := false
	c.Failed = func(engine.Exec, string) *agent.TurnFailure {
		if once {
			return nil
		}
		once = true
		return &agent.TurnFailure{Kind: kind}
	}
}

// Stop ends harp's run as its parent's agent_stop does, and returns once the
// roster shows it ended.
func (s *Session) Stop(t *testing.T, harp string) {
	t.Helper()
	if _, err := s.Coord.Stop(context.Background(), s.Coord.Owner(), coord.StopRequest{Harp: harp, Reason: "livecoord stop"}); err != nil {
		t.Fatalf("livecoord: stop %s: %v", harp, err)
	}
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if s.state(harp) == coord.StateEnded {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("livecoord: %s never ended (state %q)", harp, s.state(harp))
}

// Ask has harp's current run ask the root to allow tool over its real
// RunChannel, as the run's approval route does for its engine's permission
// prompt. The decision the run receives arrives on the channel.
func (s *Session) Ask(t *testing.T, harp, tool string) <-chan coord.ApprovalDecision {
	t.Helper()
	runID := s.runID(harp)
	s.mu.Lock()
	home := s.homes[runID]
	s.mu.Unlock()
	if home == nil {
		t.Fatalf("livecoord: %s has no live run to ask from", harp)
	}
	input, _ := json.Marshal(map[string]string{"command": "ls"})
	req := coordgrpc.ApprovalRequestToWire(coord.ApprovalRequest{Ask: engine.PermissionAsk{Tool: tool, ToolUseID: "tu-" + tool, Input: input}})
	out := make(chan coord.ApprovalDecision, 1)
	go func() {
		resp, err := home.Request(context.Background(), &agentcoordpb.AgentRequest{Kind: &agentcoordpb.AgentRequest_Approval{Approval: req}})
		if err != nil || resp.GetApproval() == nil {
			out <- coord.ApprovalDecision{Decider: agent.DeciderRefused, Message: fmt.Sprintf("livecoord: no decision: %v %v", err, resp.GetStatus())}
			return
		}
		d, err := coordgrpc.ApprovalDecisionFromWire(resp.GetApproval())
		if err != nil {
			d = coord.ApprovalDecision{Decider: agent.DeciderRefused, Message: err.Error()}
		}
		out <- d
	}()
	return out
}

// chat is harp's engine, made on first use when make is set.
func (s *Session) chat(harp string, make bool) *scriptedchat.Chat {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.chats[harp]
	if c == nil && make {
		c = &scriptedchat.Chat{ContextWindow: s.ContextWindow}
		s.chats[harp] = c
	}
	return c
}

// kind is the mock engine kind whose every instance is the child's scripted
// chat.
type kind struct {
	engine.Engine
	inst *scriptedchat.Chat
}

func (k kind) Instance(engine.Session) (engine.Instance, error) { return k.inst, nil }

// noDelivery is the children's static writer: nothing lands.
type noDelivery struct{}

func (noDelivery) Deliver(context.Context, delivery.Loadout, engine.Base, delivery.Target) (delivery.Delivered, error) {
	return delivery.Delivered{}, nil
}

func (noDelivery) Reverse(context.Context, delivery.Ownership, ...delivery.Writer) error {
	return nil
}

func (noDelivery) Preview(context.Context, delivery.Loadout, engine.Base, delivery.Target) (delivery.Previewed, error) {
	return delivery.Previewed{}, nil
}

// spawner is a coord.Spawner whose children are in-process runners over the
// coordinator's own RunChannel.
type spawner struct{ s *Session }

func (*spawner) Resolve(context.Context, string) (*coord.SpawnPlan, error) {
	return &coord.SpawnPlan{AgentName: Agent, Backend: Engine, Label: "fast", Permission: "bypass"}, nil
}

// AssignSession mints a REAL session-index harp, so the index lists the
// child and its feed resolves.
func (*spawner) AssignSession(projectDir, backend string) (string, error) {
	mgr, err := sessions.Open(nil)
	if err != nil {
		return "", err
	}
	entry, err := mgr.AssignHarp(projectDir, backend)
	if err != nil {
		return "", err
	}
	return entry.HarpName, nil
}

// ResolveLaunch composes no context: a child's turns are its prompts.
func (*spawner) ResolveLaunch(ctx context.Context, plan *coord.SpawnPlan, start coord.SpawnStart) (coord.Resolved, error) {
	enc, err := composite.Encode(composite.Package{})
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
		Permission: engine.PermissionPolicy{Posture: engine.Posture{Engine: engine.Name(plan.Backend), Document: map[string]any{"mode": "bypass"}}, Sandbox: engine.SandboxFull},
		Cell:       launch.Cell{Placement: launch.Placement{Paths: present.OnHost(present.Paths{ProjectRoot: present.Root{Host: "/work"}})}, Cleanup: func() error { return nil }},
		Package:    carrier,
		Prompt:     start.Prompt,
	}
	plan.Launch = l
	return coord.Resolved{Launch: l}, nil
}

// Start runs the child's runner in process: an EngineHost driving the
// child's scripted chat, and a Home dialling the coordinator back over the
// reach the launch carries.
func (sp *spawner) Start(_ context.Context, l launch.Launch, reach sessions.Endpoint) (*coord.EngineSpawn, error) {
	runnerEnv := sessions.EncodeReach(reach, l.Identity.RunID)
	sctx, cancel := context.WithCancel(context.Background())
	backend := string(l.Engine)
	host := runner.NewEngineHost(sctx, nil, backend, runnerEnv[coord.EnvRunID])
	host.BindRunner(&runner.Host{Deps: runner.Deps{
		Locks:      &launchtest.Locks{},
		Kind:       kind{Engine: mock.NewNamed(l.Engine), inst: sp.s.chat(l.Identity.Harp, true)},
		Inline:     composite.Inline{Max: composite.DefaultInlineMax},
		ClaimCheck: composite.ClaimCheck{Store: launchtest.MemStore{}},
		Static:     noDelivery{},
		Driver:     host,
	}})
	home, err := runner.NewHome(sctx, runner.HomeConfig{
		URL:     runnerEnv[coord.EnvCoordURL],
		Token:   runnerEnv[coord.EnvCoordCred],
		RunID:   runnerEnv[coord.EnvRunID],
		Harness: backend,
		Version: "test",
		Engine:  host.Handle,
	})
	if err != nil {
		cancel()
		return nil, err
	}
	host.BindHome(home)
	sp.s.mu.Lock()
	sp.s.homes[l.Identity.RunID] = home
	sp.s.harps[l.Identity.RunID] = l.Identity.Harp
	sp.s.mu.Unlock()
	// Kill is the runner process's death: its Home crashes with it.
	return &coord.EngineSpawn{Kill: func() { cancel(); home.Crash() }}, nil
}

func (*spawner) ResumeHistory(context.Context, string) string                { return "" }
func (*spawner) RecordEngineVersion(context.Context, string, string, string) {}
func (*spawner) MarkSessionEnded(string, string, *sessions.RunEnd)           {}
func (*spawner) BindNativeSession(string, string)                            {}
func (*spawner) NativeSession(string) string                                 { return "" }
