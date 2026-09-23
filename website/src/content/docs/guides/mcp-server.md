---
title: "MCP Server"
---

ctxloom exposes MCP (Model Context Protocol) tools to the agent it launches, so the agent can retrieve context during a session.

## How a Session Reaches It

The server is served by the running session. When `ctxloom run` starts a session, the session's runner serves an MCP endpoint and writes its URL and a bearer token into the session's own MCP registry, in the session home (Claude Code receives it on `--mcp-config`). The engine dials that endpoint directly.

There is no `ctxloom` command that speaks the protocol, and nothing is registered in your project at rest: ctxloom injects its MCP only while a session is running. A Claude Code you launch directly, outside `ctxloom run`, does not get ctxloom's tools. `ctxloom manage hooks install` registers the MCP servers your bundles declare, but not ctxloom's own.

If an old project `.mcp.json` still has a `ctxloom` entry that launches the binary, the engine starts and waits forever for a handshake that never arrives. `ctxloom doctor` flags this (`DOCTOR-CHECK-MCP-INVOCATION-g7`), and `ctxloom manage hooks install` rewrites the registry without it.

### Where the registration comes from

ctxloom's own server is declared by ctxloom's own companion loadout, which every
session composes, so it is registered by default and there is no flag to turn
on. To withhold it, exclude it from the profiles a session composes:

```yaml
# .ctxloom/profiles/<name>.yaml
exclude_mcp:
  - ctxloom
```

Withholding it costs the session every ctxloom tool, including the
`agent_send`/`agent_recv`/`agent_report` bus a delegated child reports back on.

## What the Server Exposes

The MCP surface is deliberately small: it retrieves context, works with session memory, reports the session's context-window occupancy, and delegates to other ctxloom agents. Everything that *manages* ctxloom (bundles, profiles, remotes, review/approval, trust, hooks) is CLI-only; an agent runs those commands through its shell. Task tracking is served by the separate `taskloom mcp` server.

Read-only catalog listings, such as fragments, commands and skills, are MCP resources (`ctxloom://...`) rather than tools; `ctxloom://help` describes every resource URI to the connected agent.

The [MCP Tools Reference](/reference/mcp-tools/) is generated from the registered tools and resources, and lists each one with its parameter schema. `ctxloom mcp --help` gives the same tools grouped by purpose. For the delegation tools, see [Agent Delegation](/concepts/agent-delegation/).

## MCP Usage Examples

Within an AI assistant conversation:

```
> assemble context with the developer profile

● ctxloom - assemble_context (MCP)(profile: "developer")
  ⎿ { "context": "# Development Standards\n..." }

> search for python content

● ctxloom - search_content (MCP)(query: "python", types: ["fragment"])
  ⎿ { "results": [...], "count": 5 }

> what bundles could I install for Go?

● ctxloom - search_library (MCP)(query: "tag:golang")
  ⎿ { "results": [{"name": "go-development", "pull_ref": "...", ...}] }
```

Management requests route through the CLI instead — e.g. "pull the remotes" runs `ctxloom deps pull` in the shell.

## Managing MCP Servers

Every MCP server lives in a bundle, so the CLI here READS the roster and edits
the bundle a server came from. Adding a server means composing a bundle that
declares it; removing one means not composing that bundle, or excluding the
server by name from the profiles a session composes (`exclude_mcp`).

```bash
ctxloom mcp server list                        # what this project registers, and from which bundle
ctxloom mcp server show tree-sitter
ctxloom mcp server edit tools#mcp/tree-sitter  # edit it where it lives
```

## Bundle MCP Definitions

Bundles can include MCP server definitions:

```yaml
mcp:
  tree-sitter:
    command: "tree-sitter-mcp"
    args: ["--stdio"]
    notes: "AST parsing for code"
    installation: "npm install -g tree-sitter-mcp"

  database:
    command: "postgres-mcp"
    args: ["--connection", "localhost:5432"]
    env:
      PGPASSWORD: "${PGPASSWORD}"
```

These MCP servers are registered when the bundle is used, subject to [review and trust](/concepts/review-and-trust/).

## Security Considerations

:::warning
MCP servers can execute arbitrary commands with user permissions. Only install servers from trusted sources.
:::

When pulling from remotes:
- **MCP Servers**: Can execute arbitrary commands
- **Context Items**: Risk of prompt injection
- **Bundles**: Combine both risks

Always review content before referencing it in a profile and running `ctxloom deps pull`. Trust-gating withholds unreviewed MCP servers from the agent until you accept them with `ctxloom review` (or `ctxloom bundle trust <ref>` for a single item) — or trust the publisher's key with `ctxloom signer trust <principal> --key <path> --namespace publish` so their future content skips review. A remote itself carries no trust; trust follows a signing key, not a fetch address. Those keys are SSH keys and the signatures are SSH signatures (sshsig, verified against `allowed_signers`); ctxloom does not use GPG/PGP.
