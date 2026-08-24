# Wire contract — `agentcoord.v1`

The hand-written protos in `internal/agentcoord/` are the normative contract for
agent delegation: three gRPC services, 82 messages and 13 enums in
`coordination.proto` (1348 lines), 9 messages and 1 service in `artifacts.proto`
(207 lines), plus a vendored `google/rpc/status.proto`. The contract has **three**
consumer classes, and the third is what makes a dead field expensive: six of these
messages are projected by `internal/agentcoord/mcpschema` into the **JSON Schemas an
LLM reads and fills in**, so a field with no handler is a model-facing argument that
silently does nothing.

Generated `*.pb.go` is gitignored (`.gitignore:26`); the `.proto` files are the source
of truth.

```mermaid
flowchart TD
  subgraph Wire["coordination.proto — agentcoord.v1"]
    RF["RunnerFrame / RuntimeFrame<br/>runner lifecycle · :263-430"]
    AF["AgentFrame / CoordinatorFrame<br/>one run, 3 planes"]
    P1["plane 1 — AgentEvent :961<br/>15 payload arms, 12 produced"]
    P2U["plane 2 up — AgentRequest :524<br/>approval · user_input · spawn_agent<br/>peer_send · list_runs · stop_run · custom"]
    P2D["plane 2 down — CoordinatorRequest :826<br/>steer · question · summarize · pause · resume"]
    P3["plane 3 — Ack · Heartbeat · CoordinatorNotice :913"]
    AF --> P1 & P2U & P2D & P3
  end
  subgraph Art["artifacts.proto"]
    UP["UploadArtifact(stream) :88"]
    DL["DownloadArtifact → stream :95"]
  end
  MCP[["mcpschema/schemas/*.json<br/>LLM-facing tool surface"]]
  COORD[["coord/runchannel.go serve* handlers"]]
  RUNNER[["coord/enginehost.go · harnessspec.go"]]
  VIEW[["operations/sessionfeed.go · cli/run_owned.go"]]
  P2U --> COORD
  P1 --> VIEW
  RF --> RUNNER
  Art --> COORD
  P2U -. "SpawnAgentRequest · PeerSendRequest · StopRun<br/>ListRunsRequest · Summary · FetchArtifactRequest" .-> MCP
  P2D -. "23 of 82 messages: zero references repo-wide" .-> DEAD["dead surface"]
  style DEAD fill:#f8d7da,stroke:#b02a37
  style MCP fill:#fff3cd,stroke:#997404
```

## Services

| Service / RPC | file:line | Contract | Implementation |
|---|---|---|---|
| `CoordinatorService.RunnerChannel` | `coordination.proto:208` | bidi; runner lifecycle keyed by credential hash | server `coord/grpcserver.go:156`, client `coord/runnerlink.go:107` |
| `CoordinatorService.RunChannel` | `coordination.proto:221` | bidi; ONE run's three planes | server `coord/runchannel.go:116`, client `coord/home.go:259` |
| `ConsumerService.WatchRuns` | `coordination.proto:772` | snapshot frame, then live events | `coord/consumer.go:274`; client `operations/sessionfeed.go:181` |
| `ConsumerService.ListRuns` | `coordination.proto:776` | roster poll | `coord/consumer.go:266` |
| `ArtifactTransferService.UploadArtifact` | `artifacts.proto:88` | chunked upload, server-hashed | `coord/artifacts.go:81`; client `coord/homeartifacts.go:36` |
| `ArtifactTransferService.DownloadArtifact` | `artifacts.proto:95` | header-first stream | `coord/artifacts.go:198`; client `coord/homeartifacts.go:88` |

`CoordinatorService` carries no unary RPC: the at-least-once event fallback exists
only as the in-process `coord.Coordinator.PublishEvents`, called by the oneshot
bridging in `children.go` for a run it already owns. There is no authenticated wire
surface for it, because there was never a non-test client to serve.

