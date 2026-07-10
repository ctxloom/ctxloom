package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ctxloom/ctxloom/internal/agentcoord/coord"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// Agent-delegation tools (agent_run / agent_send / agent_recv / agent_stop),
// backed by the runtime coordinator (internal/agentcoord/coord). One process
// plays one of two roles, fixed by environment at startup:
//
//   - COORDINATOR HOST (no CTXLOOM_COORD_URL): this server owns delegation.
//     `ctxloom run` and `ctxloom acp` stand the coordinator up eagerly and
//     hand it to their engines via the env trio; a bare `ctxloom mcp` (an
//     externally-launched harness — the orphaned-orchestrator fallback)
//     builds one lazily on first agent-tool use. Either way the durable CQRS
//     stores, credentials, and listeners live in the coordinator library.
//   - FORWARDER (CTXLOOM_COORD_URL set): this server belongs to a spawned
//     child's engine (or the parent harness of a hosting run/acp process);
//     it never registers local tools at all — the WHOLE server is a
//     stdio↔HTTP proxy onto the coordinator's MCP endpoint (mcp_forward.go),
//     and identity derives from the credential per request, never from this
//     process's env.
const (
	defaultRecvWait = 60 * time.Second
	maxRecvWait     = 10 * time.Minute
)

// agentDelegation is the coordinator-host state behind the agent tools.
type agentDelegation struct {
	self coord.Identity
	c    *coord.Coordinator
}

// selfIdentityFromEnv is the stdio server's ambient identity: the serving
// session's harp, always depth 0 — the executor role died with the shim
// (children run in forward mode and never reach this constructor).
func selfIdentityFromEnv(projectDir string) coord.Identity {
	return coord.Identity{
		Harp:    os.Getenv("CTXLOOM_SESSION_HARP"),
		Depth:   0,
		Project: projectDir,
	}
}

// newAgentDelegation stands the coordinator up for a bare `ctxloom mcp`
// serving process: durable stores + listeners + the viewer socket under the
// serving harp's dir. Children spawned from here reach back over the
// coordinator's authenticated MCP endpoint exactly like run/acp-hosted ones.
func newAgentDelegation(cfg *config.Config) (*agentDelegation, error) {
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}
	self := selfIdentityFromEnv(cwd)
	c, err := newHostedCoordinator(cfg, cwd)
	if err != nil {
		return nil, err
	}
	if err := c.BindSessionSocket(self.Harp); err != nil {
		clidiag.Warn("ctxloom", "agent bus (viewer verbs) unavailable: %v", err)
	}
	return &agentDelegation{self: self, c: c}, nil
}

type agentRunInput struct {
	Agent  string `json:"agent" jsonschema:"Configured ctxloom agent name to launch (its composed profiles, engine binding, runtime axis, and permission enum are honored)"`
	Prompt string `json:"prompt" jsonschema:"The child's briefing — delivered as its first turn"`
}

type agentRunResult struct {
	Harp             string   `json:"harp"`
	Engine           string   `json:"engine"`
	Profiles         []string `json:"profiles,omitempty"`
	Runtime          string   `json:"runtime"`
	Queued           bool     `json:"queued,omitempty"`
	DegradedFindings []string `json:"degraded_findings,omitempty"`
}

type agentSendInput struct {
	To   string `json:"to" jsonschema:"Recipient: a child session harp, or \"parent\" (delegated children may ONLY address their parent)"`
	Body string `json:"body" jsonschema:"Message body (compact: findings, questions, verdicts — bulk detail stays in the session transcript)"`
	Kind string `json:"kind,omitempty" jsonschema:"Optional message kind (e.g. result, question, error)"`
}

type agentSendResult struct {
	To          string `json:"to"`
	Disposition string `json:"disposition"`
}

type agentRecvInput struct {
	Wait int `json:"wait,omitempty" jsonschema:"Seconds to wait for a message (default 60, max 600). On timeout the call fails: drop the coordination, write your report/deferral state, and finish"`
}

type agentBusMessage struct {
	From string `json:"from"`
	Kind string `json:"kind,omitempty"`
	Body string `json:"body"`
}

type agentRecvResult struct {
	Messages []agentBusMessage `json:"messages"`
}

type agentStopInput struct {
	Harp string `json:"harp" jsonschema:"The child session harp to stop (its engine is killed and its execution slot freed; a later agent_send resumes it as a fresh run)"`
}

type agentStopResult struct {
	Harp        string `json:"harp"`
	Disposition string `json:"disposition"`
}

