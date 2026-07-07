package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ctxloom/ctxloom/internal/agentbus"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/lm/isolation"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

const (
	// agentDepthEnv propagates the delegation depth onto children; agent_run
	// refuses to spawn past maxAgentDepth (the recursion guard).
	agentDepthEnv = "CTXLOOM_AGENT_DEPTH"
	maxAgentDepth = 2
	// agentTurnCap is D4: children execute serially until the isolation
	// concurrency defects land. The cap counts EXECUTING turns — a child
	// parked in agent_recv or idle at a turn boundary yields its slot.
	agentTurnCap = 1
)

// agentRunGateMu serializes each agent_run strictness window (Checkpoint →
// resolve/D3 → findingsError) AND each child's isolation-prepare window
// (PrepareAgentChat), exactly like acpOpenGateMu: strictness Marks only
// isolate SEQUENTIAL windows in one process, so an unserialized child
// isolation window interleaving with a concurrent agent_run's resolve window
// would land its findings inside — and wrongly refuse — that agent_run. The
// lock never covers the engine spawn (PreparedAgentChat.Start).
var agentRunGateMu sync.Mutex

type childState int

const (
	childQueued childState = iota
	childExecuting
	childParked
	childIdle
	childEnded
)

// childAgent is the orchestrator's record of one spawned child. state,
// slotHeld, in/close/oneshot are guarded by agentOrchestrator.mu; the in
// channel is written and closed only by the child's driver goroutine.
type childAgent struct {
	harp      string
	agentName string
	resolved  *operations.ResolvedAgent
	perm      agent.PermissionMode
	env       map[string]string // harp/bus/depth env, reused on resume

	state        childState
	slotHeld     bool
	oneshot      bool
	lastActivity time.Time // stamped on every state change and event (roster freshness)
	in           chan<- agent.ChatMessage
	close        func()
	wake         chan struct{}
	turnOutput   []string // oneshot bridging: this turn's entries
}

// label is the wire vocabulary for a child's state (agentbus.RosterEntry.State).
func (s childState) label() string {
	switch s {
	case childQueued:
		return "queued"
	case childExecuting:
		return "executing"
	case childParked:
		return "parked"
	case childIdle:
		return "idle"
	case childEnded:
		return "ended"
	}
	return "unknown"
}

// agentOrchestrator owns agent delegation for one serving session: it spawns
// children whose ctxloom agent definitions are fully honored, holds their
// chat drivers, and cohabits with the bus broker — so delivering a message
// and driving the recipient's next turn are one operation (§6a).
type agentOrchestrator struct {
	cfg        *config.Config
	parentHarp string // the session this MCP server serves ("" when unset)
	depth      int    // this process's delegation depth (0 = root coordinator)
	projectDir string
	// baseCtx bounds child lifetimes: the orchestrator process's, never one
	// tool call's — agent_run returns at enqueue while the child runs on.
	baseCtx context.Context
	factory pb.ClientFactory              // test seam; nil = default (isolation applies)
	gate    *operations.ExecutableTrustGate

	broker *agentbus.Broker
	hub    *agentbus.TapHub // live-observation seam: children's event streams, tapped by harp
	slots  *turnSlots

	mu       sync.Mutex
	children map[string]*childAgent

	sockOnce sync.Once
	sockErr  error
	sockPath string
	busSrv   *agentbus.Server
}

func newAgentOrchestrator(cfg *config.Config, parentHarp string, depth int, projectDir string) *agentOrchestrator {
	o := &agentOrchestrator{
		cfg:        cfg,
		parentHarp: parentHarp,
		depth:      depth,
		projectDir: projectDir,
		baseCtx:    context.Background(),
		children:   make(map[string]*childAgent),
		slots:      newTurnSlots(agentTurnCap),
	}
	o.broker = agentbus.New(agentbus.Hooks{OnPark: o.onChildPark, OnUnpark: o.onChildUnpark})
	o.hub = agentbus.NewTapHub()
	// The executable trust gate for the children's managed-MCP composition —
	// the same fail-closed gate the run/acp paths apply. Built here (pre-serve,
	// single-threaded) because Config's gate field is not synchronized.
	o.gate = operations.NewExecutableTrustGate(cfg)
	cfg.SetExecutableTrustGate(o.gate.Gate())
	return o
}

