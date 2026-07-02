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
// # SDK decision: reuse the wire types, hand-roll the codec
//
// We use github.com/joshgarnett/agent-client-protocol-go/acp/api for the wire
// types (schema-generated, comprehensive, correct discriminated-union
// marshaling, and — verified — stdlib-only: it imports just encoding/json + fmt,
// so it pulls none of the SDK's heavier connection dependencies into ctxloom).
//
// We do NOT use the SDK's connection layer. It is built on golang.org/x/exp/
// jsonrpc2, which hardcodes the LSP-style Content-Length "HeaderFramer" with no
// override hook — but ACP frames messages as NEWLINE-DELIMITED JSON over stdio.
// The SDK's connection is therefore wire-INCOMPATIBLE with real ACP agents
// (its own client↔agent tests pass only because both ends share the wrong
// framer). So jsonrpc.go supplies a minimal newline-delimited JSON-RPC 2.0 peer.
// This is the "minimal internal JSON-RPC 2.0 codec" fallback, taken deliberately
// for wire-correctness while still reusing the SDK's structs.
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
// # TODO(acp): descriptor registration + materialization delegation
//
// This increment is deliberately UNREGISTERED — internal/lm/backends/registry.go
// is untouched — mirroring how internal/kiro first landed. Registering an "acp"
// descriptor trips TestDescriptorTable_Invariants, which requires every non-mock
// backend to carry a settings writer AND command exports/writer. A GENERIC ACP
// backend has no settings format of its own: its config materialization must
// DELEGATE to the TARGET agent's writer (kiro/claude/codex) selected by
// ACPConfig.AgentEngine — an unresolved design question:
//
//   - How does the descriptor pick the delegate writer/exports from agent_engine,
//     and how does an "acp" backend's Name() reconcile with the invariant that
//     descriptor name == backend Name()?
//   - Do we register ONE "acp" descriptor that fans out by agent_engine, or one
//     per target ("acp-kiro", …)? Either way the config-materialization delegation
//     (WriteSettings/exports/writeCommands → the target agent's) must be wired
//     before registration, or the invariant test (rightly) fails.
//
// Until that is settled, internal/acp stays green and standalone; nothing outside
// this package references it yet.
package acp
