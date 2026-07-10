// Package mcpschema is the proto-canonical MCP tool surface: the binding
// table (tool name → agentcoord.v1 message), the three-way routing registry,
// the descriptor→JSON-Schema projection rules, and the checked-in generated
// schemas (goldens, embedded for runtime registration).
//
// PROTOBUF IS THE CANONICAL REPRESENTATION (topology-reversal decision,
// 2026-07-09): tool names are stable UX, but every input/output shape is a
// projection of the contract in internal/agentcoord/coordination.proto.
// Generation is BUILD-TIME (go:generate / `just gen-mcp-schemas`): proto
// comments live in SourceCodeInfo, which protoc-gen-go strips from the
// descriptors it embeds in generated code — runtime protoreflect cannot see
// them — so the generator reads a buf-built FileDescriptorSet WITH source
// info, merges comments with the annotations.proto metadata (required /
// example / doc-override), and emits schemas/*.json. The checked-in schemas
// ARE the goldens; CI regenerates and diffs (gen-docs-check precedent).
package mcpschema

// Tool names — stable UX, decoupled from the proto message names they bind.
const (
	ToolAgentRun    = "agent_run"
	ToolAgentSend   = "agent_send"
	ToolAgentRecv   = "agent_recv"
	ToolAgentStop   = "agent_stop"
	ToolAgentReport = "agent_report"
	ToolRoster      = "roster"
)

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
			Tool: ToolAgentRecv,
			Description: "Receive pending mailbox messages for this session, waiting (parked at this session's runner) up to the bounded timeout when none are pending. A child parked here yields its execution slot. Delivery is at-least-once: unconsumed deliveries are re-delivered after a crash, deduped on message_id. On timeout the call fails and you are expected to drop the coordination: write your report/deferral state and finish.",
			SyntheticInput: func(*Projector) (map[string]any, error) {
				return map[string]any{
					"type": "object",
					"properties": map[string]any{
						"wait": map[string]any{
							"type":        "integer",
							"description": "Seconds to wait for a message (default 60, max 600). On timeout the call fails: drop the coordination, write your report/deferral state, and finish",
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

		// Cell-local content — the data was delivered into the cell.
		"assemble_context": RouteCellLocal,
		"search_content":   RouteCellLocal,
		"search_library":   RouteCellLocal,

		// Host-resident — session dirs/transcript stores live on the host.
		"compact_session":      RouteHostRelay,
		"load_session":         RouteHostRelay,
		"recover_session":      RouteHostRelay,
		"get_previous_session": RouteHostRelay,
	}
}