// agentRunOutcome is agent_run's return payload, fixed at enqueue.
type agentRunOutcome struct {
	Harp     string
	Engine   string
	Profiles []string
	Runtime  string
	Queued   bool
	Degraded []string
}

// run is agent_run: resolve the agent exactly as `run --agent` does, gate the
// permission enum (D3), mint the harp, enqueue under the D4 cap, and return
// immediately — everything after spawn rides the bus.
func (o *agentOrchestrator) run(ctx context.Context, agentName, prompt string) (*agentRunOutcome, error) {
	if agentName == "" {
		return nil, errors.New("agent_run: agent is required (a configured agent name; see `ctxloom agent list`)")
	}
	if prompt == "" {
		return nil, errors.New("agent_run: prompt is required (the child's briefing/first turn)")
	}
	childDepth := o.depth + 1
	if childDepth > maxAgentDepth {
		return nil, fmt.Errorf("agent_run: refusing to spawn at delegation depth %d (max %d): this session is itself a delegated child — route further fan-out through its coordinator", childDepth, maxAgentDepth)
	}

	// Fail-loudly gate, per spawn: resolve + D3 inside one serialized
	// strictness window (see agentRunGateMu). An unknown agent or unresolvable
	// profile is a hard error in every mode — degraded can't launch what
	// doesn't resolve; a non-headless-safe (or absent) permission enum is a
	// fatal finding, downgraded under degraded mode to the most restrictive
	// headless-safe posture.
	var (
		rs       *operations.ResolvedAgent
		perm     agent.PermissionMode
		degraded []string
	)
	if gerr := func() error {
		agentRunGateMu.Lock()
		defer agentRunGateMu.Unlock()
		mark := strictness.Checkpoint()
		var err error
		rs, err = operations.ResolveAgent(ctx, o.cfg, agentName, "")
		if err != nil {
			return err
		}
		perm, degraded = headlessSafePermission(agentName, rs.Permissions)
		return findingsError(mark)
	}(); gerr != nil {
		return nil, gerr
	}

	sock, err := o.ensureSocket()
	if err != nil {
		return nil, fmt.Errorf("agent_run: agent bus unavailable: %w", err)
	}

	// Mint the harp at ENQUEUE (host-side accounting): it is the child's bus
	// address and continuation token, so a failed mint is a hard error, not a
	// degrade.
	entry, err := operations.AssignSession(o.projectDir, rs.Backend)
	if err != nil {
		return nil, fmt.Errorf("agent_run: session accounting unavailable (the harp is the child's bus address): %w", err)
	}

	c := &childAgent{
		harp:         entry.HarpName,
		agentName:    agentName,
		resolved:     rs,
		perm:         perm,
		state:        childQueued,
		lastActivity: time.Now(),
		wake:         make(chan struct{}, 1),
		env: map[string]string{
			"CTXLOOM_SESSION_HARP": entry.HarpName,
			agentbus.SocketEnv:     sock,
			agentDepthEnv:          strconv.Itoa(childDepth),
		},
	}
	// Ambient project identity, inherited from this server's own env (the
	// parent run exported it): a containerized child's taskloom must key the
	// SAME shared host log — the isolation task-store mount resolves from this
	// var, and without it the child's task writes are skipped rather than
	// minted under a fresh id.
	if pid := os.Getenv("CTXLOOM_PROJECT_ID"); pid != "" {
		c.env["CTXLOOM_PROJECT_ID"] = pid
	}
	// Enqueue past the cap, never error (D5 queue semantics): claim a free
	// slot now when one exists so `queued` is truthful at return. Claimed
	// BEFORE the record is published — after publication slotHeld belongs to
	// the mutex (the park hooks may touch it).
	c.slotHeld = o.slots.tryAcquire()
	o.broker.AddChild(c.harp, o.parentHarp)
	o.mu.Lock()
	o.children[c.harp] = c
	o.mu.Unlock()
	go o.runChild(c, prompt)

	runtime := rs.Runtime
	if runtime == "" {
		runtime = string(isolation.RuntimeHost)
	}
	return &agentRunOutcome{
		Harp:     c.harp,
		Engine:   rs.Label,
		Profiles: rs.Profiles,
		Runtime:  runtime,
		Queued:   !c.slotHeld,
		Degraded: degraded,
	}, nil
}

