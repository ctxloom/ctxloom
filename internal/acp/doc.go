// Package acp implements a GENERIC Agent Client Protocol (ACP) client backend:
// one driver that speaks the standard Agent Client Protocol
// (https://agentclientprotocol.com) to any ACP-capable coding agent — `kiro-cli
// acp`, `claude-agent-acp`, `codex-acp`, a future `agy acp` — instead of a bespoke
// Go backend per engine.
//
// ACP is layered ON TOP of ctxloom's existing go-plugin gRPC transport: this
// client runs inside the plugin, spawns `<agent> acp` as a subprocess, and speaks
// JSON-RPC 2.0 over the child's stdio. It maps the agent's `session/update`
// stream onto ctxloom's backend-agnostic chat entries (agent.ChatEvent), so a
// frontend consumes ACP agents through the same StructuredChat seam claude-code
// uses — with the bonus that ACP's `agent_thought_chunk` finally surfaces
// summarized reasoning as EntryTypeThinking (claude-code's stream-json strips it).
//
// # SDK decision: own wire types (api/), hand-rolled codec (jsonrpc.go)
//
// THIS WAS RE-DECIDED in the SDK1 slice (2026-07-16). Originally we vendored
// github.com/joshgarnett/agent-client-protocol-go/acp/api for the wire types.
// That module turned out to be frozen: no commits to main since 2025-09-02,
// ~10 months stale against the spec. An unmerged side branch DID update the
// schema, but sat with no open PR for 8+ months and was, itself, already
// stale against the CURRENT spec (missing SessionConfigOption,
// SessionCapabilities/AgentAuthCapabilities, session_info_update; its
// session/set_model has since been superseded by the spec's generalized
// session/set_config_option mechanism). Rather than depend on a frozen
// module or an abandoned, already-outdated fork, we now hand-author our own
// wire types directly from the vendored current spec — see api/doc.go for
// the full rationale and provenance.
//
// The newline-delimited JSON-RPC 2.0 codec was ALREADY ours before this
// decision, and stays exactly as it was: the SDK's connection layer is built
// on golang.org/x/exp/jsonrpc2, which hardcodes the LSP-style Content-Length
// "HeaderFramer" with no override hook — but ACP frames messages as
// NEWLINE-DELIMITED JSON over stdio, making that connection layer
// wire-INCOMPATIBLE with real ACP agents (its own client↔agent tests pass
// only because both ends share the wrong framer). jsonrpc.go supplies the
// minimal newline-delimited JSON-RPC 2.0 peer this needs; re-vendoring the
// wire types changed nothing about that decision.
//
// # Files
//
//	doc.go      — this overview + the SDK decision + the registration TODO
//	api/        — ctxloom's own ACP wire types (see api/doc.go)
//	jsonrpc.go  — the newline-delimited JSON-RPC 2.0 codec (framing + duplex peer)
//	mapping.go  — THE CORE: session/update → agent.ChatEvent + permission decision
//	session.go  — the session lifecycle driver (initialize→new→prompt→cancel) and
//	              the agent→client callback handler (permission, fs read/write)
//	acp.go      — ACPConfig, the ACP backend, argv builder, subprocess transport
//
// # Registration + materialization delegation (RESOLVED)
//
// The registry carries ONE generic "acp" descriptor (backend + config decode
// only — see the invariant-test exemption). The materialization-delegation
// question dissolved rather than needing a fan-out mechanism:
//
//   - KNOWN agents' ACP paths ride their OWN backends: kiro and codex implement
//     agent.StructuredChat by delegating to this package's driver (NewChatDriver)
//     with their agent-specific ACP command. Their descriptors already carry the
//     correct writer/exports, so config materialization is the target's own —
//     delegation for free, no "acp-kiro"-style descriptors.
//   - The generic "acp" descriptor exists for ARBITRARY/unlisted ACP agents
//     (`type: acp` + `command: "<agent> acp"`). A generic agent's native config
//     format is unknown, so it deliberately materializes nothing: context still
//     reaches a run as the lead fragment / prompt, and caller-supplied MCP
//     servers (ChatRequest.MCPServers) ride session/new's mcpServers.
package acp
