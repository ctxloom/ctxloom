---
title: "CLI Reference"
---

# CLI Reference

Complete reference for all ctxloom commands.

## ctxloom init

Initialize a new .ctxloom directory.

```bash
ctxloom init              # Create .ctxloom in current directory
ctxloom init --home       # Create/ensure ~/.ctxloom exists
```

## ctxloom run

Assemble context and run AI plugin.

```bash
ctxloom run [flags] [prompt...]
```

### Flags

| Flag | Description |
|------|-------------|
| `-p, --profile <name>` | Load predefined fragment set |
| `-f, --fragments <names>` | Additional fragments to include (repeatable) |
| `-t, --tags <tags>` | Include fragments with specific tags (repeatable) |
| `--plugin <name>` | LLM plugin to use (default: claude-code) |
| `--saved-prompt <name>` | Use saved prompt instead of inline |
| `--dry-run` | Show what would be assembled without running |
| `--suppress-warnings` | Suppress warnings |
| `--print` | Print assembled context to stdout |
| `-v, -vv, -vvv` | Verbosity levels |

### Examples

```bash
ctxloom run -p developer "implement error handling"
ctxloom run -f python-tools#fragments/typing "add type hints"
ctxloom run -f security#fragments/owasp -f python#fragments/errors "audit"
ctxloom run -t security "check for vulnerabilities"
ctxloom run --plugin gemini "use Gemini"
ctxloom run --dry-run  # Preview only
```

## ctxloom fragment

Manage fragments.

| Command | Flags | Arguments | Description |
|---------|-------|-----------|-------------|
| `list` | `--bundle` | | List all fragments, optionally filtered by bundle |
| `show` | `--distilled` | `bundle#fragments/name` | Show fragment content |
| `create` | | `<bundle> <name>` | Create new fragment with placeholder |
| `delete` | | `bundle#fragments/name` | Delete fragment from bundle |
| `edit` | | `bundle#fragments/name` | Edit fragment in configured editor |
| `distill` | `--force` | `bundle#fragments/name` | Create token-efficient version |
| `install` | `--force`, `--blind` | `<reference>` | Install bundle from remote |

### Examples

```bash
ctxloom fragment list
ctxloom fragment list --bundle python-tools
ctxloom fragment show python-tools#fragments/typing
ctxloom fragment show --distilled python-tools#fragments/typing
ctxloom fragment create my-bundle coding-standards
ctxloom fragment edit my-bundle#fragments/coding-standards
ctxloom fragment distill my-bundle#fragments/coding-standards
ctxloom fragment install ctxloom-default/testing
ctxloom fragment install --blind ctxloom-default/security  # Skip preview
```

## ctxloom prompt

Manage prompts.

| Command | Arguments | Description |
|---------|-----------|-------------|
| `list` | | List all prompts |
| `show` | `bundle#prompts/name` | Show prompt content |
| `create` | `<bundle> <name>` | Create new prompt |
| `delete` | `bundle#prompts/name` | Delete prompt |
| `edit` | `bundle#prompts/name` | Edit prompt in editor |
| `install` | `<reference>` | Install from remote |

## ctxloom profile

Manage profiles.

| Command | Flags | Arguments | Description |
|---------|-------|-----------|-------------|
| `list` | | | List all profiles |
| `show` | | `<name>` | Show profile details and exclusions |
| `create` | `--parent`, `-b`, `-d` | `<name>` | Create new profile |
| `modify` | See below | `<name>` | Modify profile configuration |
| `delete` | | `<name>` | Delete profile |
| `edit` | | `<name>` | Edit profile in editor |
| `install` | | `<reference>` | Install from remote |

### Create Flags

| Flag | Description |
|------|-------------|
| `--parent` | Parent profiles to inherit from (repeatable) |
| `-b, --bundle` | Bundle references to include (repeatable) |
| `-d, --description` | Profile description |

### Modify Flags

| Flag | Description |
|------|-------------|
| `--add-parent` | Add parent profile (repeatable) |
| `--remove-parent` | Remove parent profile (repeatable) |
| `--add-bundle` | Add bundle reference (repeatable) |
| `--remove-bundle` | Remove bundle reference (repeatable) |
| `-d, --description` | Update description |
| `--exclude-fragment` | Add fragment to exclusion list (repeatable) |
| `--include-fragment` | Remove fragment from exclusion list (repeatable) |
| `--exclude-prompt` | Add prompt to exclusion list (repeatable) |
| `--include-prompt` | Remove prompt from exclusion list (repeatable) |
| `--exclude-mcp` | Add MCP server to exclusion list (repeatable) |
| `--include-mcp` | Remove MCP server from exclusion list (repeatable) |

