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
// # SDK decision: schema-generated wire types (a maintained fork), hand-rolled codec (jsonrpc.go)
//
// THIS WAS RE-DECIDED TWICE. Originally we vendored
// github.com/joshgarnett/agent-client-protocol-go/acp/api for the wire types.
// That module turned out to be frozen: no commits to main since 2025-09-02,
// ~10 months stale against the spec. An unmerged side branch DID update the
// schema, but sat with no open PR for 8+ months and was, itself, already
// stale against the CURRENT spec. The SDK1 slice (2026-07-16) reacted to
// that by hand-authoring ~1,040 lines of ACP types directly from the
// vendored current spec — a stopgap with no generator and no regeneration
// path, which was itself a mistake: ACP's types are DEFINED BY its JSON
// schema, so a maintained, schema-generated copy is the right dependency,
// not a hand-maintained one. The SDK1-fork slice replaced that hand-authored
// package with github.com/coder/acp-go-sdk, redirected via a TEMPORARY go.mod
// replace directive to github.com/benjaminabbitt/acp-go-sdk (branch
// schema-v1.19.0) — a fork carrying a generator + the current schema,
// pending that fork's PR merging upstream. See go.mod's replace-directive
// comment for the exact removal condition. Naming/shape parity with the
// prior hand-rolled types was NOT assumed: ContentBlock/SessionUpdate/
// ToolCallContent unions carry no discriminator field in the generated
// shape (dispatch switches on which variant pointer is non-nil instead),
// McpServer is now itself a transport union (ctxloom only ever builds the
// Stdio variant), and several id types (SessionModeId, TerminalId) are
// distinct string types rather than aliases.
//
// The newline-delimited JSON-RPC 2.0 codec was ALREADY ours before either
// decision, and stays exactly as it was: the SDK's connection layer is built
// on golang.org/x/exp/jsonrpc2, which hardcodes the LSP-style Content-Length
// "HeaderFramer" with no override hook — but ACP frames messages as
// NEWLINE-DELIMITED JSON over stdio, making that connection layer
// wire-INCOMPATIBLE with real ACP agents (its own client↔agent tests pass
// only because both ends share the wrong framer). jsonrpc.go supplies the
// minimal newline-delimited JSON-RPC 2.0 peer this needs. (The
// coder/acp-go-sdk fork's OWN connection.go, unlike the abandoned SDK, DOES
// frame newline-delimited — verified during the SDK1-fork slice — so this
// redundancy may be worth revisiting in a follow-up; not adopted in this
// slice per its own scope boundary.)
//
// # Files
//
//	doc.go      — this overview + the SDK decision + the registration TODO
//	jsonrpc.go  — the newline-delimited JSON-RPC 2.0 codec (framing + duplex peer)
//	mapping.go  — THE CORE: session/update → agent.ChatEvent + permission decision
//	session.go  — the session lifecycle driver (initialize→new→prompt→cancel) and
//	              the agent→client callback handler (permission, fs read/write)
//	acp.go      — ACPConfig, the ACP backend, argv builder, subprocess transport
//
// ACP wire types come from github.com/coder/acp-go-sdk (see go.mod) rather
// than a package in this tree.
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
