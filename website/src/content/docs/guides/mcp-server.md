---
title: "MCP Server"
---

ctxloom can run as an MCP (Model Context Protocol) server, allowing AI assistants to retrieve context during a session.

## Running the MCP Server

```bash
ctxloom mcp serve
```

This starts ctxloom as an MCP server over stdio.

## Claude Code Configuration

Claude Code doesn't read `mcpServers` from `~/.claude/settings.json` — it reads `.mcp.json` (project scope) or `~/.claude.json` (user scope). The easiest path is to let ctxloom write that entry for you:

```bash
ctxloom mcp register
```

If you need to add it by hand, put the block in `.mcp.json` (project) or `~/.claude.json` (user), not `settings.json`:

```json
{
  "mcpServers": {
    "ctxloom": {
      "command": "/path/to/ctxloom",
      "args": ["mcp", "serve"]
    }
  }
}
```

Replace `/path/to/ctxloom` with your actual binary location (e.g., `~/go/bin/ctxloom`).

### Auto-Registration

By default, ctxloom auto-registers itself as an MCP server. Control this with:

```bash
ctxloom mcp unregister
ctxloom mcp register
```

Or in config:

```yaml
mcp:
  auto_register_ctxloom: false
```

## What the Server Exposes

The MCP surface is deliberately small: it retrieves context, works with session memory, and delegates to other ctxloom agents. Everything that *manages* ctxloom — bundles, profiles, remotes, review/approval, trust, hooks — is CLI-only; an agent runs those commands through its shell. Task tracking is served by the separate `taskloom mcp` server.

### Tools

| Tool | Description |
|------|-------------|
| `assemble_context` | Combine a profile, fragments, and/or tags into context |
| `search_content` | Search installed content (fragments, commands, profiles, MCP servers) |
| `search_library` | Search installable bundles across configured remotes (discovery) |
| `compact_session` | Compact a session log into a distilled summary |
| `load_session` | Distill and load a session by ID or harp name |
| `recover_session` | Recover the most-recent session's context after `/clear` |
| `get_previous_session` | Get the previous session's distilled content |
| `agent_run` | Launch a configured ctxloom agent as a delegated child session |
| `agent_send` | Send a message to another agent session (coordinator → child by harp, or child → "parent") |
| `agent_recv` | Receive pending mailbox messages for this session, waiting up to a bounded timeout |
| `agent_stop` | Stop a delegated child session; it stays resumable via a later `agent_send` |

:::note
This is the standalone `ctxloom mcp serve` surface — the one this page documents. A normal
`ctxloom run` / `ctxloom acp` session gets a **richer, runner-terminated** delegation surface
instead (different `agent_run`/`agent_send`/`agent_recv`/`agent_stop` schemas, plus
`agent_report`, `agent_fetch_artifact`, and `roster`), reached automatically — you never
register `ctxloom mcp serve` yourself for a normal session. See [Agent
Delegation](/concepts/agent-delegation/) for that surface, and the [MCP Tools
Reference](/reference/mcp-tools/) for the schema difference in full.
:::

### Resources

Read-only listings are exposed as MCP resources rather than tools:

| URI | Contents |
|-----|----------|
| `ctxloom://help` | Orientation for the connected agent |
| `ctxloom://fragments`, `ctxloom://fragments/{name}` | Fragment listing / content |
| `ctxloom://commands`, `ctxloom://commands/{name}` | Command listing / content |
| `ctxloom://profiles`, `ctxloom://profiles/{name}` | Profile listing / configuration |
| `ctxloom://remotes`, `ctxloom://remotes/{name}/contents` | Remotes / a remote's bundles |
| `ctxloom://mcp-servers` | Configured MCP servers |
| `ctxloom://sessions`, `ctxloom://sessions/recent` | Session listings |

See the [MCP Tools Reference](/reference/mcp-tools/) for parameter schemas.

## Tool Schemas (summary)

### assemble_context

```json
{
  "profile": "string",
  "bundles": ["string"],
  "tags": ["string"]
}
```

### search_content

```json
{
  "query": "string (required)",
  "types": ["fragment", "prompt", "profile", "mcp_server"],
  "tags": ["string"],
  "sort_by": "name|type|relevance",
  "sort_order": "asc|desc",
  "limit": "integer"
}
```

### search_library

```json
{
  "query": "string (required; plain words or tag:NAME)",
  "item_type": "bundle"
}
```

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

Management requests route through the CLI instead — e.g. "pull the remotes" runs `ctxloom remote pull` in the shell.

## Managing MCP Servers

ctxloom manages MCP server configurations with the CLI:

```bash
ctxloom mcp server list
ctxloom mcp server create tree-sitter -c "npx" -a "tree-sitter-mcp"
ctxloom mcp server create my-server -c "/path/to/server" -b claude-code
ctxloom mcp server remove tree-sitter --yes
ctxloom mcp server show tree-sitter
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

Always review content before referencing it in a profile and running `ctxloom remote pull`. Trust-gating withholds unreviewed MCP servers from the agent until you accept them with `ctxloom review` (or `ctxloom bundle trust <ref>` for a single item) — or trust the publisher's key with `ctxloom signer trust <principal> --key <path> --namespace publish` so their future content skips review. A remote itself carries no trust; trust follows a signing key, not a fetch address.
