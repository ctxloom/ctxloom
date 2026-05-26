# MCP Go SDK Migration — Living Plan

> **Status:** core migration complete on `feat/bundle-mcp-tools`. All 37 tools running through the official SDK, `just test` green. Open items below.
> **Living document** — update with decisions, scope changes, and progress as work continues.

## Why

The hand-rolled JSON-RPC implementation in `cmd/mcp.go` had three correctness/maintainability problems surfaced by an `any`-usage audit:

1. **Silent unmarshal errors in 8 handlers** — `_ = json.Unmarshal(args, &params)` ignored malformed input and proceeded with zero-value params. Real correctness bug.
2. **`map[string]any` output repackaging in 21 of 31 handlers** — strips type info from operations Result structs for no JSON-shape benefit. ~80 lines of noise.
3. **`map[string]any` JSON-Schema literals (~200 sites)** — the input schema and the inline param struct in each handler could drift silently. Typo `"properites"` once and the tool's contract degrades to "accepts anything."

The bigger structural issue: ~2400 lines maintaining envelope types, dispatch tables, and schema literals that an SDK could provide.

## Decisions

| Decision | Choice | Reasoning |
|---|---|---|
| SDK selection | `modelcontextprotocol/go-sdk@v1.6.1` (official) over community `mark3labs/mcp-go` | Official is v1.x (API-stability commitment); community is still v0.x. Anthropic+Google maintained. Generic-typed handler signature is cleaner. Community has more features (SSE, OAuth, async tools) but none we need today for a stdio server. |
| Migration scope | Big-bang on `feat/bundle-mcp-tools` (not a fresh branch, not phased) | User directive after reviewing options. Acceptable because the changes are mostly mechanical handler-shape rewrites plus a clean cut at the cobra boundary. |
| Go version | Bumped go.mod from 1.24.0 → 1.25.0; devcontainer from 1.24.4 → 1.25.10 | SDK requires 1.25+. |
| Dockerfile arch | Parameterized via `ARG TARGETARCH` (auto-populated by BuildKit) | Was hardcoded `amd64` for Go and UPX installs, broken on arm64. ONNX block also switched from `dpkg --print-architecture` to `${TARGETARCH}` for consistency. |
| `any` policy | Use where real polymorphism exists. NOT a cosmetic blanket replacement for `interface{}` | `any` is literally `interface{}` since Go 1.18. The mass-rename was cosmetic; the real wins are deleting wrappers (`parseYAML`) and typing concrete unmarshal targets (`config.Profile`). |
| Error handling | `signal.NotifyContext` + treat `context.Canceled` as clean shutdown | SDK's `Server.Run` handles stdin EOF and ctx-cancellation natively; manual `sigCh` + stdin-close goroutine removed. |

## Phase 1 — Foundation (done)

- Added `github.com/modelcontextprotocol/go-sdk@v1.6.1` dependency.
- Bumped Go version (go.mod + Dockerfile).
- Fixed Dockerfile multi-arch (TARGETARCH).
- Scaffolded `cmd/mcp_server.go` with `ctxServer` struct + startup orchestration (config load, sync, hooks) and `runMCPServerSDK` cobra RunE.

## Phase 2 — Tool migration (done)

37 tools ported into 8 new files. Each tool gets a typed Input struct (with `json` + `jsonschema:"<description>"` tags) and a named handler method on `*ctxServer`. Return types are the operations layer's existing `*XxxResult` structs (no more `map[string]any` repackaging).

| File | Tools |
|---|---|
| `mcp_tools_fragments.go` | list, get, create, delete |
| `mcp_tools_profiles.go` | list, get, create, update, delete |
| `mcp_tools_prompts.go` | list, get |
| `mcp_tools_context.go` | assemble_context, search_content |
| `mcp_tools_remotes.go` | list, search, discover, browse, pull + add, remove, update |
| `mcp_tools_bundles.go` | create, update, delete, push + bundleDistiller infra |
| `mcp_tools_hooks_sync.go` | apply_hooks, sync_dependencies |
| `mcp_tools_mcpservers.go` | list, add, remove, set_auto_register |
| `mcp_tools_memory.go` | compact, list, load, recover, browse_history, get_previous |

## Phase 3 — Cutover (done)

- Updated `mcpCmd.RunE` and `mcpServeCmd.RunE` to point at `runMCPServerSDK`.
- Truncated `cmd/mcp.go` from 2468 → 307 lines (cobra subcommands only).
- Deleted `cmd/mcp_memory.go` (handlers rewritten in `mcp_tools_memory.go`).
- Removed the manual signal-handling goroutine — SDK handles it.