// headlessSafePermission enforces D3: children never prompt, so the agent
// must DECLARE a headless-safe permission enum — an absent field is refused
// exactly like a non-headless-safe one, loudly. Under degraded mode the
// refusal becomes a warning and the child launches at the MOST RESTRICTIVE
// headless-safe posture (plan): degraded never widens a child's permissions,
// it narrows them.
func headlessSafePermission(name, declared string) (agent.PermissionMode, []string) {
	if declared != "" {
		if mode, ok := agent.ParsePermissionMode(declared); ok && mode.SafeHeadless() {
			return mode, nil
		}
	}
	reason := "declares no permissions"
	if declared != "" {
		reason = fmt.Sprintf("declares permissions %q, which is not headless-safe", declared)
	}
	strictness.Fail(strictness.ClassConfig,
		fmt.Sprintf("set permissions: plan|bypass on agent %q (agents: in .ctxloom/config.yaml)", name),
		"agent_run: agent %q %s: a delegated child has no channel to surface a permission prompt (D3)", name, reason)
	if strictness.Degraded() {
		return agent.PermissionPlan, []string{fmt.Sprintf(
			"agent %q %s; degraded mode launches it at %q — the most restrictive headless-safe posture", name, reason, agent.PermissionPlan)}
	}
	return agent.PermissionPlan, nil // unreachable in strict mode: findingsError refuses
}

// ensureSocket lazily binds the broker's Unix socket (the executor
// reach-back transport) under the serving session's directory, falling back
// to a temp dir when there is no session dir or the path would overflow the
// Unix socket limit. Listener lifetime is the process's — the broker dies
// with the orchestration by construction.
func (o *agentOrchestrator) ensureSocket() (string, error) {
	o.sockOnce.Do(func() {
		path, err := o.socketPath()
		if err != nil {
			o.sockErr = err
			return
		}
		srv, err := agentbus.Listen(path, o.broker, o.hub, o.rosterSnapshot, o.inject)
		if err != nil {
			o.sockErr = err
			return
		}
		o.busSrv = srv
		o.sockPath = path
	})
	return o.sockPath, o.sockErr
}

// sunPathHeadroom keeps socket paths under the portable sun_path limit.
const sunPathHeadroom = 100

func (o *agentOrchestrator) socketPath() (string, error) {
	if o.parentHarp != "" {
		if dir, err := paths.HarpDir(o.parentHarp); err == nil {
			path := filepath.Join(dir, "agent-bus.sock")
			if len(path) <= sunPathHeadroom {
				if merr := os.MkdirAll(dir, 0o755); merr == nil {
					return path, nil
				}
			}
		}
	}
	dir, err := os.MkdirTemp("", "ctxloom-bus-")
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "agent-bus.sock"), nil
}

// runChild is a spawned child's driver goroutine: wait for an execution slot
// (D4), launch the engine with the agent's intents honored, deliver the
// briefing as the first turn, then drive turns from bus deliveries.
func (o *agentOrchestrator) runChild(c *childAgent, prompt string) {
	o.mu.Lock()
	held := c.slotHeld
	o.mu.Unlock()
	if !held {
		if err := o.slots.acquire(o.baseCtx); err != nil {
			o.failChild(c, err)
			return
		}
		o.mu.Lock()
		c.slotHeld = true
		o.mu.Unlock()
	}
	o.setState(c, childExecuting)

	launch, err := o.launchChild(c, c.resolved.Context)
	if err != nil {
		o.failChild(c, err)
		return
	}
	o.attachLaunch(c, launch)
	o.sendTurn(c, prompt)
	o.driveChild(c, launch)
}

