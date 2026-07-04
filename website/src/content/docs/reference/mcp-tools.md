---
title: "MCP Tools Reference"
---

Reference for the tools and resources exposed by ctxloom's MCP server (`ctxloom mcp`).

## Overview

The MCP surface is for **retrieving context during a session**: assembling context, searching content, and working with session memory. It exposes seven tools and a set of read-only resources.

Everything that *manages* ctxloom — creating or editing bundles, profiles, fragments, and skills; pulling remotes; reviewing, approving, and trusting changes — is done with the ctxloom CLI, not MCP tools. Task tracking lives in the separate `taskloom` binary; its MCP server (`taskloom mcp`) serves the `task_*` tools.

## Context Tools

### assemble_context

Assemble context from a profile, fragments, and/or tags. Returns the combined context that would be sent to an AI.

**Parameters:**

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `profile` | string | No | Profile name to use |
| `bundles` | string[] | No | Additional fragment names to include |
| `tags` | string[] | No | Include all fragments with these tags |

**Example:**
```json
{
  "tool": "assemble_context",
  "arguments": {
    "profile": "developer",
    "bundles": ["security"],
    "tags": ["best-practices"]
  }
}
```

### search_content

Search content already installed in this project (fragments, skills, profiles, MCP servers). Does not reach remotes — use `search_library` for discovery.

**Parameters:**

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `query` | string | Yes | Search text (matches name, description, tags) |
| `types` | string[] | No | Content types: `fragment`, `prompt`, `profile`, `mcp_server` (default: all) |
| `tags` | string[] | No | Filter by tags (fragments only) |
| `sort_by` | string | No | Sort: `name`, `type`, or `relevance` (default: `relevance`) |
| `sort_order` | string | No | `asc` or `desc` (default: `asc`) |
| `limit` | integer | No | Maximum results (default: 50) |

**Example:**
```json
{
  "tool": "search_content",
  "arguments": {
    "query": "testing",
    "types": ["fragment"],
    "limit": 10
  }
}
```

### search_library

Search the library of installable bundles across configured remotes, reading their local git clones (no network). This is the discovery counterpart to `search_content`. Each match includes a `pull_ref` for referencing the bundle from a local profile; install it with the CLI (`ctxloom profile create <name> -b <pull_ref>`, then `ctxloom remote pull`).

**Parameters:**

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `query` | string | Yes | Search text. Plain words match name/description/tags; `tag:NAME` matches a tag (e.g. `tag:golang`) |
| `item_type` | string | No | Item type to search (currently only `bundle`; profiles ship inside bundles) |

**Example:**
```json
{
  "tool": "search_library",
  "arguments": {
    "query": "tag:golang"
  }
}
```

## Session Memory Tools

These tools manage session memory for context preservation across conversations. See the [Session Memory Guide](/getting-started/memory) for usage details.

### compact_session

Compact the current or a specified session log into a distilled summary. Use when context is running low.

**Parameters:**

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `session_id` | string | No | Session ID to compact (defaults to current session) |
| `model` | string | No | LLM model for distillation (default: from config, else `claude-3-haiku`) |
| `backend` | string | No | Backend to read from (default: the configured default LLM) |

### load_session

Distill and load context from a session. Accepts either a backend-native `session_id` (UUID) or a `harp_name`; one of the two is required, and `harp_name` wins if both are passed. Harp names are listed in the `ctxloom://sessions/recent` resource.

**Parameters:**

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `session_id` | string | One of these | Backend-native session ID (UUID) |
| `harp_name` | string | One of these | Harp-named session reference (e.g. `swift-amber-falcon`) |
| `backend` | string | No | Backend to read from (default: the configured default LLM) |
| `model` | string | No | LLM model for distillation if needed |

### recover_session

Recover the most-recent session's context after `/clear`. Resolves the most recent session transcript for this working directory at request time (no process/PID tracking).

**Parameters:**

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `session_id` | string | No | Session ID to recover (most recent if not provided) |
| `backend` | string | No | Backend to read from (default: the configured default LLM) |
| `model` | string | No | LLM model for distillation if needed |

### get_previous_session

Get the previous session's distilled content for the current project, read from disk at request time. This is the primary tool for recovering context after `/clear`.

**Parameters:**

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `model` | string | No | LLM model for distillation if needed |

## Resources

Read-only listings are exposed as MCP resources rather than tools:

| URI | Contents |
|-----|----------|
| `ctxloom://help` | Orientation: what the server exposes and where management lives |
| `ctxloom://fragments` | All installed fragments |
| `ctxloom://fragments/{name}` | One fragment's content |
| `ctxloom://profiles` | All profiles |
| `ctxloom://profiles/{name}` | One profile's configuration |
| `ctxloom://skills` | All installed skills |
| `ctxloom://skills/{name}` | One skill's content |
| `ctxloom://remotes` | Configured remotes |
| `ctxloom://remotes/{name}/contents` | Bundles available in a remote |
| `ctxloom://mcp-servers` | Configured MCP servers |
| `ctxloom://sessions` | All harp-named sessions across every project |
| `ctxloom://sessions/recent` | Harp-named sessions for the current project |

## Using MCP Tools

### With Claude Code

Claude Code discovers ctxloom's tools automatically when ctxloom is configured as an MCP server. You can invoke them naturally:

```
"Load the context for the developer profile"
→ Uses assemble_context

"Find installable bundles about Go"
→ Uses search_library with query "tag:golang"

"Recover what we were working on before /clear"
→ Uses get_previous_session
```

Management requests ("create a profile", "pull the remotes", "approve the pending bundle") run through the CLI in your shell instead.

### Programmatic Access

Tools can be called directly via the MCP protocol:

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "tools/call",
  "params": {
    "name": "search_content",
    "arguments": {
      "query": "testing",
      "tags": ["golang"]
    }
  }
}
```

### Error Handling

All tools return errors in the standard MCP format:

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "error": {
    "code": -32602,
    "message": "Invalid params: query is required"
  }
}
```