## Phase 4 — Test coverage (done)

- Deleted `cmd/mcp_bundle_test.go` (tested the deleted dispatch layer).
- Updated `cmd/mcp_bundle_integration_test.go` to assert the SDK's exact schema-validation error wording.
- Refactored bundle handlers from anonymous closures into named methods (`handleCreateBundle`, `handleUpdateBundle`, `handleDeleteBundle`, `handlePushBundle`) so they're directly callable from tests.
- Wrote `cmd/mcp_tools_bundles_test.go` with 7 tests:
  - 4 handler-direct tests (create writes file, update no_changes/updated, push dry-run, delete removes file).
  - 3 SDK-via-in-memory-transport tests (registration, required-fields schema derivation, schema rejection of missing required props).

## Phase 5 — Targeted cleanup (done)

- Deleted `parseYAML` wrapper in `cmd/search.go` (one-line indirection around `yaml.Unmarshal`).
- Typed `profileData` in `cmd/profile.go` as `config.Profile` instead of `map[string]any` — now validates profile shape during import, not just YAML parseability.

## Net diff

| Metric | Value |
|---|---|
| Insertions | ~1100 |
| Deletions | ~3300 |
| Net | **~2200 lines smaller** |
| Files added | 9 (mcp_server.go + 8 mcp_tools_*.go + 1 test) |
| Files deleted | 2 (mcp_memory.go, mcp_bundle_test.go) |

## Caveats / behavior changes worth knowing

- **Wire error message text changed.** SDK rejects missing required fields with `validating "arguments": validating root: required: missing properties: ["name"]`. Anything that greps tool-error text for legacy phrasing needs updating. The bundle integration test asserts the exact new string.
- **Schema enum constraints dropped from machine-readable schema, kept in description text.** `google/jsonschema-go` tags only support description text, not enum constraints. We carry constraints in the description (e.g. `jsonschema:"Sort field (one of: name, default; default: name)"`) — LLM clients still see them, but they're no longer validated by the SDK. Operations layer accepts any string and falls back to defaults, so this is a no-op for behavior.
- **No more parent-PID death poll.** Legacy had a 2s ticker checking `os.Getppid()`. SDK relies on stdin EOF instead. If parent dies without closing stdin (rare), the server may hang until OS reaps it. Re-add only if this becomes a real problem.

## Open items

- [ ] Decide whether the parent-PID death poll needs to come back. (Not blocking; defer until observed.)
- [ ] Audit remaining `map[string]any` sites elsewhere in the codebase for ones that could become typed structs. (Lower priority — most are in JSON-RPC test envelopes where raw maps are correct.)
- [ ] Consider whether `bundleDistiller` should move out of `cmd/` into `internal/operations/` so it's reusable from the CLI bundle subcommand too. (Refactor, not migration-blocking.)
- [ ] Update `docs/bundle-review-plan.md` Phase 1.3 cross-reference if any bundle-extraction work shifts because of this migration. (Tracking note: the SDK migration doesn't touch the BundleReader design but the bundle handlers' file layout changed.)

## Out of scope (won't do as part of this migration)

- Task-augmented tools (async/long-running handler support). Community SDK has this; official doesn't yet. Revisit if `pull_remote`/`sync_dependencies`/`discover_remotes` blocking becomes a UX problem.
- Roots, sampling, elicitation. Not currently used; can adopt when needed.
- Reorganizing the `cmd/mcp.go` cobra subcommands. They're independent of the protocol layer.

## How to extend

To add a new tool:

1. Pick the right `cmd/mcp_tools_<category>.go` file (or create a new one + a new `registerXxxTools` method called from `mcp_server.go`).
2. Define a typed Input struct. Fields without `omitempty` become required; with become optional. Use `jsonschema:"<description>"` for the LLM-facing description.
3. Write a named handler method on `*ctxServer` with signature
   `func(ctx context.Context, _ *mcp.CallToolRequest, in XxxInput) (*mcp.CallToolResult, *operations.XxxResult, error)`.
4. Call `mcp.AddTool(server, &mcp.Tool{Name, Description}, s.handleXxx)` in the register function.
5. If the tool has non-trivial behavior worth testing at the cmd layer (not just the operations layer), add tests to the corresponding `_test.go` file — direct method calls for business logic, in-memory SDK transport for schema/registration assertions.