// launchChild prepares (isolation window, serialized — see agentRunGateMu)
// and starts (outside any lock) one child engine.
func (o *agentOrchestrator) launchChild(c *childAgent, contextText string) (*operations.AgentChatLaunch, error) {
	mcpServers := o.childMCPServers(c)
	agentRunGateMu.Lock()
	prep, err := operations.PrepareAgentChat(o.baseCtx, o.cfg, operations.AgentChatRequest{
		Resolved:    c.resolved,
		Context:     contextText,
		WorkDir:     o.projectDir,
		Env:         c.env,
		Permissions: c.perm,
		MCPServers:  mcpServers,
		Gate:        o.gate.Gate(),
		Factory:     o.factory,
	})
	agentRunGateMu.Unlock()
	if err != nil {
		return nil, err
	}
	return prep.Start(o.baseCtx)
}

// childMCPServers composes the child session's managed MCP set — the same
// sources Setup's settings write reconciles, scoped to the child's profile
// set (the chat substrate never runs Setup) — and stamps the bus env onto the
// child's own ctxloom entry: the harp is the child's ambient identity and the
// socket its reach-back to this broker, even if the engine scrubs its
// process env.
func (o *agentOrchestrator) childMCPServers(c *childAgent) []agent.ChatMCPServer {
	servers := agent.ComposeChatMCPServers(c.resolved.Backend,
		backends.AssembleManagedMCP(o.cfg, c.resolved.Profiles),
		o.cfg.ResolveBundleMCPServers(c.resolved.Profiles), nil)
	o.gate.WarnWithheld()
	for i := range servers {
		if servers[i].Name != agent.MCPServerName {
			continue
		}
		env := make(map[string]string, len(servers[i].Env)+len(c.env))
		for k, v := range servers[i].Env {
			env[k] = v
		}
		for k, v := range c.env {
			env[k] = v
		}
		servers[i].Env = env
	}
	return servers
}

// driveChild consumes the child's event stream, handling turn boundaries and
// idle wakes, until the stream closes (the child ended). The stream is teed
// through the observation hub so observers can subscribe by harp; the tee
// never adds backpressure here (agentbus.TapHub's invariant), so this loop's
// pace is exactly what it was consuming launch.Events directly.
func (o *agentOrchestrator) driveChild(c *childAgent, launch *operations.AgentChatLaunch) {
	events := o.hub.Tee(c.harp, launch.Events)
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				o.endChild(c, launch)
				return
			}
			o.touch(c)
			o.handleChildEvent(c, ev)
		case <-c.wake:
			o.wakeChild(c)
		}
	}
}

// touch stamps the child's last activity (roster freshness).
func (o *agentOrchestrator) touch(c *childAgent) {
	o.mu.Lock()
	c.lastActivity = time.Now()
	o.mu.Unlock()
}

// rosterSnapshot lists this orchestrator's children for the bus roster verb —
// the observation viewer's roster source (a future agent_list MCP tool shares
// this same snapshot). Sorted by harp for stable output.
func (o *agentOrchestrator) rosterSnapshot() []agentbus.RosterEntry {
	o.mu.Lock()
	out := make([]agentbus.RosterEntry, 0, len(o.children))
	for _, c := range o.children {
		out = append(out, agentbus.RosterEntry{
			Harp:             c.harp,
			Agent:            c.agentName,
			State:            c.state.label(),
			Parent:           o.parentHarp,
			LastActivityUnix: c.lastActivity.Unix(),
		})
	}
	o.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Harp < out[j].Harp })
	return out
}

func (o *agentOrchestrator) handleChildEvent(c *childAgent, ev agent.ChatEvent) {
	switch {
	case ev.Entry != nil:
		// Chat children report over the bus themselves; their entries are the
		// engine transcript's job (§6b), not the orchestrator's. Oneshot
		// children have no bus reach-back, so their output is bridged to the
		// parent at the boundary.
		if c.oneshot && ev.Entry.Content != "" {
			c.turnOutput = append(c.turnOutput, ev.Entry.Content)
		}
	case ev.Complete != nil:
		o.onTurnBoundary(c)
	}
}

