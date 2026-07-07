package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ctxloom/ctxloom/internal/agentbus"
	"github.com/ctxloom/ctxloom/internal/config"
)

// Agent-delegation tools (agent_run / agent_send / agent_recv). One process
// plays one of two roles, fixed by environment at startup:
//
//   - ORCHESTRATOR (no CTXLOOM_BUS_SOCKET): this server coordinates — its
//     agent_run spawns children whose ctxloom agent definitions are fully
//     honored, the in-memory bus broker lives here, and agent_send/agent_recv
//     address the children it holds.
//   - EXECUTOR (CTXLOOM_BUS_SOCKET set): this server belongs to a spawned
//     child's engine; agent_send/agent_recv FORWARD to the parent
//     orchestrator's broker over the Unix socket, under the child's ambient
//     identity (CTXLOOM_SESSION_HARP). agent_run still orchestrates locally
//     (a sub-coordinator), bounded by the depth guard.

const (
	defaultRecvWait = 60 * time.Second
	maxRecvWait     = 10 * time.Minute
)

// agentDelegation is the per-process dispatch state for the agent tools.
type agentDelegation struct {
	forwardSock string // executor mode: the parent orchestrator's bus socket
	selfHarp    string // this process's ambient session identity
	orch        *agentOrchestrator
}

func newAgentDelegation(cfg *config.Config) *agentDelegation {
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}
	harp := os.Getenv("CTXLOOM_SESSION_HARP")
	return &agentDelegation{
		forwardSock: os.Getenv(agentbus.SocketEnv),
		selfHarp:    harp,
		orch:        newAgentOrchestrator(cfg, harp, envAgentDepth(), cwd),
	}
}

// envAgentDepth reads this process's delegation depth (0 = root coordinator;
// children see parent+1 via the env agent_run sets).
func envAgentDepth() int {
	n, err := strconv.Atoi(os.Getenv(agentDepthEnv))
	if err != nil || n < 0 {
		return 0
	}
	return n
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
	To   string `json:"to" jsonschema:"Recipient: a child session harp, or \"parent\" (executors may ONLY address their parent)"`
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

func (s *ctxServer) registerAgentTools(server *mcp.Server) {
	// The docgen server registers with a nil cfg (handlers never invoked);
	// live servers get their delegation state here, pre-serve and
	// single-threaded (newAgentOrchestrator installs the executable trust
	// gate on the shared cfg).
	if s.cfg != nil && s.agents == nil {
		s.agents = newAgentDelegation(s.cfg)
	}

	mcp.AddTool(server,
		&mcp.Tool{
			Name: "agent_run",
			Description: "Launch a configured ctxloom agent as a delegated child session. Async spawn: returns at enqueue with the child's harp (its address and continuation token); results, questions, and reports come back as bus messages (agent_recv). Follow-ups go down with agent_send(to: harp). Children execute serially (a spawn past the cap queues) and never prompt: the agent must declare a headless-safe permission enum.",
		},
		s.handleAgentRun)

	mcp.AddTool(server,
		&mcp.Tool{
			Name: "agent_send",
			Description: "Send a bus message to another agent session. Coordinators address their children by harp — delivery completes a waiting agent_recv, starts a new turn on an idle child, queues mid-turn for the next boundary, or resumes an ended session. Executors may only address \"parent\"; peer messaging routes via the coordinator. In-memory, at-most-once: durable results belong in reports/tasks, not the bus.",
		},
		s.handleAgentSend)

	mcp.AddTool(server,
		&mcp.Tool{
			Name: "agent_recv",
			Description: "Receive pending bus messages for this session, waiting (parked server-side) up to the bounded timeout when none are pending. A child parked here yields its execution slot. On timeout the call fails and you are expected to drop the coordination: write your report/deferral state and finish — the coordinator learns from the session record, not from silence.",
		},
		s.handleAgentRecv)
}

func (s *ctxServer) delegation() (*agentDelegation, error) {
	if s.agents == nil {
		return nil, errors.New("agent delegation unavailable: server started without a loaded config")
	}
	return s.agents, nil
}

func (s *ctxServer) handleAgentRun(ctx context.Context, _ *mcp.CallToolRequest, in agentRunInput) (*mcp.CallToolResult, *agentRunResult, error) {
	d, err := s.delegation()
	if err != nil {
		return nil, nil, err
	}
	out, err := d.orch.run(ctx, in.Agent, in.Prompt)
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
		return nil, nil, errors.New(`agent_send: to is required (a child harp, or "parent" from an executor)`)
	}
	if in.Body == "" {
		return nil, nil, errors.New("agent_send: body is required")
	}
	if d.forwardSock != "" {
		if err := agentbus.ForwardSend(d.forwardSock, d.selfHarp, in.To, in.Kind, in.Body); err != nil {
			return nil, nil, err
		}
		return nil, &agentSendResult{To: in.To, Disposition: "sent to the coordinator"}, nil
	}
	disposition, err := d.orch.send(in.To, in.Kind, in.Body)
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
	var msgs []agentbus.Message
	if d.forwardSock != "" {
		msgs, err = agentbus.ForwardRecv(d.forwardSock, d.selfHarp, wait)
	} else {
		msgs, err = d.orch.recv(ctx, wait)
	}
	if err != nil {
		if errors.Is(err, agentbus.ErrRecvTimeout) {
			return nil, nil, fmt.Errorf("%w (waited %s)", agentbus.ErrRecvTimeout, wait)
		}
		return nil, nil, err
	}
	out := &agentRecvResult{Messages: make([]agentBusMessage, 0, len(msgs))}
	for _, m := range msgs {
		out.Messages = append(out.Messages, agentBusMessage{From: m.From, Kind: m.Kind, Body: m.Body})
	}
	return nil, out, nil
}
