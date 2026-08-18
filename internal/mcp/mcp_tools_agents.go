package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ctxloom/ctxloom/internal/agentcoord/coord"
	"github.com/ctxloom/ctxloom/internal/agentcoord/mcpschema"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/harp"
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

// agentDelegation is the coordinator-host state behind the agent tools.
type agentDelegation struct {
	self coord.Identity
	c    *coord.Coordinator
}

// selfIdentityFromEnv is the stdio server's ambient identity: the serving
// session's harp, always depth 0 — the executor role died with the shim
// (children run in forward mode and never reach this constructor).
//
// THE HARP IS NOT OPTIONAL, and an absent one used to be accepted silently.
// `ctxloom run` exports CTXLOOM_SESSION_HARP into the engine's env, so an
// engine it launched spawns this server WITH a harp. But that is only one of
// the two shipping ways to reach here: `manage install` registers this server
// in .mcp.json as a bare `ctxloom mcp` with NO env at all
// (agent.WriteMCPConfig's generated entry), so a plain engine session — the
// most common installation — starts a coordinator whose Harp is "".
//
// An empty owner harp does not fail; it silently breaks every child->parent
// delivery, because the harp IS the coordinator's mailbox address:
//
//   - agent_run journals AgentSpawned.ParentHarp = "" (coord/children.go's
//     childRt.parentHarp), so the child's lineage has no parent;
//   - coord's bridgeTurnResult then queues the child's whole turn output to
//     "" and queueMailPayloadID refuses it ("no session can drain role"),
//     leaving the report only in a stderr warning the coordinator — an agent
//     whose sole input is its mailbox — structurally cannot read;
//   - a child that calls agent_send(to:"parent") itself hits childSend's
//     `parent == ""` arm and is told it is "not a child of this coordinator".
//
// Every cheap signal stays green throughout: agent_run returns success with a
// harp, the child really runs, its transcript really appears. Only the
// delivery is gone — this codebase's characteristic exit-0-with-zero-bytes
// shape, on the bus.
//
// So a serving process with no ambient session mints its own harp rather than
// running as an unaddressable one. It is a real, distinct identity for the
// lifetime of this coordinator process, which is exactly the lifetime over
// which its mailbox is meaningful: the same process both spawns the children
// and drains the mailbox via agent_recv, so a per-process identity closes the
// loop. It is deliberately NOT persisted — inventing a durable session
// identity for a session ctxloom did not launch would be a stronger claim
// than the evidence supports.
func selfIdentityFromEnv(projectDir string) coord.Identity {
	sessionHarp := os.Getenv("CTXLOOM_SESSION_HARP")
	if sessionHarp == "" {
		sessionHarp = harp.GenerateName()
	}
	return coord.Identity{
		Harp:    sessionHarp,
		Depth:   0,
		Project: projectDir,
	}
}

// newAgentDelegation stands the coordinator up for a bare `ctxloom mcp`
// serving process: durable stores + listeners (D2: ConsumerService is part
// of that listener set — no separate per-harp viewer bind step exists
// anymore). Children spawned from here reach back over the coordinator's
// authenticated MCP endpoint exactly like run/acp-hosted ones.
func newAgentDelegation(cfg *config.Config) (*agentDelegation, error) {
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}
	self := selfIdentityFromEnv(cwd)
	c, err := NewHostedCoordinator(cfg, cwd)
	if err != nil {
		return nil, err
	}
	return &agentDelegation{self: self, c: c}, nil
}