// onTurnBoundary applies the §6a delivery rule "queued mid-turn → deliver at
// the next boundary": pending mail starts the next turn (the slot is kept);
// an empty mailbox parks the child idle and yields the slot.
func (o *agentOrchestrator) onTurnBoundary(c *childAgent) {
	if c.oneshot && len(c.turnOutput) > 0 {
		o.broker.Send(agentbus.Message{
			From: c.harp, To: o.parentHarp, Kind: "result",
			Body: strings.Join(c.turnOutput, "\n"),
		})
		c.turnOutput = nil
	}
	if msg, ok := o.broker.TakeNext(c.harp); ok {
		o.sendTurn(c, msg.Body)
		return
	}
	o.setState(c, childIdle)
	o.releaseSlot(c)
}

// wakeChild starts a new turn on an idle child after a bus delivery (§6a
// "idle at a turn boundary: start a new turn, message as the prompt").
func (o *agentOrchestrator) wakeChild(c *childAgent) {
	o.mu.Lock()
	idle := c.state == childIdle
	o.mu.Unlock()
	if !idle {
		return
	}
	msg, ok := o.broker.TakeNext(c.harp)
	if !ok {
		return // spurious wake (a recv or boundary drain consumed it)
	}
	if err := o.slots.acquire(o.baseCtx); err != nil {
		return
	}
	o.mu.Lock()
	c.slotHeld = true
	c.state = childExecuting
	c.lastActivity = time.Now()
	o.mu.Unlock()
	o.sendTurn(c, msg.Body)
}

// sendTurn writes one turn to the child's input channel. The driver goroutine
// is the channel's only writer, so this never races the close in endChild.
func (o *agentOrchestrator) sendTurn(c *childAgent, text string) {
	select {
	case c.in <- agent.ChatMessage{Text: text}:
	case <-o.baseCtx.Done():
	}
}

// endChild finalizes a child whose event stream closed: surface the terminal
// stream error, mark the session ended (host-side accounting), release the
// slot, and tear the launch down. The record stays — a later agent_send
// resumes the session (§6a).
func (o *agentOrchestrator) endChild(c *childAgent, launch *operations.AgentChatLaunch) {
	for err := range launch.Errs {
		if err != nil {
			clidiag.Warn("ctxloom", "agent %s (%s): chat stream ended: %v", c.harp, c.agentName, err)
		}
	}
	o.mu.Lock()
	c.state = childEnded
	c.lastActivity = time.Now()
	in := c.in
	c.in = nil
	o.mu.Unlock()
	if in != nil {
		close(in)
	}
	launch.Close()
	o.releaseSlot(c)
	if merr := operations.MarkSessionEnded(c.harp, time.Now()); merr != nil {
		clidiag.Warn("ctxloom", "agent %s: mark session ended: %v", c.harp, merr)
	}
	// A message that raced the death (queued after the last boundary drain)
	// must not strand: the ended-child delivery rule is resume (§6a).
	if o.broker.Pending(c.harp) > 0 {
		o.mu.Lock()
		resume := c.state == childEnded
		if resume {
			c.state = childQueued
			c.lastActivity = time.Now()
		}
		o.mu.Unlock()
		if resume {
			go o.resumeChild(c)
		}
	}
}

// failChild reports a launch failure to the parent's inbox — the spawn verb
// already returned (async), so the bus is where the coordinator learns of it.
func (o *agentOrchestrator) failChild(c *childAgent, err error) {
	clidiag.Warn("ctxloom", "agent_run: child %s (%s) failed to launch: %v", c.harp, c.agentName, err)
	o.mu.Lock()
	c.state = childEnded
	c.lastActivity = time.Now()
	o.mu.Unlock()
	o.releaseSlot(c)
	o.broker.Send(agentbus.Message{
		From: c.harp, To: o.parentHarp, Kind: "error",
		Body: fmt.Sprintf("agent %q (session %s) failed to launch: %v", c.agentName, c.harp, err),
	})
	if merr := operations.MarkSessionEnded(c.harp, time.Now()); merr != nil {
		clidiag.Warn("ctxloom", "agent %s: mark session ended: %v", c.harp, merr)
	}
}

