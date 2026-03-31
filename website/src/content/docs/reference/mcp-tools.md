---
title: "MCP Tools Reference"
---

# MCP Tools Reference

Complete reference for all MCP (Model Context Protocol) tools exposed by SCM's MCP server.

## Overview

SCM runs as an MCP server, exposing tools that AI assistants can use to manage context, bundles, profiles, and remotes. These tools enable seamless integration with Claude Code, Cursor, and other MCP-compatible clients.

## Fragment Tools

### list_fragments

List available local context fragments with their tags and source locations.

**Parameters:**

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `query` | string | No | Text search on name |
| `tags` | string[] | No | Filter by tags |
| `sort_by` | string | No | Sort field: `name` or `source` (default: `name`) |
| `sort_order` | string | No | `asc` or `desc` (default: `asc`) |

**Example:**
```json
{
  "tool": "list_fragments",
  "arguments": {
    "tags": ["golang", "testing"],
    "sort_by": "name"
  }
}
```

### get_fragment

Get a local fragment's content by name.

**Parameters:**

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `name` | string | Yes | Fragment name (without extension) |

**Example:**
```json
{
  "tool": "get_fragment",
  "arguments": {
    "name": "go-testing"
  }
}
```

### create_fragment

Create a new context fragment.

**Parameters:**

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `name` | string | Yes | Fragment name (without extension) |
| `content` | string | Yes | Fragment content (markdown) |
| `tags` | string[] | No | Tags for the fragment |
| `version` | string | No | Version string (default: `1.0`) |

### delete_fragment

Delete a local context fragment.

**Parameters:**

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `name` | string | Yes | Fragment name to delete |

---

## Profile Tools

### list_profiles

List all configured profiles with their descriptions.

**Parameters:**

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `query` | string | No | Text search on name or description |
| `sort_by` | string | No | Sort field: `name` or `default` (default: `name`) |
| `sort_order` | string | No | `asc` or `desc` (default: `asc`) |

### get_profile

Get a profile's configuration including fragments, tags, and variables.

**Parameters:**

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `name` | string | Yes | Profile name |

### create_profile

Create a new profile with bundles, tags, and/or parent profiles.

**Parameters:**

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `name` | string | Yes | Profile name |
| `description` | string | No | Profile description |
| `bundles` | string[] | No | Bundle references to include |
| `tags` | string[] | No | Tags to include fragments by |
| `parents` | string[] | No | Parent profiles to inherit from |
| `default` | boolean | No | Set as default profile |
| `exclude_fragments` | string[] | No | Fragment names to exclude |
| `exclude_prompts` | string[] | No | Prompt names to exclude |
| `exclude_mcp` | string[] | No | MCP server names to exclude |

**Example:**
```json
{
  "tool": "create_profile",
  "arguments": {
    "name": "my-golang-profile",
    "description": "Go development with testing focus",
    "bundles": ["go-development", "testing"],
    "parents": ["base-developer"],
    "exclude_fragments": ["verbose-logging"],
    "default": true
  }
}
```

### update_profile

Update an existing profile by adding/removing bundles, tags, or parents.

**Parameters:**

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `name` | string | Yes | Profile name to update |
| `description` | string | No | New description |
| `add_bundles` | string[] | No | Bundles to add |
| `remove_bundles` | string[] | No | Bundles to remove |
| `add_tags` | string[] | No | Tags to add |
| `remove_tags` | string[] | No | Tags to remove |
| `add_parents` | string[] | No | Parent profiles to add |
| `remove_parents` | string[] | No | Parent profiles to remove |
| `default` | boolean | No | Set as default profile |
| `add_exclude_fragments` | string[] | No | Add fragments to exclusion list |
| `remove_exclude_fragments` | string[] | No | Remove fragments from exclusion list |
| `add_exclude_prompts` | string[] | No | Add prompts to exclusion list |
| `remove_exclude_prompts` | string[] | No | Remove prompts from exclusion list |
| `add_exclude_mcp` | string[] | No | Add MCP servers to exclusion list |
| `remove_exclude_mcp` | string[] | No | Remove MCP servers from exclusion list |

