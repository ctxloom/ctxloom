package operations

import (
	"context"
	"maps"
	"sync"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/lm/isolation"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// AgentChatRequest launches a resolved agent as a delegated CHILD driven over
// the structured-chat substrate (agent_run): the same launch tail the fan
// uses, but multi-turn — the orchestrator delivers bus messages as turns.
type AgentChatRequest struct {
	Resolved *ResolvedAgent
	// Context overrides Resolved.Context for this launch (a resume primes it
	// with rendered history); empty uses Resolved.Context. It rides the first
	// turn as a lead block — the chat substrate never runs Setup.
	Context string
	WorkDir string
	// Env is the child engine's extra environment: the child's session harp,
	// the bus socket, and the delegation depth (ambient identity — never
	// client-claimed).
	Env map[string]string
	// Permissions is the already-gated headless-safe posture (D3: children
	// never prompt; the caller refused or downgraded anything else).
	Permissions agent.PermissionMode
	// MCPServers is the composed managed set for the child session (the
	// chat paths never run Setup, so the servers ride the session).
	MCPServers []agent.ChatMCPServer
	// Gate is the shared executable trust gate for per-turn managed assembly
	// on the oneshot fallback path.
	Gate      bundles.ContentGate
	Verbosity int
	// Factory overrides plugin construction (test seam). Exactly like the
	// fan: a non-nil Factory skips isolation entirely.
	Factory pb.ClientFactory
}

// AgentChatLaunch is a launched child: turns go in on In (plain text — the
// launch prepends the lead context to the first turn itself), normalized
// events come out on Events (a Complete marks each turn boundary), and Errs
// carries the stream's terminal error. Close kills the engine and tears down
// the workspace; the orchestrator closes In (its exclusive writer) first.
type AgentChatLaunch struct {
	In     chan<- agent.ChatMessage
	Events <-chan agent.ChatEvent
	Errs   <-chan error
	// Oneshot marks the no-structured-chat fallback: each turn ran as an
	// independent oneshot and the child engine has no bus reach-back, so the
	// orchestrator bridges each turn's output to the parent's mailbox.
	Oneshot bool
	Close   func()
}

// PreparedAgentChat is the isolation-resolved half of a child launch. The
// split exists for strictness-window hygiene: Prepare runs the
// checkpoint→isolation.Prepare→gate window (so the caller can serialize it
// against its own windows), Start spawns the engine outside any lock.
type PreparedAgentChat struct {
	cfg         *config.Config
	req         AgentChatRequest
	contextText string
	axes        isolation.Axes
	oneshot     bool

	// chat-path only (nil/empty on the oneshot fallback, which prepares its
	// isolation per turn inside runResolvedAgent):
	factory      pb.ClientFactory
	workDir      string
	workspaceEnv map[string]string
	cleanup      func()
}

// PrepareAgentChat resolves how the child will run: the structured-chat path
// for backends with the capability (isolation prepared once, engine kept
// alive across turns), or the oneshot fallback for backends without it. On
// the chat path this runs the same fail-loudly member gate as the fan —
// checkpoint before isolation.Prepare, refuse when an explicitly-requested
// container degraded (ClassIsolation finding) unless degraded mode — under
// the same isolationGateMu window serialization.
func PrepareAgentChat(ctx context.Context, cfg *config.Config, req AgentChatRequest) (*PreparedAgentChat, error) {
	rs := req.Resolved
	p := &PreparedAgentChat{
		cfg:         cfg,
		req:         req,
		contextText: req.Context,
		axes: isolation.Axes{
			Workspace: isolation.WorkspaceAxis(cfg.Workspace),
			Runtime:   isolation.RuntimeAxis(rs.Runtime),
		},
	}
	if p.contextText == "" {
		p.contextText = rs.Context
	}

	if _, ok := backends.Get(rs.Backend).(agent.StructuredChat); !ok {
		p.oneshot = true
		return p, nil
	}

	p.factory = req.Factory
	p.workDir = req.WorkDir
	if p.factory == nil {
		isolationGateMu.Lock()
		mark := strictness.Checkpoint()
		policy, ws := prepareIsolation(ctx, p.axes, rs.Backend, IsolationImageConfig(cfg, rs.Backend), req.WorkDir, rs.Name, isolation.SessionStateFromEnv(req.Env))
		found := strictness.Since(mark)
		isolationGateMu.Unlock()
		p.workDir = ws.Dir()
		p.workspaceEnv = isolation.WorkspaceEnv(ws)
		p.cleanup = func() { _ = ws.Cleanup() }
		if gerr := isolationGateErr(found); gerr != nil {
			p.Abort()
			return nil, gerr
		}
		p.factory = isolation.FactoryForWorkspace(policy, ws)
	}
	return p, nil
}

// Abort tears down a prepared-but-never-started launch's workspace.
func (p *PreparedAgentChat) Abort() {
	if p.cleanup != nil {
		p.cleanup()
		p.cleanup = nil
	}
}

// Start spawns the child and opens its turn stream. ctx bounds the child's
// whole lifetime (the orchestrator's, not one tool call's).
func (p *PreparedAgentChat) Start(ctx context.Context) (*AgentChatLaunch, error) {
	if p.oneshot {
		return p.startOneshot(ctx), nil
	}

	rs := p.req.Resolved
	client, err := p.factory(rs.Backend, rs.Label, p.req.Verbosity)
	if err != nil {
		p.Abort()
		return nil, err
	}

	env := p.workspaceEnv
	if len(p.req.Env) > 0 {
		merged := make(map[string]string, len(env)+len(p.req.Env))
		maps.Copy(merged, env)
		maps.Copy(merged, p.req.Env)
		env = merged
	}
	in, events, errs, err := client.Chat(ctx, agent.ChatRequest{
		WorkDir:     p.workDir,
		Model:       rs.Model,
		Env:         env,
		Permissions: p.req.Permissions,
		MCPServers:  p.req.MCPServers,
	})
	if err != nil {
		client.Kill()
		p.Abort()
		return nil, err
	}

	done := make(chan struct{})
	var closeOnce sync.Once
	closeFn := func() {
		closeOnce.Do(func() {
			close(done)
			client.Kill()
			p.Abort()
		})
	}
	return &AgentChatLaunch{
		In:     leadContextIn(in, p.contextText, done),
		Events: events,
		Errs:   errs,
		Close:  closeFn,
	}, nil
}

// leadContextIn wraps a chat input channel so the composed agent context
// rides the FIRST turn as a lead block (the chat substrate never runs Setup,
// so this is the context delivery — same contract as the ACP first-turn lead
// block). done unblocks a pending forward when the launch is torn down.
func leadContextIn(in chan<- agent.ChatMessage, lead string, done <-chan struct{}) chan<- agent.ChatMessage {
	wrapped := make(chan agent.ChatMessage)
	go func() {
		defer close(in)
		first := true
		for {
			var msg agent.ChatMessage
			var ok bool
			select {
			case msg, ok = <-wrapped:
				if !ok {
					return
				}
			case <-done:
				return
			}
			if first && msg.Text != "" {
				if lead != "" {
					msg.Text = lead + "\n\n" + msg.Text
				}
				first = false
			}
			select {
			case in <- msg:
			case <-done:
				return
			}
		}
	}()
	return wrapped
}

// startOneshot adapts backends WITHOUT structured chat to the launch shape:
// each inbound turn runs as an independent oneshot through the fan's launch
// tail (runResolvedAgent — per-turn isolation window, headless permission
// floor), its captured stdout emitted as an assistant entry + turn Complete.
// There is no session continuity between turns beyond the composed context.
func (p *PreparedAgentChat) startOneshot(ctx context.Context) *AgentChatLaunch {
	rs := p.req.Resolved
	in := make(chan agent.ChatMessage)
	events := make(chan agent.ChatEvent)
	errs := make(chan error, 1)
	turnCtx, cancel := context.WithCancel(ctx)
	go func() {
		defer close(errs)
		defer close(events)
		for msg := range in {
			if msg.Text == "" {
				continue
			}
			res, err := runResolvedAgent(turnCtx, resolvedRunRequest{
				Context:        p.contextText,
				Task:           msg.Text,
				WorkDir:        p.req.WorkDir,
				Verbosity:      p.req.Verbosity,
				Label:          rs.Label,
				Backend:        rs.Backend,
				Model:          rs.Model,
				Permissions:    p.req.Permissions.String(),
				Axes:           p.axes,
				IsolationImage: IsolationImageConfig(p.cfg, rs.Backend),
				AgentID:        rs.Name,
				Profiles:       rs.Profiles,
				Gate:           p.req.Gate,
				Factory:        p.req.Factory,
				ExtraEnv:       p.req.Env,
			})
			if err != nil {
				events <- agent.ChatEvent{Entry: &agent.SessionEntry{
					Type: agent.EntryTypeSystem, Content: "delegated turn failed: " + err.Error(), IsError: true,
				}}
				events <- agent.ChatEvent{Complete: &agent.TurnMeta{StopReason: "error"}}
				continue
			}
			events <- agent.ChatEvent{Entry: &agent.SessionEntry{
				Type: agent.EntryTypeAssistant, Content: res.Output,
			}}
			events <- agent.ChatEvent{Complete: &agent.TurnMeta{StopReason: "end_turn"}}
		}
	}()
	return &AgentChatLaunch{In: in, Events: events, Errs: errs, Oneshot: true, Close: cancel}
}