`ConsumerService` is deliberately read-only — steer/inject is excluded and the proto
says why. Consumer credentials are refused on `CoordinatorService` by the auth
interceptors (`grpcserver.go:121`).

## Message families that matter

| Message | file:line | Role | Reality |
|---|---|---|---|
| `AgentEvent` | `coordination.proto:961` | the durable sequenced fact; "the coordinator's view is a pure fold over these" | envelope fields `task_id`, `turn_id`, `parent_item_id`, `traceparent` have **zero references repo-wide**; 4 of 15 payload arms are never constructed |
| `HarnessSpec` | `coordination.proto:319` | what to launch — `permission_mode` typed and enforced, everything else in an open `config` Struct | built+decoded in one file (`coord/harnessspec.go`), which is what keeps the three magic string keys honest |
| `SpawnAgentRequest` | `coordination.proto:623` | `agent_run` | the live payload has migrated *into* the untyped `input` Struct (`prompt`, `workspace`, `dirty_tree_handler`, read at `runchannel.go:724-737`); the typed `budget`/`constraints`/`notify_on` are read by nobody |
| `PeerSendRequest` | `coordination.proto:668` | `agent_send` | `to_agent_id`/`to_role`/`text`/`structured`/`in_reply_to` are live; `artifact_ids` is never read and `PeerMessage.artifacts` is never populated |
| `ListRunsRequest` / `ListRunsResult.RunInfo` | `coordination.proto:714,728` | `roster` | see [observation.md](observation.md) — 2 of 4 filters and 2 of 9 result fields are inert |
| `StopRun` | `coordination.proto:363` | used in **two directions with two contracts**: `RunnerRequest.stop_run=11` (runtime→runner, graceful) and `AgentRequest.stop_run=16` (parent→coordinator, hard kill, "grace is advisory") | its `(message_schema).doc` describes only the `agent_stop` sense and is projected into `schemas/agent_stop.json` |
| `Summary` | `coordination.proto:1269` | durable report event **and** the `agent_report` tool input | fully consumed (`coord/reports.go:180-200`); `mcp_runner.go:547-552` hard-rejects empty `text` and `SCOPE_UNSPECIFIED` — the fail-loud model for the rest of the file |
| `Result` / `Usage` | `coordination.proto:1098,1133` | terminal outcome + accounting | 5 of 10 `Result` fields and `Usage.per_model` have zero producers and zero consumers; the micro-USD discipline (`:1138-1145`) is correctly implemented at `enginehost.go:530` |
| `AgentIdentity` | `coordination.proto:468` | who this agent is | only `agent_id` and `role` are ever populated (`consumer.go:215`, `enginehost.go:269`); `runner_id` is documented as "coordinator-assigned and validated against the connection credential" and is never assigned |

## Enum zero-value semantics

Proto3 enums have no "unset"; the zero value is what an unfilled or forward-version
field decodes to. This table is the security-relevant audit.

| Enum | Zero value | Polarity | Evidence |
|---|---|---|---|
| `ApprovalDecision.Decision` | `DECISION_UNSPECIFIED` | **fails closed** | `enginehost.go:734-748` is an explicit allow-list; `approval.go:452` rejects it by name |
| `Summary.Scope` | `SCOPE_UNSPECIFIED` | **fails closed** | `mcp_runner.go:550-552` hard-rejects |
| `Result.RunStatus` | `RUN_STATUS_UNSPECIFIED` | **fails open** | every consumer tests `== RUN_STATUS_FAILED`, so `run_owned.go:271` exits 0 for UNSPECIFIED, CANCELLED and TIMED_OUT; `children.go:862` records no failure reason |
| `MessageChannel` | `MESSAGE_CHANNEL_UNSPECIFIED` | **read two opposite ways** | `children.go:830` treats unset as *not* final (dropped from the turn accumulator); `operations/sessionfeed.go:237-241` renders it as user-facing assistant output |
| `ArtifactKind`, `InteractionRecorded.Resolution`, `PeerSendResult.Delivery`, `ApprovalKind`, `MessageRole` | — | neutral (always explicitly set, or lookup-fallback only) | — |
| `StepCompleted.Outcome`, `StatusChanged.Phase`, `SteerResult.Applied`, `SpawnAgentRequest.NotifyOn` | — | **dead enums** — no value referenced anywhere | — |

