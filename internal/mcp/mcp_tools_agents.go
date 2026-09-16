package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ctxloom/ctxloom/internal/agentcoord/coord"
	"github.com/ctxloom/ctxloom/internal/agentcoord/mcpschema"
	"github.com/ctxloom/ctxloom/internal/lm/isolation"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/harp"
)

// Agent-delegation tools (agent_run / agent_send / agent_recv / agent_stop),
// backed by the runtime coordinator (internal/agentcoord/coord).
//
// ONE coordinator per project, hosted by a RUNNER — `ctxloom run` stands it
// up (coord_host.go) and hands its engine the reach-back env. An MCP server
// is a CLIENT of that arrangement and never a host:
//
//   - FORWARDER: the stdio shim finds the runner (env socket or discovery
//     marker, mcp_server.go) and becomes a pure stdio↔HTTP proxy onto it;
//     the runner serves the agent tools as plane-2 frames and never touches
//     this file's state.
//   - RELAY: a host-resident tool relayed BACK from the runner runs here on a
//     per-caller ctxServer whose agents field is pre-bound to the
//     already-hosted coordinator (coordCustomHandlers).
//   - BARE LOCAL: a `ctxloom mcp` with no reachable runner (the `.mcp.json`
//     install) still ADVERTISES the agent tools, and every one of them
//     refuses with errNoRunner. A server that built one lazily here instead
//     would be a stdio relay able to promote itself into a second
//     session-owning process for the project — the rival-coordinator class
//     this boundary exists to eliminate.

// errNoRunner is the refusal every agent tool returns from a bare stdio
// server: a coordinator is hosted only by a RUNNER, and this process found
// none to forward to. It names the remedy because the caller is an LLM
// reading a tool error, not a person reading a stack.
var errNoRunner = errors.New("agent delegation unavailable: no ctxloom runner is reachable from this MCP server, and an MCP server never hosts a coordinator itself — launch the session through `ctxloom run` so a runner exists, or make sure the runner's " + coord.EnvMCPSocket + " (or its discovery marker) reaches this process")

// agentDelegation is the coordinator-host state behind the agent tools.
type agentDelegation struct {
	self coord.Identity
	c    *coord.Coordinator
}

// selfIdentityFromEnv is the stdio server's ambient identity: the serving
// session's harp, always depth 0 — the executor role died with the shim
// (children run in forward mode and never reach this constructor).
//
// THE HARP IS NOT OPTIONAL. `ctxloom run` exports CTXLOOM_SESSION_HARP into
// the engine's env, so an engine it launched spawns this server WITH a harp.
// But `manage install` registers this server in .mcp.json as a bare
// `ctxloom mcp` with NO env at all (agent.WriteMCPConfig's generated entry),
// so a plain engine session — the most common installation — arrives with
// none. Every session-scoped tool on this surface keys off the harp
// (recovery, previous-session lookup, context status, trigger evaluation),
// and an empty one does not fail: it silently resolves to nothing, this
// codebase's characteristic exit-0-with-zero-bytes shape.
//
// So a serving process with no ambient session mints its own harp rather
// than running as an unaddressable one. It is a real, distinct identity for
// the lifetime of this process, deliberately NOT persisted — inventing a
// durable session identity for a session ctxloom did not launch would be a
// stronger claim than the evidence supports. It is NOT a coordinator
// identity: a bare server hosts no coordinator (see delegation()).
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
	Kind       string         `json:"kind,omitempty" jsonschema:"The message's kind, from a CLOSED vocabulary of exactly four values you may send: message (plain prose, claiming no special authority), result (your findings/verdict/deliverable), error (a failure the recipient must act on), question (you expect an answer back). REQUIRED for an ordinary send — an absent or unrecognised value is refused, naming these four — except when in_reply_to correlates to a coordinator-asked question: that reply's kind is not read at all (the answer in body is what resolves it), so it may be left unset. Every value outside these four (the coordinator's own reserved vocabulary) is rejected from a sender, not quietly downgraded."`
	Structured map[string]any `json:"structured,omitempty" jsonschema:"Optional structured companion, carried opaque — it is not read for a \"kind\" key; name the kind in the kind field above."`
	InReplyTo  string         `json:"in_reply_to,omitempty" jsonschema:"Correlates this reply to an earlier inbound message's message_id. It is the ONLY correlation key — set it to a coordinator question's message_id to answer that question."`
}

type agentSendResult struct {
	To          string `json:"to"`
	Disposition string `json:"disposition"`
}

