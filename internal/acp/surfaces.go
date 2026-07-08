package acp

// Surface-delivery seam assessment: the generic ACP backend has NO filesystem
// surfaces. It shares the one cell-based Setup path (so there is a single Setup),
// but delivers an agent.EmptySurfaceSet — Setup runs the lifecycle merge that
// feeds ManagedChatMCPServers, and materializes NOTHING to the filesystem. There
// are still no agent.Delivery surface objects here (an empty set is not a surface),
// consistent with the opt-out this file records.
//
// The other launch backends (claude, codex, antigravity, kiro) each materialize
// their loadout as files at engine-native well-known paths — CLAUDE.md, .mcp.json,
// .codex/config.toml, .agents/*, .kiro/* — so each gets a set of agent.Delivery
// surface objects the isolation cells dispatch. The generic "acp" descriptor has
// no such paths to write:
//
//   - It materializes NOTHING to the filesystem. A generic ACP agent's native
//     config format is unknown by construction (any ACP-capable CLI), so the
//     descriptor writes no engine files at all — acpSkills.RegisterFromContent is
//     a no-op, and there is no settings writer (see doc.go, capabilities.go).
//
//   - Its loadout rides the PROTOCOL, not files. Context reaches a run in-band as
//     the lead fragment / prompt over `session/prompt`; caller-supplied MCP servers
//     ride `session/new`'s `mcpServers` field (agent.ChatRequest.MCPServers →
//     mapping.go). Both are JSON-RPC payloads on the live session, not writes into
//     a cwd — so there is no well-known file for a Delivery to produce and no
//     shared file for a race to clobber. The race-safety distinction the cells
//     encode (Delivery vs RaceSafeDelivery) is therefore vacuous for acp.
//
//   - KNOWN agents' ACP paths do NOT flow through this package's setup. kiro and
//     codex speak ACP by delegating to acp.NewChatDriver from their OWN backends;
//     their file surfaces are their own package's concern (internal/kiro,
//     internal/codex), materialized through their own writers — the generic acp
//     backend never owns them.
//
// Conclusion: acp's delivery stays in the ACP session layer, OUTSIDE the
// file-writing surface seam. There are intentionally no agent.Delivery /
// agent.RaceSafeDelivery surface objects in this package, and none should be added
// — forcing file-writing surfaces onto a protocol-only backend would invent
// well-known paths that no ACP agent reads. This file exists only to record that
// opt-out where the other backends' surfaces.go live, so the absence is a
// documented decision rather than an oversight. If a future generic-ACP config
// materialization is designed (see doc.go's registration notes), THAT mechanism —
// not this seam — is where it belongs.