### delete_profile

Delete a profile.

**Parameters:**

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `name` | string | Yes | Profile name to delete |

---

## Prompt Tools

### list_prompts

List saved prompts.

**Parameters:**

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `query` | string | No | Text search on name |
| `sort_by` | string | No | Sort field: `name` (default: `name`) |
| `sort_order` | string | No | `asc` or `desc` (default: `asc`) |

### get_prompt

Get a saved prompt's content by name.

**Parameters:**

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `name` | string | Yes | Prompt name (without extension) |

---

## Context Assembly

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

---

## Search

### search_content

Search across all SCM content types (fragments, prompts, profiles, MCP servers).

**Parameters:**

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `query` | string | Yes | Search text (matches name, description, tags) |
| `types` | string[] | No | Content types: `fragment`, `prompt`, `profile`, `mcp_server` |
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
    "types": ["fragment", "prompt"],
    "limit": 10
  }
}
```

---

## Remote Tools

### list_remotes

List configured remote sources for fragments and prompts.

**Parameters:** None

### discover_remotes

Search GitHub/GitLab for SCM repositories containing fragments and prompts.

**Parameters:**

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `query` | string | No | Optional search term to filter repositories |
| `source` | string | No | Which forge: `github`, `gitlab`, or `all` (default: `all`) |
| `min_stars` | integer | No | Minimum star count filter (default: 0) |

**Example:**
```json
{
  "tool": "discover_remotes",
  "arguments": {
    "query": "golang",
    "source": "github",
    "min_stars": 10
  }
}
```

### browse_remote

List items (fragments, prompts, profiles) available in a remote repository.

**Parameters:**

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `remote` | string | Yes | Remote name (from list_remotes) |
| `item_type` | string | No | Type: `fragment`, `prompt`, or `profile` (default: all) |
| `path` | string | No | Subdirectory path to browse |

### preview_remote

Preview content of a remote item before pulling. Returns a `pull_token` for `confirm_pull`.

**Parameters:**

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `reference` | string | Yes | Remote reference (e.g., `github/general/tdd` or `github/security@v1.0.0`) |
| `item_type` | string | Yes | Type: `fragment`, `prompt`, or `profile` |

### confirm_pull

Install a previously previewed item using the pull_token from preview_remote.

**Parameters:**

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `pull_token` | string | Yes | Token from preview_remote response |

### add_remote

Register a new remote source for fragments and prompts.

**Parameters:**

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `name` | string | Yes | Short name for the remote (e.g., `alice`) |
| `url` | string | Yes | Repository URL (e.g., `alice/scm` or `https://github.com/alice/scm`) |

### remove_remote

Remove a registered remote source.

**Parameters:**

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `name` | string | Yes | Remote name to remove |

---

## MCP Server Management

### list_mcp_servers

List configured MCP servers.

**Parameters:**

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `query` | string | No | Text search on name or command |
| `sort_by` | string | No | Sort: `name` or `command` (default: `name`) |
| `sort_order` | string | No | `asc` or `desc` (default: `asc`) |

### add_mcp_server

Add an MCP server to the configuration.

**Parameters:**

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `name` | string | Yes | Server name (unique identifier) |
| `command` | string | Yes | Command to run the MCP server |
| `args` | string[] | No | Command arguments |
| `backend` | string | No | Backend: `unified`, `claude-code`, or `gemini` (default: `unified`) |

**Example:**
```json
{
  "tool": "add_mcp_server",
  "arguments": {
    "name": "tree-sitter",
    "command": "npx",
    "args": ["tree-sitter-mcp", "--stdio"],
    "backend": "claude-code"
  }
}
```

### remove_mcp_server

Remove an MCP server from the configuration.