type agentRunInput struct {
	Agent  string `json:"agent" jsonschema:"Configured ctxloom agent name to launch (its composed profiles, engine binding, runtime axis, and permission enum are honored)"`
	Prompt string `json:"prompt" jsonschema:"The child's briefing — delivered as its first turn"`
	// Workspace is GAP 2's per-call workspace-axis override: "none" runs the
	// child in the parent's live project checkout (it can stomp it
	// mid-session); "worktree" carves the child its own git worktree instead
	// — matching run/acp --workspace's enum. Empty defers to the
	// project's cfg.Workspace when THAT is set explicitly; if neither this
	// nor the project config says anything, a delegated child now DEFAULTS
	// to worktree (own checkout) rather than the shared one — see
	// operations.PrepareAgentChat's workspace-resolution comment. This is a
	// file-level default only: a worktree isolates the child's WORKSPACE,
	// never the engine's own global config/credential/session store, which
	// some engines keep outside any per-agent env override entirely.
	//
	// A worktree spawn (explicit or defaulted) that lands while the parent
	// project tree carries uncommitted changes now has an explicit decision
	// to make — see DirtyTreeHandler below — rather than a bare refusal:
	// `git worktree add` only ever checks out committed state, so those
	// edits would otherwise be silently invisible to the child (this
	// project's signature failure mode, self-inflicted by the very
	// isolation meant to protect the child's blast radius).
	Workspace string `json:"workspace,omitempty" jsonschema:"Session workspace axis for this child: \"none\" (shared project checkout — the child can stomp the parent's live files) or \"worktree\" (its own isolated git worktree, checked out at HEAD — the child will NOT see the parent's uncommitted edits). Empty defers to the project config if it sets one explicitly; otherwise defaults to worktree. Isolates the workspace only, never the engine's own global config/credentials/session store."`
	// DirtyTreeHandler is the caller's per-call override for what a
	// worktree spawn does when the PARENT project tree carries uncommitted
	// changes (a worktree checkout only ever sees committed state). Empty
	// defers to the project's `dirty_tree_handler` config default, then to
	// the built-in default ("commit") — the identical precedence Workspace
	// above uses. See operations.handleDirtyParentTree for what each value
	// does.
	//
	// Deliberately carries NO acknowledgement for the "commit" handler's
	// mutation: committing on the user's behalf requires a per-project,
	// HUMAN-set acknowledgement (.ctxloom/state/dirty_tree_commit_ack.yaml,
	// written by `ctxloom init` or `ctxloom manage commit trust` —
	// never a config.yaml key)
	// that this — or any other — per-call MCP parameter can never set. An
	// agent cannot consent on the user's behalf; only a human editing the
	// project's config can.
	DirtyTreeHandler string `json:"dirty_tree_handler,omitempty" jsonschema:"What this spawn does when the PARENT project tree has uncommitted changes and resolves to worktree isolation (a worktree checkout only ever sees committed state). \"commit\": auto-commit the parent's dirty state first, so the child sees it (requires a human dirty-tree-commit acknowledgement recorded via ctxloom init or ctxloom manage commit trust — never a config key, never this per-call parameter; otherwise the spawn is refused, actionably). \"copy\": carve the worktree at HEAD, then reproduce the uncommitted changes inside it as uncommitted WIP (tracked and untracked both) — nothing is committed to the parent's branch. \"stale\": proceed with the child seeing committed state only, warning what it will miss. \"fail\": refuse the spawn, naming the uncommitted paths and the alternatives. Empty defers to the project's dirty_tree_handler config default, then to the built-in default (\"commit\")."`
}

type agentRunResult struct {
	Harp             string   `json:"harp"`
	LLM              string   `json:"llm"`
	Profiles         []string `json:"profiles,omitempty"`
	Runtime          string   `json:"runtime"`
	Queued           bool     `json:"queued,omitempty"`
	DegradedFindings []string `json:"degraded_findings,omitempty"`
}

type agentSendInput struct {
	To         string         `json:"to" jsonschema:"Recipient: a child session harp, or \"parent\" (delegated children may ONLY address their parent)"`
	Body       string         `json:"body" jsonschema:"Message body (compact: findings, questions, verdicts — bulk detail stays in the session transcript)"`
	Kind       string         `json:"kind,omitempty" jsonschema:"The message's kind, from a CLOSED vocabulary of exactly four values you may send: message (plain prose, claiming no special authority), result (your findings/verdict/deliverable), error (a failure the recipient must act on), question (you expect an answer back). REQUIRED for an ordinary send — an absent or unrecognised value is refused, naming these four — except when in_reply_to correlates to a relayed approval_request or a coordinator-asked question: that reply's kind is not read at all (the decision/answer in structured or body is what resolves it), so it may be left unset. Every other value (approval_request and the rest of the coordinator's own vocabulary) is rejected from a sender, not quietly downgraded."`
	Structured map[string]any `json:"structured,omitempty" jsonschema:"Optional structured companion, carried opaque — it is no longer read for a \"kind\" key; name the kind in the kind field above. ANSWERING A RELAYED approval_request is its main use: set in_reply_to to that message's message_id and this field IS the ApprovalDecision itself: {\"decision\": \"DECISION_ACCEPT\"|\"DECISION_ACCEPT_FOR_SESSION\"|\"DECISION_DECLINE\"|\"DECISION_CANCEL\", \"note\": \"...\"}. Any OTHER key on an approval reply is rejected, and the send fails naming the accepted shape. Answering with anything else — including a bare courtesy ack — is refused without consuming the approval, so the decision can simply be re-sent."`
	InReplyTo  string         `json:"in_reply_to,omitempty" jsonschema:"Correlates this reply to an earlier inbound message's message_id. It is the ONLY correlation key. Set it to a relayed approval_request's message_id to answer that approval, and put the ApprovalDecision in structured."`
}

type agentSendResult struct {
	To          string `json:"to"`
	Disposition string `json:"disposition"`
}

type agentRecvInput struct {
	// A struct tag must be a literal, so it cannot reference
	// mcpschema.RecvWaitDoc the way the generated schema does;
	// TestAgentRecvWait_StdioSchemaDescribesTheSameBounds pins them equal.
	Wait int `json:"wait,omitempty" jsonschema:"Seconds to wait for a message (default 60, max 600). On timeout the call fails: drop the coordination, write your report/deferral state, and finish"`
}

