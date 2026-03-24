---
sidebar_position: 2
---

# MCP Server

SCM can run as an MCP (Model Context Protocol) server, allowing AI assistants to access your context directly.

## Running the MCP Server

```bash
scm mcp
```

This starts SCM as an MCP server over stdio.

## Claude Code Configuration

Add SCM to `~/.claude/settings.json`:

```json
{
  "mcpServers": {
    "scm": {
      "command": "/path/to/scm",
      "args": ["mcp"]
    }
  }
}
```

Replace `/path/to/scm` with your actual binary location (e.g., `~/go/bin/scm`).

## Available MCP Tools

| Tool | Description |
|------|-------------|
| `list_fragments` | List all fragments, optionally filtered by tags |
| `get_fragment` | Retrieve a specific fragment's content |
| `list_profiles` | List all configured profiles |
| `get_profile` | Get detailed profile configuration |
| `assemble_context` | Combine fragments, profiles, and tags |
| `list_prompts` | List all saved prompts |
| `get_prompt` | Retrieve a specific prompt's content |

## MCP Usage Examples

Within an AI assistant conversation:

```
> assemble context with the scm-main/python-developer profile

● scm - assemble_context (MCP)(profile: "scm-main/python-developer")
  ⎿ { "context": "# Python Development\n..." }

> assemble context with python and security tags

● scm - assemble_context (MCP)(tags: ["python", "security"])
  ⎿ { "context": "# Python Development\n# Security..." }
```

## Managing MCP Servers

SCM can also manage MCP server configurations for your AI tools:

```bash
scm mcp-servers list                # List configured MCP servers
scm mcp-servers add <name>          # Add MCP server config
scm mcp-servers remove <name>       # Remove MCP server config
```

## Bundle MCP Definitions

Bundles can include MCP server definitions:

```yaml
mcp:
  tree-sitter:
    command: "tree-sitter-mcp"
    args: ["--stdio"]

  database:
    command: "postgres-mcp"
    args: ["--connection", "localhost:5432"]
```

These MCP servers are registered when the bundle is used.
