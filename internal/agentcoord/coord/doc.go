// Package coord is the agentcoord.v1 runtime coordinator library (Wave B1).
//
// It is stood up as a LIBRARY by every session-owning process — `ctxloom run`,
// `ctxloom acp`, and (as the orphaned-orchestrator fallback) a bare
// `ctxloom mcp` — and owns everything runtime-state-shaped about agent
// delegation:
//
//   - the durable CQRS stores (run registry, spawn queue, roster, role
//     mailboxes, interaction journal): append-only JSONL journals, a single
//     writer goroutine per journal, append-then-apply, fsync before any
//     durability-asserting response, torn-tail truncation on replay, and
//     fact-carried time only (folds never call time.Now);
//   - credential-as-identity: a per-runner 256-bit bearer token minted at
//     spawn; only SHA-256(token) is persisted (in the run-registry journal,
//     0600 files / 0700 dirs); verification is constant-time per request;
//     revocation at run end severs the credential's streams and parked polls;
//   - the agentcoord.v1 gRPC server — RunnerChannel (Wave B1), RunChannel
//     (Wave C1/B1.6), and the unary PublishEvents fallback (Wave C4,
//     publish.go) are all live — and the streamable-HTTP MCP endpoint
//     children and the parent harness dial with `CTXLOOM_COORD_URL` +
//     `CTXLOOM_COORD_CRED`;
//   - runner dial-home lifecycle: RunnerHello / heartbeats / RunExited, with
//     coordinator-side RunExited synthesis on RunnerChannel disconnect or
//     missed heartbeats — the fix for the child-death queue-stranding bug —
//     reconciled exactly-once with the legacy chat-stream-close path (which,
//     post Wave C4, survives only for the degraded no-reach-back spawn
//     fallback and non-allowlisted StructuredChat backends — see
//     spawner.go's viaStartRunBackends);
//   - the delegation orchestration that used to live in the parent's
//     `ctxloom mcp` process (spawn queue under the D4 cap, §6a
//     delivery-by-state, resume of ended children, oneshot bridging, the
//     observation TapHub and the surviving bus verbs observe/roster/inject);
//   - artifact transfer (Wave E1, artifacts.go/artifactstore.go/
//     homeartifacts.go): agentcoord.v1.ArtifactTransferService (chunked
//     upload/download, credentialed on the SAME connection as
//     RunnerChannel/RunChannel) backed by a content-addressed blob store
//     (~/.ctxloom/coord/<project>/artifacts/<sha256>) — bytes move THROUGH
//     the coordinator because containers and worktrees are not a shared
//     filesystem. Deliberately OUTSIDE the CQRS boundary below: the store
//     has no fold/projection of its own (the filesystem, addressed by
//     content hash, is already its own source of truth); the LOGICAL
//     manifest fact (ArtifactProduced, which artifact_id points at which
//     content) still rides the ordinary CQRS run registry (reports.go),
//     unchanged in kind, only richer in field (upload_id).
//
// SCOPE BOUNDARY (plan, CQRS discipline): CQRS applies to this runtime
// coordinator's state ONLY — config loading, the selection builder, and
// surface delivery have no read model and stay event-free.
//
// D3 EVOLUTION (Wave C2 — ladder.go/approval.go): D3 originally meant
// "children never prompt" — a delegated child had to declare a
// headless-safe permission_mode (bypass|plan) because it had no channel to
// surface an interactive prompt, and a forwarded engine permission request
// was auto-decided at the driver boundary (allow under bypass, else
// reject). permission_mode is UNCHANGED and still gates what a child may
// even attempt headless — that structural floor stands. What changes: a
// migrated (StartRun) child now ALWAYS forwards its engine's permission
// requests as plane-2 ApprovalRequests (harnessspec.go), and the
// coordinator's ESCALATION LADDER decides — auto_accept, auto_decline, or
// RELAY the request as durable mail to the parent role, which may itself be
// a human-attended session. D3 restated: children may now park on
// approvals; the "never prompts" guarantee becomes "never prompts a HUMAN
// unless a ladder rung says so" (relay_to_role / surface_to_human) — every
// other path (auto_accept, auto_decline, a relay's timeout fallback) still
// resolves without a human anywhere in the loop, bottoming at CANCEL when no
// rung resolved anything at all.
package coord