## Contracts asserted in the proto that the code does not implement

Recorded because the comment is normative and a reader will otherwise trust it.

- `HarnessSpec.extra_args` (`:323-325`) documents "runner-validated against an
  allowlist — the runner has direct CLI control and is the enforcement point". There
  is no allowlist; the field is never read and never populated.
- `HelloAck.committed_seq` (`:483`) is "the authoritative resume cursor";
  `runchannel.go:148` sets `CommittedSeq: hello.GetResumeFromSeq()` — the agent's own
  claim, echoed. `HelloAck.event_window` has zero references, so the file header's
  end-to-end backpressure claim (`:180-181`) is unimplemented.
- `RunnerHello.harnesses` ("runtime MUST NOT StartRun a harness the runner didn't
  advertise") and `RunnerHello.max_concurrent_runs` ("RESOURCE_EXHAUSTED when at
  capacity") are both never read. `RunnerHeartbeat`'s three payload fields and
  `DrainResult` are entirely unreferenced. This family describes multi-runner
  placement; ctxloom runs one runner per run.
- `Hello.protocol_version` is written as `1` (`home.go:286`) and never checked, across
  11 documented revisions.
- Both `reject_reason` fields (`:278`, `:480`), added so a handshake rejection is
  actionable, are discarded by their only clients (`runnerlink.go:137`, `home.go:295`).
- `StopRunResult.exited_within_grace` is hardcoded `true` on **both** return paths
  (`runchannel.go:801,810`), including the immediate-kill path.

## Arguments the LLM is told to use that are discarded

Schema-validated at the MCP edge, then never read. `Get*` call sites verified zero.

| Tool | Argument | Handler |
|---|---|---|
| `agent_run` | `budget`, `constraints`, `notify_on` | `runchannel.go:719-757` reads only `role` and `input.{prompt,workspace,dirty_tree_handler}` |
| `roster` | `task_id`, `include_descendants` | `runchannel.go:772-783` passes only `include_terminal` and `role` |
| `agent_stop` | `grace`, `reason` | `runchannel.go:785-812` reads only `run_id` |
| `agent_send` | `artifact_ids` | `runchannel.go:666-704` never reads it; `PeerMessage.artifacts` is never populated |

`SpawnAgentResult.child_task_id` is likewise returned to the model as a permanently
empty string.

## Dead surface

23 of 82 messages have zero references outside generated code, including the entire
coordinator→agent request direction: `CoordinatorRequest`, `Steer*`, `Question*`,
`Summarize*`, `Pause*`, `Resume*`, `BudgetSpec`, `BudgetUpdate`, `CancelRun`,
`CancelRequest`, `StepStarted`, `StepCompleted`, `StatusChanged`, `UserInput*`,
`RawEvent`, `DrainResult`, `ArtifactRef`. `CoordinatorNotice` has 4 arms of which only
`peer_message` is alive.

## Field-number hygiene

No duplicate field numbers exist (verified across all 159 generated structs). Six
`reserved` declarations exist, all deferral holds rather than deletion tombstones.
Revisions 6 and 7 **renumbered** fields (`RunnerHello`, `ArtifactProduced`) with no
tombstones — safe only because the durable journal is JSONL of hand-written Go structs
(`coord/journal.go:18-22`), not proto bytes. That invariant is not recorded in the
file.

## Vendored `google/rpc/status.proto`

Byte-identical to upstream googleapis. Deliberately vendored and excluded from both
lint (`buf.yaml`) and codegen (`buf.gen.yaml`) with recorded rationale: remote BSR
made `buf generate` fail non-deterministically. Go types still come from
`google.golang.org/genproto`, so there is no duplicate-type hazard. No provenance
commit is pinned.