### Examples

```bash
ctxloom profile list
ctxloom profile show developer
ctxloom profile create my-profile -b python-tools -d "My dev profile"
ctxloom profile create child --parent base --parent security -b extras
ctxloom profile modify developer --exclude-fragment verbose-logging
ctxloom profile modify developer --include-mcp slow-server
ctxloom profile edit my-profile
ctxloom profile install ctxloom-default/python-developer
```

## ctxloom remote

Manage remote sources.

| Command | Arguments | Description |
|---------|-----------|-------------|
| `add` | `<name> <url>` | Register remote source |
| `remove` | `<name>` | Remove registered remote |
| `list` | | List configured remotes |
| `default` | `[name]` | Get/set default remote |
| `search` | `<query>` | Search for bundles/profiles |
| `browse` | `<remote>` | Browse remote contents |
| `discover` | | Find ctxloom repos on GitHub/GitLab |
| `lock` | | Generate lockfile from installed items |
| `update` | `[name]` | Update remotes (all or specific) |
| `sync` | | Sync dependencies |

### URL Formats

| Format | Example |
|--------|---------|
| GitHub shorthand | `alice/ctxloom` |
| Full HTTPS | `https://github.com/alice/ctxloom` |
| GitLab | `https://gitlab.com/corp/ctxloom` |
| SSH (converted to HTTPS) | `git@github.com:alice/ctxloom.git` |

### Examples

```bash
ctxloom remote add myteam myorg/ctxloom-team
ctxloom remote add corp https://gitlab.com/corp/ctxloom
ctxloom remote list
ctxloom remote default myteam
ctxloom remote browse ctxloom-default
ctxloom remote search "python testing"
ctxloom remote discover
ctxloom remote sync
```

## ctxloom mcp

Manage MCP server configuration.

| Command | Flags | Arguments | Description |
|---------|-------|-----------|-------------|
| `serve` | | | Run as MCP server over stdio (default) |
| `list` | | | List configured MCP servers |
| `add` | `-c`, `-a`, `-b` | `<name>` | Add MCP server |
| `remove` | `-b` | `<name>` | Remove MCP server |
| `show` | | `<name>` | Show MCP server details |
| `auto-register` | `--disable`, `--enable` | | Configure auto-registration |

### Add Flags

| Flag | Description |
|------|-------------|
| `-c, --command` | Command to run (required) |
| `-a, --args` | Command arguments (repeatable) |
| `-b, --backend` | Backend scope: `unified`, `claude-code`, `gemini` |

### Examples

```bash
ctxloom mcp serve                    # Run as MCP server
ctxloom mcp list
ctxloom mcp add tree-sitter -c "npx" -a "tree-sitter-mcp" -a "--stdio"
ctxloom mcp add my-server -c "/path/to/server" -b claude-code
ctxloom mcp remove tree-sitter
ctxloom mcp auto-register --disable
```

## ctxloom memory

Manage session memory.

| Command | Flags | Description |
|---------|-------|-------------|
| `check` | | Show current session size and threshold status |
| `compact` | `--session`, `--model` | Compact session to distilled summary |
| `list` | `--backend` | List sessions with compaction status |
| `load` | `--model` | Load and distill a specific session |
| `query` | `--limit` | Semantic search across session history |

### Examples

```bash
ctxloom memory check                         # Check session size
ctxloom memory compact                       # Compact current session
ctxloom memory compact --session abc123      # Compact specific session
ctxloom memory list                          # List all sessions
ctxloom memory list --backend gemini         # List Gemini sessions
ctxloom memory load abc123def                # Load specific session
ctxloom memory query "authentication flow"  # Search session history
```

## ctxloom completion

Generate shell completion scripts.

```bash
ctxloom completion [bash|zsh|fish|powershell]
```

### Installation

**Bash:**
```bash
source <(ctxloom completion bash)                        # Current session
ctxloom completion bash > /etc/bash_completion.d/ctxloom # Permanent (Linux)
ctxloom completion bash > /usr/local/etc/bash_completion.d/ctxloom   # macOS
```

**Zsh:**
```bash
echo "autoload -U compinit; compinit" >> ~/.zshrc  # Enable if needed
ctxloom completion zsh > "${fpath[1]}/_ctxloom"
```

**Fish:**
```fish
ctxloom completion fish > ~/.config/fish/completions/ctxloom.fish
```

**PowerShell:**
```powershell
ctxloom completion powershell | Out-String | Invoke-Expression
```
