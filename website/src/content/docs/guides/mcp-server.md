---
title: "MCP Server"
---

ctxloom can run as an MCP (Model Context Protocol) server, allowing AI assistants to access your context directly.

## Running the MCP Server

```bash
ctxloom mcp serve
```

This starts ctxloom as an MCP server over stdio.

## Claude Code Configuration

Add ctxloom to `~/.claude/settings.json`:

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
ctxloom mcp auto-register --disable
ctxloom mcp auto-register --enable
```

Or in config:

```yaml
mcp:
  auto_register_ctxloom: false
```

## Available MCP Tools

### Context Tools

| Tool | Description |
|------|-------------|
| `list_fragments` | List fragments with optional filtering |
| `get_fragment` | Get specific fragment content |
| `list_profiles` | List all profiles |
| `get_profile` | Get profile configuration |
| `assemble_context` | Combine fragments, profiles, tags |
| `list_prompts` | List all prompts |
| `get_prompt` | Get prompt content |
| `search_content` | Search across all content types |

### Management Tools

| Tool | Description |
|------|-------------|
| `create_profile` | Create new profile |
| `update_profile` | Modify existing profile |
| `delete_profile` | Remove profile |
| `create_fragment` | Create new fragment |
| `delete_fragment` | Remove fragment |
| `apply_hooks` | Apply hooks to backend configs |

### Remote Tools

| Tool | Description |
|------|-------------|
| `list_remotes` | List configured remotes |
| `add_remote` | Register new remote |
| `remove_remote` | Unregister remote |
| `discover_remotes` | Search GitHub/GitLab for ctxloom repos |
| `browse_remote` | List items in remote |
| `preview_remote` | Preview content before pulling |
| `confirm_pull` | Install previewed item |

### MCP Server Tools

| Tool | Description |
|------|-------------|
| `list_mcp_servers` | List configured servers |
| `add_mcp_server` | Add server config |
| `remove_mcp_server` | Remove server |
| `set_mcp_auto_register` | Toggle auto-registration |

### Sync Tools

| Tool | Description |
|------|-------------|
| `sync_dependencies` | Sync remote dependencies |
| `lock_dependencies` | Generate lockfile |
| `install_dependencies` | Install from lockfile |
| `check_outdated` | Check for updates |
| `check_missing_dependencies` | Find missing deps |

## Tool Schemas

### assemble_context

```json
{
  "profile": "string",
  "bundles": ["string"],
  "tags": ["string"]
}
```

### list_fragments

```json
{
  "query": "string",
  "tags": ["string"],
  "sort_by": "name|source",
  "sort_order": "asc|desc"
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

### create_profile

```json
{
  "name": "string (required)",
  "description": "string",
  "parents": ["string"],
  "bundles": ["string"],
  "tags": ["string"],
  "default": "boolean"
}
```

### update_profile

```json
{
  "name": "string (required)",
  "description": "string",
  "add_parents": ["string"],
  "remove_parents": ["string"],
  "add_bundles": ["string"],
  "remove_bundles": ["string"],
  "add_tags": ["string"],
  "remove_tags": ["string"],
  "default": "boolean"
}
```

### add_mcp_server

```json
{
  "name": "string (required)",
  "command": "string (required)",
  "args": ["string"],
  "backend": "unified|claude-code|gemini"
}
```

### sync_dependencies

```json
{
  "profiles": ["string"],
  "force": "boolean",
  "lock": "boolean",
  "apply_hooks": "boolean"
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

> list available remotes

● ctxloom - list_remotes (MCP)()
  ⎿ { "remotes": [{"name": "ctxloom-default", ...}] }
```

## Managing MCP Servers

ctxloom can manage MCP server configurations:

```bash
ctxloom mcp list
ctxloom mcp add tree-sitter -c "npx" -a "tree-sitter-mcp"
ctxloom mcp add my-server -c "/path/to/server" -b claude-code
ctxloom mcp remove tree-sitter
ctxloom mcp show tree-sitter
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

These MCP servers are registered when the bundle is used.

## Security Considerations

:::warning
MCP servers can execute arbitrary commands with user permissions. Only install servers from trusted sources.
:::

When pulling from remotes:
- **MCP Servers**: Can execute arbitrary commands
- **Context Items**: Risk of prompt injection
- **Bundles**: Combine both risks

Always review content before installing with `ctxloom fragment install`.