// send is the coordinator-side agent_send: deliver to a child by state per
// §6a — complete a parked recv, wake an idle child into a new turn, queue
// mid-turn for the boundary, or resume an ended session.
func (o *agentOrchestrator) send(to, kind, body string) (string, error) {
	if to == agentbus.ParentAddress {
		return "", errors.New("agent_send: this session is the coordinator — it has no parent; address a child by its harp")
	}
	o.mu.Lock()
	c := o.children[to]
	o.mu.Unlock()
	if c == nil {
		return "", fmt.Errorf("agent_send: unknown recipient %q: not a child of this session (spawn it with agent_run first)", to)
	}

	if o.broker.Send(agentbus.Message{From: o.parentHarp, To: to, Kind: kind, Body: body}) {
		return "completed the child's waiting agent_recv", nil
	}
	switch o.driveQueued(c) {
	case childEnded:
		return "child session had ended — resuming it with the message as its next turn", nil
	case childIdle:
		return "delivering as a new turn", nil
	case childQueued:
		return "queued: the child has not started yet; it will drain its mailbox after its first turn", nil
	default: // executing / parked race
		return "queued mid-turn: delivered at the child's next turn boundary", nil
	}
}

// driveQueued drives the recipient of an already-enqueued message per its
// state (§6a): resume an ended child or wake an idle one into a new turn;
// executing/parked/queued children reach the message at their own boundary.
// Returns the state the delivery observed — callers render it for their
// surface (agent_send prose, the inject verb's Delivery* enum).
func (o *agentOrchestrator) driveQueued(c *childAgent) childState {
	o.mu.Lock()
	state := c.state
	if state == childEnded {
		c.state = childQueued // claim the resume so concurrent sends don't double-launch
		c.lastActivity = time.Now()
	}
	o.mu.Unlock()
	switch state {
	case childEnded:
		go o.resumeChild(c)
	case childIdle:
		select {
		case c.wake <- struct{}{}:
		default:
		}
	}
	return state
}

// inject is the bus socket's inject verb (plan §5, slice S2): deliver
// user-typed text into a child by the same §6a delivery-by-state rules as a
// parent agent_send, but with sender identity agentbus.UserSender — a child
// whose parked agent_recv completes sees the message came from the user, not
// its parent. The broker mirrors the O3 user_injected notice to this serving
// session's mailbox unconditionally. Only children this orchestrator holds
// (live, or ended-but-resumable) are injectable; anything else — a foreign
// process's session, an unknown harp, this session itself — is the typed
// ErrNotInjectable. A refused injection touches no delivery state.
func (o *agentOrchestrator) inject(harp, text string) (string, error) {
	o.mu.Lock()
	c := o.children[harp]
	o.mu.Unlock()
	if c == nil {
		return "", fmt.Errorf("inject: %q is not a child of this orchestrator: %w", harp, agentbus.ErrNotInjectable)
	}
	completed, err := o.broker.InjectFromUser(harp, text)
	if err != nil {
		return "", err
	}
	if completed {
		return agentbus.DeliveryCompletedRecv, nil
	}
	switch o.driveQueued(c) {
	case childEnded:
		return agentbus.DeliveryResumed, nil
	case childIdle:
		return agentbus.DeliveryNewTurn, nil
	default:
		return agentbus.DeliveryQueued, nil
	}
}

// resumeChild relaunches an ended child so a bus message can be delivered as
// its next turn — the session-load machinery primes the fresh engine with the
// recorded history when the transcript is bound; an unbound transcript (no
// SessionStart bind — the known L2 gap) degrades to the agent context alone.
func (o *agentOrchestrator) resumeChild(c *childAgent) {
	if err := o.slots.acquire(o.baseCtx); err != nil {
		return
	}
	o.mu.Lock()
	c.slotHeld = true
	c.state = childExecuting
	c.lastActivity = time.Now()
	o.mu.Unlock()

	contextText := c.resolved.Context
	if entries, err := recordedSessionEntries(o.baseCtx, c.harp); err == nil {
		contextText = joinLeadBlocks(contextText, renderResumedTranscript(c.harp, entries))
	} else {
		clidiag.Warn("ctxloom", "agent_send: resume %s: no recorded history to prime (%v); resuming with the agent context only", c.harp, err)
	}

	launch, err := o.launchChild(c, contextText)
	if err != nil {
		o.failChild(c, err)
		return
	}
	o.attachLaunch(c, launch)
	if msg, ok := o.broker.TakeNext(c.harp); ok {
		o.sendTurn(c, msg.Body)
	}
	o.driveChild(c, launch)
}

