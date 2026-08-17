// Package mcpschema is the proto-canonical MCP tool surface: the binding
// table (tool name → agentcoord.v1 message), the three-way routing registry,
// the descriptor→JSON-Schema projection rules, and the checked-in generated
// schemas (goldens, embedded for runtime registration).
//
// PROTOBUF IS THE CANONICAL REPRESENTATION (topology-reversal decision):
// tool names are stable UX, but every input/output shape is a
// projection of the contract in internal/agentcoord/coordination.proto.
// Generation is BUILD-TIME (go:generate / `just gen-mcp-schemas`): proto
// comments live in SourceCodeInfo, which protoc-gen-go strips from the
// descriptors it embeds in generated code — runtime protoreflect cannot see
// them — so the generator reads a buf-built FileDescriptorSet WITH source
// info, merges comments with the annotations.proto metadata (required /
// example / doc-override), and emits schemas/*.json. The checked-in schemas
// ARE the goldens; CI regenerates and diffs (gen-docs-check precedent).
package mcpschema

import "time"

// Tool names — stable UX, decoupled from the proto message names they bind.
const (
	ToolAgentRun           = "agent_run"
	ToolAgentSend          = "agent_send"
	ToolAgentRecv          = "agent_recv"
	ToolAgentStop          = "agent_stop"
	ToolAgentReport        = "agent_report"
	ToolRoster             = "roster"
	ToolAgentFetchArtifact = "agent_fetch_artifact"
)

// CoordinatorOnlyTools returns the set of tools a LEAF delegated agent must
// NOT receive (the trust-boundary gate, internal/mcp/mcp_runner.go's
// registration loop): agent_run, roster, agent_stop, and
// agent_fetch_artifact all either spawn/observe/control OTHER children or
// retrieve another agent's artifacts — capabilities that make sense only for
// a coordinator. A leaf keeps agent_send/agent_recv/agent_report (parent
// reporting only). The top-level human session is never gated by this (see
// llm_serve.go's leaf computation); only a delegated child's runner consults
// it.
func CoordinatorOnlyTools() map[string]bool {
	return map[string]bool{
		ToolAgentRun:           true,
		ToolRoster:             true,
		ToolAgentStop:          true,
		ToolAgentFetchArtifact: true,
	}
}

// Binding maps one coordination tool onto its proto messages. Input/Output
// name agentcoord.v1 messages; an empty name means the shape is SYNTHETIC —
// declared by the corresponding builder below because no wire frame exists
// for it (agent_recv parks against the runner's local notice buffer; no
// polling frame exists or is added).
type Binding struct {
	Tool   string
	Input  string // proto full name, e.g. "agentcoord.v1.SpawnAgentRequest"
	Output string

	// Description overrides the bound message's doc for the TOOL description
	// (used only when Input is synthetic; message-backed tools carry their
	// description on the message's (message_schema).doc annotation).
	Description string

	// SyntheticInput / SyntheticOutput build the schema for a side with no
	// bound message. They receive the Projector so they can embed generated
	// message projections (agent_recv's output embeds PeerMessage).
	SyntheticInput  func(p *Projector) (map[string]any, error)
	SyntheticOutput func(p *Projector) (map[string]any, error)

	// Route is this binding's classification (Routes(), below). The zero
	// value is RouteCoordination — every binding predating E1 rides typed
	// RunChannel plane-2 frames, so leaving this unset is correct for them.
	// E1's agent_fetch_artifact is the first binding to set it explicitly:
	// its schema is proto-derived like every other binding here, but it is
	// served by a runner-local handler that calls ArtifactTransferService
	// directly, never a typed AgentRequest/CoordinatorResponse frame — see
	// RouteArtifactFetch's doc.
	Route Route
}

// CoordinationBindings is the binding table: every coordination tool and the
// contract messages its arguments/results project. Order is the generation
// order (stable output).
func CoordinationBindings() []Binding {
	return []Binding{
		{
			Tool:   ToolAgentRun,
			Input:  "agentcoord.v1.SpawnAgentRequest",
			Output: "agentcoord.v1.SpawnAgentResult",
		},
		{
			Tool:   ToolAgentSend,
			Input:  "agentcoord.v1.PeerSendRequest",
			Output: "agentcoord.v1.PeerSendResult",
		},
		{
			Tool:        ToolAgentRecv,
			Description: "Receive pending mailbox messages for this session, waiting (parked at this session's runner) up to the bounded timeout when none are pending. A child parked here yields its execution slot. Delivery is at-least-once: unconsumed deliveries are re-delivered after a crash, deduped on message_id. On timeout the call fails and you are expected to drop the coordination: write your report/deferral state and finish.",
			SyntheticInput: func(*Projector) (map[string]any, error) {
				return map[string]any{
					"type": "object",
					"properties": map[string]any{
						"wait": map[string]any{
							"type":        "integer",
							"description": RecvWaitDoc,
						},
					},
					"additionalProperties": false,
				}, nil
			},
			SyntheticOutput: func(p *Projector) (map[string]any, error) {
				msg, err := p.MessageSchema("agentcoord.v1.PeerMessage")
				if err != nil {
					return nil, err
				}
				return map[string]any{
					"type": "object",
					"properties": map[string]any{
						"messages": map[string]any{
							"type":  "array",
							"items": msg,
						},
					},
				}, nil
			},
		},
		{
			Tool:   ToolAgentStop,
			Input:  "agentcoord.v1.StopRun",
			Output: "agentcoord.v1.StopRunResult",
		},
		{
			Tool:  ToolAgentReport,
			Input: "agentcoord.v1.Summary",
			SyntheticOutput: func(*Projector) (map[string]any, error) {
				return map[string]any{
					"type": "object",
					"properties": map[string]any{
						"journaled": map[string]any{
							"type":        "boolean",
							"description": "The report is a durable journaled fact",
						},
						"artifact_ids": map[string]any{
							"type":        "array",
							"items":       map[string]any{"type": "string"},
							"description": "Plan-manifest artifacts stamped by this report (session-dir *.plan.md files, content-addressed)",
						},
					},
				}, nil
			},
		},
		{
			Tool:   ToolRoster,
			Input:  "agentcoord.v1.ListRunsRequest",
			Output: "agentcoord.v1.ListRunsResult",
		},
		{
			// E1d: retrieve a reported artifact's bytes to a local path.
			// RouteArtifactFetch, NOT RouteCoordination — see Binding.Route
			// and RouteArtifactFetch's doc for why.
			Tool:   ToolAgentFetchArtifact,
			Input:  "agentcoord.v1.FetchArtifactRequest",
			Output: "agentcoord.v1.FetchArtifactResult",
			Route:  RouteArtifactFetch,
		},
	}
}

