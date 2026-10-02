# `ctxloom mcp` and the MCP surface

ctxloom has ONE MCP surface: the session's endpoint, which the session's
runner serves (`internal/adapters/runner/interaction` — `Endpoint`, the
`delivery.Dynamic` port; `NewServer`, the surface). No ctxloom command speaks
the protocol, and nothing is registered in a project at rest.

## How the endpoint reaches an engine

ctxloom is its own companion, and its loadout (`cmd/ctxloom/loadout.yaml`)
declares its server as **served by the running session's endpoint**:

```yaml
mcp:
  ctxloom:
    served_by: session-endpoint
```

`wire.MCPServer.ServedBy` (`wire.ServedBySessionEndpoint`) is the third
exclusive target beside `command` and `url` (`wire.MCPServer.Validate`); on
`bundles.BundleMCP` it is host-evaluated routing (`surface:"selection"`),
outside the executable preimage, because the entry's executable surface is
the empty target set — no bundle contributes anything that runs.

`delivery.InputsFor(lo, root.Dynamic)` is the one mechanism that renders the
declaration: inside a session the engine's provided dynamic approach
(`engine.Definition.Dynamic`, `DynamicApproach.Endpoint`) turns the endpoint
the loadout carries (`Loadout.MCP`, bound by the runner) into the entry the
session's registry names — URL + bearer (`engine.BearerEntry` is the one
projection). At rest — `manage hooks install`, a profile materialize,
doctor — there is no session, and the entry renders nothing. An engine with
no dynamic approach receives nothing for it. A writer handed the declaration
unrendered refuses it (`wire.ErrMCPServerUnrendered`).

`ctxloom doctor` reports any materialized project registry that still
LAUNCHES ctxloom as a stdio server as stale (`DOCTOR-CHECK-MCP-INVOCATION-g7`),
because an engine launching such an entry comes up with none of ctxloom's
tools and nothing says why.

## The `ctxloom mcp` noun

`internal/adapters/cli/mcp.go`. The noun is the configured-server listing:
`mcp` (bare, a person at a terminal), `mcp server list|show|edit`. Every
server is a bundle item (`<bundle>#mcp/<name>`), gated at the bundle exec
choke; the listing DESCRIBES each entry as declared — a command, a URL, or
"served by the running session's endpoint" (`printMCPServerTarget`). Off a
terminal the bare noun refuses (`errMCPBareIsNotTheServer`): a listing
written into a pipe that expects JSON-RPC is indistinguishable from a hang,
and the refusal says what IS the server.

## What `internal/adapters/mcp` still is

The coordinator's side of the session host: `HostCoordinatorForSession`
(`coord_host.go`) stands the coordinator up for `ctxloom run` — the one
hosting path, its constructor private — and hands back the owner's
credential; `HostApp` is the host relay the coordinator answers `HostRequest`
frames through (the session tools: `compact_session`, `load_session`,
`recover_session`, `get_previous_session`, `list_sessions`,
`evaluate_triggers`, `context_status`), one `ctxServer` per relayed call
under the CALLER's identity (`ctxServer.projectDir` is the caller's project;
there is no cwd to fall back on in the coordinator's process). The runner's
endpoint advertises those relays with the contract `operations` declares
(`operations.CompactSessionDesc` and its siblings), so the two sides cannot
describe a tool two ways.

See `docs/architecture/agentcoord/overview.md` for the process topology and
the wire.