// recv is the coordinator-side agent_recv: drain this session's own inbox.
func (o *agentOrchestrator) recv(ctx context.Context, wait time.Duration) ([]agentbus.Message, error) {
	return o.broker.Recv(ctx, o.parentHarp, wait)
}

// onChildPark implements the §6a slot yield: a child parked in agent_recv
// consumes no compute, so its execution slot is released while it waits.
func (o *agentOrchestrator) onChildPark(harp string) {
	o.mu.Lock()
	c := o.children[harp]
	release := c != nil && c.slotHeld
	if release {
		c.slotHeld = false
		c.state = childParked
		c.lastActivity = time.Now()
	}
	o.mu.Unlock()
	if release {
		o.slots.release()
	}
}

// onChildUnpark re-acquires the slot before a parked recv completes (message,
// timeout, or cancel) — the child resumes an EXECUTING turn, and the cap
// counts executing turns.
func (o *agentOrchestrator) onChildUnpark(harp string) {
	o.mu.Lock()
	c := o.children[harp]
	parked := c != nil && c.state == childParked
	o.mu.Unlock()
	if !parked {
		return
	}
	if err := o.slots.acquire(o.baseCtx); err != nil {
		return
	}
	o.mu.Lock()
	c.slotHeld = true
	c.state = childExecuting
	c.lastActivity = time.Now()
	o.mu.Unlock()
}

func (o *agentOrchestrator) attachLaunch(c *childAgent, launch *operations.AgentChatLaunch) {
	o.mu.Lock()
	c.in = launch.In
	c.close = launch.Close
	c.oneshot = launch.Oneshot
	o.mu.Unlock()
}

func (o *agentOrchestrator) setState(c *childAgent, s childState) {
	o.mu.Lock()
	c.state = s
	c.lastActivity = time.Now()
	o.mu.Unlock()
}

func (o *agentOrchestrator) releaseSlot(c *childAgent) {
	o.mu.Lock()
	held := c.slotHeld
	c.slotHeld = false
	o.mu.Unlock()
	if held {
		o.slots.release()
	}
}

// turnSlots is the D4 execution-slot queue: a fixed cap with FIFO waiters, so
// enqueued children start in spawn order.
type turnSlots struct {
	mu      sync.Mutex
	free    int
	waiters []chan struct{}
}

func newTurnSlots(n int) *turnSlots { return &turnSlots{free: n} }

func (s *turnSlots) tryAcquire() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.free > 0 && len(s.waiters) == 0 {
		s.free--
		return true
	}
	return false
}

func (s *turnSlots) acquire(ctx context.Context) error {
	s.mu.Lock()
	if s.free > 0 && len(s.waiters) == 0 {
		s.free--
		s.mu.Unlock()
		return nil
	}
	ch := make(chan struct{})
	s.waiters = append(s.waiters, ch)
	s.mu.Unlock()
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		s.mu.Lock()
		for i, w := range s.waiters {
			if w == ch {
				s.waiters = append(s.waiters[:i], s.waiters[i+1:]...)
				s.mu.Unlock()
				return ctx.Err()
			}
		}
		s.mu.Unlock()
		// The grant raced the cancel: the slot is ours, give it back.
		s.release()
		return ctx.Err()
	}
}

func (s *turnSlots) release() {
	s.mu.Lock()
	if len(s.waiters) > 0 {
		ch := s.waiters[0]
		s.waiters = s.waiters[1:]
		s.mu.Unlock()
		close(ch)
		return
	}
	s.free++
	s.mu.Unlock()
}