type agentRecvInput struct {
	// A struct tag must be a literal, so it cannot reference
	// mcpschema.RecvWaitDoc the way the generated schema does;
	// TestAgentRecvWait_StdioSchemaDescribesTheSameBounds pins them equal.
	Wait int `json:"wait,omitempty" jsonschema:"Seconds to wait for a message (default 60, max 600); on timeout a coordinator gets a successful empty result with a disposition, a leaf an error"`
}

type agentBusMessage struct {
	MessageID  string         `json:"message_id,omitempty"`
	From       string         `json:"from"`
	Kind       string         `json:"kind,omitempty"`
	Body       string         `json:"body"`
	Structured map[string]any `json:"structured,omitempty" jsonschema:"Structured companion the sender attached, when there is one."`
	// StructuredError reports a companion that arrived but could not be
	// decoded. The mailbox commits a batch as delivered BEFORE this projection
	// runs, so such a payload is lost rather than redelivered — the caller has
	// to be able to tell "the sender attached nothing" from "the sender
	// attached something and it did not survive the trip".
	StructuredError string `json:"structured_error,omitempty" jsonschema:"Set when the sender attached a structured companion that could not be decoded; the body was still delivered but the structured field is absent and will NOT be redelivered. Treat this as a delivery fault, not an empty field."`
}

type agentRecvResult struct {
	Messages []agentBusMessage `json:"messages"`
	// Disposition is set only on a successful receive with nothing to
	// deliver, naming why (see recvOutcome): the call yielded to a newer
	// receive, or a coordinator's wait elapsed quietly.
	Disposition string `json:"disposition,omitempty"`
}

type agentStopInput struct {
	Harp   string `json:"harp,omitempty" jsonschema:"The ONE child session harp to stop (its engine is killed and its execution slot freed; a later agent_send resumes it as a fresh run). OMIT this to stop EVERY live child of this session instead (the bulk sweep), which then requires reason."`
	Reason string `json:"reason,omitempty" jsonschema:"Why you are stopping. REQUIRED when harp is omitted (the bulk sweep stops every live child of this session and is never done silently); optional when harp is given. Recorded on each run's terminal record and in the coordinator audit log, and shown in the roster's cause."`
}

// agentStoppedChild is one child of the bulk sweep, as the stdio server
// reports it (the plane-2 surface's StopRunResult.Child, same fields).
type agentStoppedChild struct {
	Harp    string `json:"harp"`
	RunID   string `json:"run_id"`
	Agent   string `json:"agent"`
	Outcome string `json:"outcome" jsonschema:"stopped: ended without a turn being cut short; interrupted: its turn was still running when the drain bound elapsed and it was forced"`
	Detail  string `json:"detail"`
}

type agentStopResult struct {
	// Harp and Disposition describe the ONE child stopped when harp was given.
	Harp        string `json:"harp,omitempty"`
	Disposition string `json:"disposition"`
	// Children is set ONLY by the bulk sweep (harp omitted): every child of
	// this session that was live when the sweep began, each with its outcome.
	Children []agentStoppedChild `json:"children,omitempty"`
}

// agentRunInputSchema is agent_run's advertised input schema: the shape
// inferred from agentRunInput, with its two per-call VOCABULARY arguments
// carrying the enum of what they actually accept.
//
// The prose in each field's description already listed the legal values, but
// prose is not a constraint: nothing rejected a spelling outside it, and the
// caller filling these arguments is a model. An `enum` is read by the model
// AND enforced by the SDK's own argument validation before the handler runs,
// which is the same posture the human-edited channel already has (the
// project config's JSON Schema enumerates both of these keys). The enums
// come from each vocabulary's owning package, so a member added there cannot
// leave this surface behind.
//
// A missing property PANICS rather than quietly advertising an unconstrained
// argument: it can only mean the struct field was renamed, and a schema that
// silently stopped constraining the argument that decides whether ctxloom
// commits the user's tree is the failure this whole surface exists to avoid.
func agentRunInputSchema() *jsonschema.Schema {
	schema, err := jsonschema.For[agentRunInput](nil)
	if err != nil {
		panic(fmt.Sprintf("agent_run: input schema: %v", err))
	}
	constrainToVocabulary(schema, "dirty_tree_handler", operations.DirtyTreeHandlerNames())
	constrainToVocabulary(schema, "workspace", isolation.WorkspaceNames())
	return schema
}

// constrainToVocabulary stamps a closed vocabulary onto one property of an
// inferred schema. See agentRunInputSchema for why an absent property is a
// panic rather than a no-op.
func constrainToVocabulary(schema *jsonschema.Schema, property string, members []string) {
	prop, ok := schema.Properties[property]
	if !ok {
		panic(fmt.Sprintf("agent_run: input schema has no %q property to constrain (renamed field?)", property))
	}
	enum := make([]any, 0, len(members))
	for _, m := range members {
		enum = append(enum, m)
	}
	prop.Enum = enum
}