// Route classifies where a tool terminates — the three-way routing table
// (plan B1.6 deliverable 3). The registry is exhaustive over the ctxloom MCP
// surface: a tool the surface serves without a classification here is a
// STARTUP ERROR at the runner (never a silent fallthrough), enforced by the
// runner server builder and the completeness test.
type Route int

const (
	// RouteCoordination tools become typed plane-2 frames on the run's
	// RunChannel (the binding table above names the message).
	RouteCoordination Route = iota
	// RouteCellLocal tools are served locally by the runner: the data they
	// read (config, fragments, library clones) was delivered into the cell.
	RouteCellLocal
	// RouteHostRelay tools need host-resident state (cross-session history,
	// transcript stores) that is not mounted into children; they relay as
	// CustomRequest{name: "ctxloom/<tool>"} with coordinator-side handlers.
	RouteHostRelay
	// RouteArtifactFetch (E1d) tools stream bytes through the DEDICATED
	// ArtifactTransferService, never the RunChannel typed-frame plane-2
	// path (a chunked transfer has no business riding the same channel as
	// status/event traffic — the whole point of splitting artifacts.proto
	// out in the first place). The schema is still proto-derived like every
	// RouteCoordination tool; only the SERVING mechanism differs: a
	// runner-local handler calls DownloadArtifact directly on the runner's
	// own credentialed connection, verifies the content hash, and places
	// the file cell-locally — never a typed AgentRequest/
	// CoordinatorResponse round-trip.
	RouteArtifactFetch
)

// Routes returns the classification of EVERY tool on the ctxloom MCP
// surface. Keep in lockstep with the stdio server's registrations
// (mcp_server.go registerTools) — the completeness test cross-checks.
func Routes() map[string]Route {
	return map[string]Route{
		// Coordination — typed frames (binding table above).
		ToolAgentRun:    RouteCoordination,
		ToolAgentSend:   RouteCoordination,
		ToolAgentRecv:   RouteCoordination,
		ToolAgentStop:   RouteCoordination,
		ToolAgentReport: RouteCoordination,
		ToolRoster:      RouteCoordination,

		// Artifact transfer (E1d) — the dedicated chunked-transfer service,
		// not a typed plane-2 frame.
		ToolAgentFetchArtifact: RouteArtifactFetch,

		// Cell-local content — the data was delivered into the cell.
		"assemble_context": RouteCellLocal,
		"search_content":   RouteCellLocal,
		"search_library":   RouteCellLocal,

		// Host-resident — session dirs/transcript stores live on the host.
		"compact_session":      RouteHostRelay,
		"load_session":         RouteHostRelay,
		"recover_session":      RouteHostRelay,
		"get_previous_session": RouteHostRelay,
		"list_sessions":        RouteHostRelay,
		// Host-resident — the task store (~/.ctxloom/tasks/<project-id>.jsonl)
		// and the project's git history are not mounted into an isolated
		// child cell.
		"evaluate_triggers": RouteHostRelay,
		// Host-resident — the context-occupancy series lives beside the
		// session's other persisted state (~/.ctxloom/sessions/<harp>/persist),
		// which an isolated child cell does not mount.
		"context_status": RouteHostRelay,
	}
}

// DistillBudget is what a transcript distillation is actually allowed to take.
// The work is ceil(chunks / distillConcurrency) waves of LLM subprocesses over
// a whole transcript, so it scales with session length, not with round-trip
// latency: a long session runs to many minutes of entirely healthy work. The
// generic plane-2 budget is sized for a coordination frame — a round trip —
// and billing distillation against it failed every large recover mid-flight.
// This is a backstop against a wedged host, not a performance target; it is
// deliberately far past any honest distillation. It bounds BOTH sides of the
// relay: how long the caller waits, and how long the host lets the work run —
// one number, so the two can't drift into a host that outlives its caller's
// patience by design.
const DistillBudget = 30 * time.Minute

// relayBudgets overrides the caller's default plane-2 request budget for the
// host-relay tools whose work is measured in minutes. A tool absent here keeps
// the default, so a genuinely hung request still fails fast.
var relayBudgets = map[string]time.Duration{
	"compact_session":      DistillBudget,
	"load_session":         DistillBudget,
	"recover_session":      DistillBudget,
	"get_previous_session": DistillBudget,
	// list_sessions is a fast index read by default, but distill_missing=true
	// compacts every title-less/stale row inline — the same minutes-long LLM
	// work the other distillation tools do, so it needs the same backstop.
	"list_sessions": DistillBudget,
}

// RelayBudget returns how long a relayed tool's plane-2 request may take, or
// zero to keep the caller's default.
func RelayBudget(tool string) time.Duration { return relayBudgets[tool] }
