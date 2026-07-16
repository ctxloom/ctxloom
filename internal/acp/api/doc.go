// Package api is ctxloom's OWN Agent Client Protocol wire-type package: the
// SDK1 slice's re-vendor of what used to be
// github.com/joshgarnett/agent-client-protocol-go/acp/api.
//
// # D-SDK: why hand-authored types instead of an updated dependency
//
// The pinned SDK (joshgarnett/agent-client-protocol-go @ 69cbaf95b89e,
// 2025-09-02) is ~10 months stale against the spec. Before re-vendoring, this
// slice assessed the upstream repo directly (2026-07-16):
//
//   - main is FROZEN at exactly the pinned commit: zero commits since
//     2025-09-02, `pushed_at` 2025-11-06 is a push to a side branch, not main.
//   - That side branch (claude/review-agent-client-protocol-..., commit
//     7c6d6db) DOES update the schema — it adds MethodSessionSetModel and a
//     session/set_model method the pinned SDK lacks. But it was never merged:
//     no open PR, no open issues, 8+ months stale with no visible maintainer
//     activity. Worse, diffing its generated types against the schema this
//     package is built from (schema-v1.19.0, 2026-07-06, see below) shows
//     THAT BRANCH IS ALSO STALE: it has no SessionConfigOption, no
//     SessionCapabilities/AgentAuthCapabilities, and no session_info_update —
//     and critically, session/set_model does not exist in schema-v1.19.0 at
//     all. The current spec generalized model selection into the
//     SessionConfigOption / session/set_config_option mechanism instead
//     (see AgentCapabilities.SessionCapabilities and the B4 note on
//     modelState in internal/acpagent/wire.go). Depending on that branch
//     would have bought us an API the spec has since abandoned.
//
// Decision: GENERATE our own types directly from the vendored current spec
// (the same schema internal/acptest validates against), rather than take a
// dependency on either the frozen main or the stale, unmerged, already-
// superseded branch. The newline-delimited JSON-RPC codec was ALREADY ours
// (internal/acp/jsonrpc; see internal/acp/doc.go) — this package replaces
// only the wire-type half of the old SDK dependency, and the
// github.com/joshgarnett/agent-client-protocol-go module dependency is
// dropped from go.mod entirely.
//
// # Provenance
//
// Hand-authored from internal/acptest/acp-schema-v1.json (the same vendored
// copy the L0 conformance harness validates against):
//
//	Source:  https://github.com/agentclientprotocol/agent-client-protocol
//	Path:    schema/v1/schema.json (the STABLE v1 schema)
//	Commit:  a34b896504dd86136f80aab0e69de7a77bacc181 (2026-07-06)
//	Version: schema-v1.19.0
//
// This package intentionally does NOT cover the full 142-definition schema:
// it defines the request/response/notification/union shapes ctxloom's ACP
// client (internal/acp) and agent (internal/acpagent) roles actually use
// today, plus the capability marker types needed to make AgentCapabilities'
// new fields (McpCapabilities, SessionCapabilities, AgentAuthCapabilities —
// see the L0 checklist's B5) expressible. Terminal (B1), auth/logout (C3),
// session config options (CO1), HTTP/SSE MCP transports, and the
// session-list/resume/close/delete surface are OUT of this slice's scope —
// their types are not yet modeled here; add them in the slice that
// implements them, reading the same vendored schema this package was built
// from.
//
// Re-derive by re-reading internal/acptest/acp-schema-v1.json's $defs (or a
// freshly `curl`'d schema.json per that package's doc comment) against this
// package's structs.
package api