func (s *ctxServer) registerAgentTools(server *mcp.Server) {
	mcp.AddTool(server,
		&mcp.Tool{
			Name: "agent_run",
			Description: "Launch a configured ctxloom agent as a delegated child session. Async spawn: returns at enqueue with the child's harp (its address and continuation token); results, questions, and reports come back as mailbox messages (agent_recv). Follow-ups go down with agent_send(to: harp). Children execute serially (a spawn past the cap queues) and never prompt: the agent must declare a headless-safe permission enum.",
		},
		s.handleAgentRun)

	mcp.AddTool(server,
		&mcp.Tool{
			Name: "agent_send",
			Description: "Send a message to another agent session. Coordinators address their children by harp — delivery completes a waiting agent_recv, starts a new turn on an idle child, queues mid-turn for the next boundary, or resumes an ended session. Delegated children may only address \"parent\"; peer messaging routes via the coordinator. Queued delivery is durable (at-least-once): a message to an offline session survives coordinator restarts.",
		},
		s.handleAgentSend)

	mcp.AddTool(server,
		&mcp.Tool{
			Name: "agent_recv",
			Description: "Receive pending mailbox messages for this session, waiting (parked server-side) up to the bounded timeout when none are pending. A child parked here yields its execution slot. Delivery is at-least-once: this call acknowledges the messages the previous call returned, and unacknowledged deliveries are re-delivered. On timeout the call fails and you are expected to drop the coordination: write your report/deferral state and finish.",
		},
		s.handleAgentRecv)

	mcp.AddTool(server,
		&mcp.Tool{
			Name: "agent_stop",
			Description: "Stop a delegated child session: its engine (or container) is killed, its execution slot frees immediately (the spawn queue advances), its credential is revoked, and the stop is journaled. The session stays resumable — a later agent_send relaunches it as a fresh run primed with its recorded history.",
		},
		s.handleAgentStop)
}

// delegation resolves the agent-tool backend, standing the coordinator up
// lazily for a bare `ctxloom mcp` (the run/acp hosts inject theirs via
// newCtxServerForIdentity).
func (s *ctxServer) delegation() (*agentDelegation, error) {
	s.agentsMu.Lock()
	defer s.agentsMu.Unlock()
	if s.agents != nil {
		return s.agents, nil
	}
	if s.cfg == nil {
		return nil, errors.New("agent delegation unavailable: server started without a loaded config")
	}
	d, err := newAgentDelegation(s.cfg)
	if err != nil {
		return nil, fmt.Errorf("agent delegation unavailable: %w", err)
	}
	s.agents = d
	return d, nil
}

func (s *ctxServer) handleAgentRun(ctx context.Context, _ *mcp.CallToolRequest, in agentRunInput) (*mcp.CallToolResult, *agentRunResult, error) {
	d, err := s.delegation()
	if err != nil {
		return nil, nil, err
	}
	out, err := d.c.AgentRun(ctx, d.self, in.Agent, in.Prompt)
	if err != nil {
		return nil, nil, err
	}
	return nil, &agentRunResult{
		Harp:             out.Harp,
		Engine:           out.Engine,
		Profiles:         out.Profiles,
		Runtime:          out.Runtime,
		Queued:           out.Queued,
		DegradedFindings: out.Degraded,
	}, nil
}

func (s *ctxServer) handleAgentSend(_ context.Context, _ *mcp.CallToolRequest, in agentSendInput) (*mcp.CallToolResult, *agentSendResult, error) {
	d, err := s.delegation()
	if err != nil {
		return nil, nil, err
	}
	if in.To == "" {
		return nil, nil, errors.New(`agent_send: to is required (a child harp, or "parent" from a delegated child)`)
	}
	if in.Body == "" {
		return nil, nil, errors.New("agent_send: body is required")
	}
	disposition, err := d.c.AgentSend(d.self, in.To, in.Kind, in.Body)
	if err != nil {
		return nil, nil, err
	}
	return nil, &agentSendResult{To: in.To, Disposition: disposition}, nil
}

func (s *ctxServer) handleAgentRecv(ctx context.Context, _ *mcp.CallToolRequest, in agentRecvInput) (*mcp.CallToolResult, *agentRecvResult, error) {
	d, err := s.delegation()
	if err != nil {
		return nil, nil, err
	}
	wait := time.Duration(in.Wait) * time.Second
	if wait <= 0 {
		wait = defaultRecvWait
	}
	if wait > maxRecvWait {
		wait = maxRecvWait
	}
	msgs, err := d.c.AgentRecv(ctx, d.self, wait)
	if err != nil {
		if errors.Is(err, coord.ErrRecvTimeout) {
			return nil, nil, fmt.Errorf("%w (waited %s)", coord.ErrRecvTimeout, wait)
		}
		return nil, nil, err
	}
	out := &agentRecvResult{Messages: make([]agentBusMessage, 0, len(msgs))}
	for _, m := range msgs {
		out.Messages = append(out.Messages, agentBusMessage{From: m.From, Kind: m.Kind, Body: m.Body})
	}
	return nil, out, nil
}

func (s *ctxServer) handleAgentStop(_ context.Context, _ *mcp.CallToolRequest, in agentStopInput) (*mcp.CallToolResult, *agentStopResult, error) {
	d, err := s.delegation()
	if err != nil {
		return nil, nil, err
	}
	if in.Harp == "" {
		return nil, nil, errors.New("agent_stop: harp is required (a child session harp; see the roster)")
	}
	disposition, err := d.c.AgentStop(d.self, in.Harp)
	if err != nil {
		return nil, nil, err
	}
	return nil, &agentStopResult{Harp: in.Harp, Disposition: disposition}, nil
}