**Parameters:**

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `name` | string | Yes | Server name to remove |
| `backend` | string | No | Backend to remove from: `unified`, `claude-code`, or `gemini` (default: all) |

### set_mcp_auto_register

Enable or disable auto-registration of SCM's own MCP server.

**Parameters:**

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `enabled` | boolean | Yes | Whether to auto-register SCM's MCP server |

---

## Dependency Management

### sync_dependencies

Sync remote bundles and profiles referenced in config. Automatically fetches missing dependencies, updates lockfile, and applies hooks.

**Parameters:**

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `profiles` | string[] | No | Specific profiles to sync (default: all) |
| `force` | boolean | No | Re-pull even if already installed (default: false) |
| `lock` | boolean | No | Update lockfile after sync (default: true) |
| `apply_hooks` | boolean | No | Apply hooks after sync (default: true) |

### lock_dependencies

Generate a lockfile from currently installed remote items for reproducible installations.

**Parameters:** None

### install_dependencies

Install all items from the lockfile.

**Parameters:**

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `force` | boolean | No | Skip confirmation prompts (default: false) |

### check_outdated

Check if any locked items have newer versions available.

**Parameters:** None

### check_missing_dependencies

Check which remote dependencies are not installed locally.

**Parameters:**

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `profiles` | string[] | No | Specific profiles to check (default: all) |

---

## Hooks

### apply_hooks

Apply/reapply SCM hooks to backend configuration files (`.claude/settings.json`, `.gemini/settings.json`).

**Parameters:**

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `backend` | string | No | Backend: `claude-code`, `gemini`, or `all` (default: `all`) |
| `regenerate_context` | boolean | No | Also regenerate the context file (default: true) |

---

## Session Memory Tools

These tools manage session memory for context preservation across conversations. See the [Memory Guide](/guides/memory) for usage details.

### compact_session

Compact current or specified session log into a distilled summary.

**Parameters:**

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `session_id` | string | No | Session ID to compact (defaults to current session) |
| `model` | string | No | LLM model for distillation (default: haiku) |
| `backend` | string | No | Backend to read from: `claude-code` or `gemini` (default: `claude-code`) |

### list_sessions

List all sessions from the backend with their compaction status.

**Parameters:**

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `backend` | string | No | Backend to list from: `claude-code` or `gemini` (default: `claude-code`) |

### load_session

Distill and load context from a specific session.

**Parameters:**

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `session_id` | string | Yes | Session ID to load |
| `backend` | string | No | Backend to read from (default: `claude-code`) |
| `model` | string | No | LLM model for distillation if needed |

### recover_session

Recover context from the current session after `/clear`. Uses stable process ID tracking to find the previous session automatically.

**Parameters:**

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `session_id` | string | No | Session ID to recover (auto-detected if not provided) |
| `backend` | string | No | Backend to read from (default: `claude-code`) |
| `model` | string | No | LLM model for distillation if needed |

### get_previous_session

Get the previous session's distilled content by looking up the session registry. This is the primary tool for recovering context after `/clear`.

**Parameters:**

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `model` | string | No | LLM model for distillation if needed |

### browse_session_history

Browse recent sessions with AI-generated summaries. Shows sessions from the last 3 days with a brief description of each.

**Parameters:**

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `backend` | string | No | Backend to browse: `claude-code` or `gemini` (default: `claude-code`) |

---

## Using MCP Tools

### With Claude Code

Claude Code automatically discovers SCM tools when SCM is configured as an MCP server. You can invoke them naturally:

```
"List all my fragments"
→ Uses list_fragments

"Find SCM repositories about Python"
→ Uses discover_remotes with query "python"

"Create a new profile called web-dev with the security bundle"
→ Uses create_profile
```

### Programmatic Access

Tools can be called directly via the MCP protocol:

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "tools/call",
  "params": {
    "name": "list_fragments",
    "arguments": {
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
    "message": "Invalid params: name is required"
  }
}
```