type agentBusMessage struct {
	MessageID  string         `json:"message_id,omitempty"`
	From       string         `json:"from"`
	Kind       string         `json:"kind,omitempty"`
	Body       string         `json:"body"`
	Structured map[string]any `json:"structured,omitempty" jsonschema:"Structured companion the sender attached, when there is one. A message whose kind is approval_request carries the escalation ladder's ApprovalRequest projection and is WAITING on you: answer it with agent_send(in_reply_to: this message_id, structured: {\"decision\": \"DECISION_ACCEPT\"|\"DECISION_ACCEPT_FOR_SESSION\"|\"DECISION_DECLINE\"|\"DECISION_CANCEL\", \"note\": \"...\"}). Until that lands the requesting child is blocked, and it is auto-DECLINED if the rung's timeout elapses first."`
	// StructuredError reports a companion that arrived but could not be
	// decoded. The mailbox commits a batch as delivered BEFORE this projection
	// runs, so such a payload is lost rather than redelivered — the caller has
	// to be able to tell "the sender attached nothing" from "the sender
	// attached something and it did not survive the trip".
	StructuredError string `json:"structured_error,omitempty" jsonschema:"Set when the sender attached a structured companion that could not be decoded; the body was still delivered but the structured field is absent and will NOT be redelivered. A message whose kind needs a structured answer (approval_request) cannot be answered from the body alone — treat this as a delivery fault, not an empty field."`
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
			Name:        "agent_run",
			Description: "Launch a configured ctxloom agent as a delegated child session. Async spawn: returns at enqueue with the child's harp (its address and continuation token); results, questions, and reports come back as mailbox messages (agent_recv). Follow-ups go down with agent_send(to: harp). Children execute serially (a spawn past the cap queues) and never prompt: the agent must declare a headless-safe permission enum.",
		},
		s.handleAgentRun)

	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "agent_send",
			Description: "Send a message to another agent session. Coordinators address their children by harp — delivery completes a waiting agent_recv, starts a new turn on an idle child, queues mid-turn for the next boundary, or resumes an ended session. Delegated children may only address \"parent\"; peer messaging routes via the coordinator. Queued delivery is durable (at-least-once): a message to an offline session survives coordinator restarts.",
		},
		s.handleAgentSend)

	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "agent_recv",
			Description: "Receive pending mailbox messages for this session, waiting (parked server-side) up to the bounded timeout when none are pending. A child parked here yields its execution slot. Delivery is at-least-once: this call acknowledges the messages the previous call returned, and unacknowledged deliveries are re-delivered. On timeout the call fails and you are expected to drop the coordination: write your report/deferral state and finish.",
		},
		s.handleAgentRecv)

	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "agent_stop",
			Description: "Stop a delegated child session: its engine (or container) is killed, its execution slot frees immediately (the spawn queue advances), its credential is revoked, and the stop is journaled. The session stays resumable — a later agent_send relaunches it as a fresh run primed with its recorded history.",
		},
		s.handleAgentStop)
}

// delegation resolves the agent-tool backend, standing the coordinator up
// lazily. Only a bare, externally-launched `ctxloom mcp` ever gets here: a
// run/acp-hosted session's stdio shim forwards to the runner instead
// (mcp_forward.go), and the runner serves the agent tools as plane-2 frames
// through coordinationHandler, which never touches this state.
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
	out, err := d.c.AgentRun(ctx, d.self, in.Agent, in.Prompt, in.Workspace, in.DirtyTreeHandler)
	if err != nil {
		return nil, nil, err
	}
	return nil, &agentRunResult{
		Harp:             out.Harp,
		LLM:              out.Engine,
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
	var structured json.RawMessage
	if len(in.Structured) > 0 {
		raw, merr := json.Marshal(in.Structured)
		if merr != nil {
			return nil, nil, fmt.Errorf("agent_send: encode structured: %w", merr)
		}
		structured = raw
	}
	disposition, err := d.c.AgentSend(d.self, in.To, in.Kind, in.Body, structured, in.InReplyTo)
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
	wait := mcpschema.ClampRecvWait(in.Wait)
	msgs, err := d.c.AgentRecv(ctx, d.self, wait)
	if err != nil {
		if errors.Is(err, coord.ErrRecvTimeout) {
			return nil, nil, fmt.Errorf("%w (waited %s)", coord.ErrRecvTimeout, wait)
		}
		return nil, nil, err
	}
	out := &agentRecvResult{Messages: make([]agentBusMessage, 0, len(msgs))}
	for _, m := range msgs {
		bm := agentBusMessage{MessageID: m.ID, From: m.From, Kind: m.Kind, Body: m.Body}
		if len(m.Structured) > 0 {
			var structured map[string]any
			if uerr := json.Unmarshal(m.Structured, &structured); uerr != nil {
				// recvMail committed this batch as DELIVERED before the
				// projection ran, so a companion dropped here is gone rather
				// than redelivered. Naming it is the difference between the
				// caller knowing an approval arrived unanswerable and the
				// caller reading a courtesy note while the requesting child
				// blocks to its auto-decline.
				bm.StructuredError = uerr.Error()
				clidiag.Warn("ctxloom", "agent_recv: message %s from %s: structured companion could not be decoded: %v (already acked as delivered — dropped, not redelivered)", m.ID, m.From, uerr)
			} else {
				bm.Structured = structured
			}
		}
		out.Messages = append(out.Messages, bm)
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