func (s *ctxServer) registerAgentTools(server *mcp.Server) {
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "agent_run",
			InputSchema: agentRunInputSchema(),
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
			Description: "Stop delegated child sessions. This tool has TWO SHAPES, chosen by whether harp is given. (1) harp GIVEN: stop that ONE child. (2) harp OMITTED: stop EVERY live child of this session — the bulk sweep. Omitting harp is NOT \"stop nothing\" and there is no default target: it addresses all of your live children at once (each is asked to exit at its turn boundary and forced when the drain bound elapses), and the result names every child with its outcome. Because of that, the omitted-harp shape REQUIRES reason — an accidental omission is refused rather than stopping everything silently. In both shapes a stopped child's engine (or container) is killed, its execution slot frees immediately (the spawn queue advances), its credential is revoked, and the stop is journaled. The session stays resumable — a later agent_send relaunches it as a fresh run primed with its recorded history. Use the sweep to reclaim slots and containers held by children that have finished (they stay open, resumable, until stopped).",
		},
		s.handleAgentStop)
}

// delegation resolves the agent-tool backend. It is bound at construction
// (coordCustomHandlers, on the runner's already-hosted coordinator) or it is
// absent — a bare stdio server with no runner refuses with errNoRunner.
// Nothing is built here: standing a coordinator up is the session host's
// job (coord_host.go), and a server that could do it lazily could become a
// second owner of the project.
func (s *ctxServer) delegation() (*agentDelegation, error) {
	if s.agents == nil {
		return nil, errNoRunner
	}
	return s.agents, nil
}

func (s *ctxServer) handleAgentRun(ctx context.Context, _ *mcp.CallToolRequest, in agentRunInput) (*mcp.CallToolResult, *agentRunResult, error) {
	d, err := s.delegation()
	if err != nil {
		return nil, nil, err
	}
	// THIS IS THE EDGE for the per-call dirty-tree vocabulary on the native
	// surface: the argument is a model-supplied string, and the member it
	// would otherwise default to auto-commits the user's working tree. Parse
	// it here, so an unrecognized spelling is refused at the tool call
	// naming the legal values, and only the typed value travels inward.
	dirtyTreeHandler, err := operations.ParseDirtyTreeHandler(in.DirtyTreeHandler)
	if err != nil {
		return nil, nil, fmt.Errorf("agent_run: %w", err)
	}
	out, err := d.c.AgentRun(ctx, d.self, in.Agent, in.Prompt, in.Workspace, dirtyTreeHandler)
	if err != nil {
		return nil, nil, err
	}
	return nil, &agentRunResult{
		Harp:             out.Harp,
		LLM:              out.Engine,
		Profiles:         out.Profiles,
		Runtime:          string(out.Runtime),
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
		// Role, not transport, picks the verdict shape; recvOutcome holds
		// the leaf/coordinator asymmetry and the reason it must stay.
		disposition, failure := recvOutcome(err, wait, d.self.IsChild())
		if failure != nil {
			return nil, nil, failure
		}
		return nil, &agentRecvResult{Messages: []agentBusMessage{}, Disposition: disposition}, nil
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

func (s *ctxServer) handleAgentStop(ctx context.Context, _ *mcp.CallToolRequest, in agentStopInput) (*mcp.CallToolResult, *agentStopResult, error) {
	d, err := s.delegation()
	if err != nil {
		return nil, nil, err
	}
	if in.Harp == "" {
		// The bulk sweep: every live child of this session.
		stopped, err := d.c.StopChildren(ctx, d.self, in.Reason)
		if errors.Is(err, coord.ErrStopReasonRequired) {
			return nil, nil, fmt.Errorf("%w — give harp to stop one child, or reason to stop them all", err)
		}
		if err != nil {
			return nil, nil, fmt.Errorf("agent_stop: %w", err)
		}
		out := &agentStopResult{Disposition: fmt.Sprintf("stopped %d child(ren) of this session; their execution slots are freed (a later agent_send resumes any of them as a fresh run)", len(stopped))}
		if len(stopped) == 0 {
			out.Disposition = "no live children to stop"
		}
		for _, sc := range stopped {
			out.Children = append(out.Children, agentStoppedChild{Harp: sc.Harp, RunID: sc.RunID, Agent: sc.Agent, Outcome: sc.Outcome, Detail: sc.Detail})
		}
		return nil, out, nil
	}
	disposition, err := d.c.AgentStop(d.self, in.Harp, in.Reason)
	if err != nil {
		return nil, nil, err
	}
	return nil, &agentStopResult{Harp: in.Harp, Disposition: disposition}, nil
}
