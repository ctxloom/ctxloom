---
sidebar_position: 1
---

# CLI Reference

Complete reference for all SCM commands.

## scm init

Initialize a new .scm directory.

```bash
scm init              # Create .scm in current directory
scm init --home       # Create/ensure ~/.scm exists
```

## scm run

Assemble context and run AI plugin.

```bash
scm run [flags] [prompt...]
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
scm run -p developer "implement error handling"
scm run -f python-tools#fragments/typing "add type hints"
scm run -f security#fragments/owasp -f python#fragments/errors "audit"
scm run -t security "check for vulnerabilities"
scm run --plugin gemini "use Gemini"
scm run --dry-run  # Preview only
```

## scm fragment

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
scm fragment list
scm fragment list --bundle python-tools
scm fragment show python-tools#fragments/typing
scm fragment show --distilled python-tools#fragments/typing
scm fragment create my-bundle coding-standards
scm fragment edit my-bundle#fragments/coding-standards
scm fragment distill my-bundle#fragments/coding-standards
scm fragment install scm-main/testing
scm fragment install --blind scm-main/security  # Skip preview
```

## scm prompt

Manage prompts.

| Command | Arguments | Description |
|---------|-----------|-------------|
| `list` | | List all prompts |
| `show` | `bundle#prompts/name` | Show prompt content |
| `create` | `<bundle> <name>` | Create new prompt |
| `delete` | `bundle#prompts/name` | Delete prompt |
| `edit` | `bundle#prompts/name` | Edit prompt in editor |
| `install` | `<reference>` | Install from remote |

## scm profile

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
scm profile list
scm profile show developer
scm profile create my-profile -b python-tools -d "My dev profile"
scm profile create child --parent base --parent security -b extras
scm profile modify developer --exclude-fragment verbose-logging
scm profile modify developer --include-mcp slow-server
scm profile edit my-profile
scm profile install scm-main/python-developer
```

## scm remote

Manage remote sources.

| Command | Arguments | Description |
|---------|-----------|-------------|
| `add` | `<name> <url>` | Register remote source |
| `remove` | `<name>` | Remove registered remote |
| `list` | | List configured remotes |
| `default` | `[name]` | Get/set default remote |
| `search` | `<query>` | Search for bundles/profiles |
| `browse` | `<remote>` | Browse remote contents |
| `discover` | | Find SCM repos on GitHub/GitLab |
| `lock` | | Generate lockfile from installed items |
| `update` | `[name]` | Update remotes (all or specific) |
| `sync` | | Sync dependencies |

### URL Formats

| Format | Example |
|--------|---------|
| GitHub shorthand | `alice/scm` |
| Full HTTPS | `https://github.com/alice/scm` |
| GitLab | `https://gitlab.com/corp/scm` |
| SSH (converted to HTTPS) | `git@github.com:alice/scm.git` |

### Examples

```bash
scm remote add myteam myorg/scm-team
scm remote add corp https://gitlab.com/corp/scm
scm remote list
scm remote default myteam
scm remote browse scm-main
scm remote search "python testing"
scm remote discover
scm remote sync
```

## scm mcp

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
scm mcp serve                    # Run as MCP server
scm mcp list
scm mcp add tree-sitter -c "npx" -a "tree-sitter-mcp" -a "--stdio"
scm mcp add my-server -c "/path/to/server" -b claude-code
scm mcp remove tree-sitter
scm mcp auto-register --disable
```

## scm memory

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
scm memory check                         # Check session size
scm memory compact                       # Compact current session
scm memory compact --session abc123      # Compact specific session
scm memory list                          # List all sessions
scm memory list --backend gemini         # List Gemini sessions
scm memory load abc123def                # Load specific session
scm memory query "authentication flow"  # Search session history
```

## scm completion

Generate shell completion scripts.

```bash
scm completion [bash|zsh|fish|powershell]
```

### Installation

**Bash:**
```bash
source <(scm completion bash)                    # Current session
scm completion bash > /etc/bash_completion.d/scm # Permanent (Linux)
scm completion bash > $(brew --prefix)/etc/bash_completion.d/scm # macOS
```

**Zsh:**
```bash
echo "autoload -U compinit; compinit" >> ~/.zshrc  # Enable if needed
scm completion zsh > "${fpath[1]}/_scm"
```

**Fish:**
```fish
scm completion fish > ~/.config/fish/completions/scm.fish
```

**PowerShell:**
```powershell
scm completion powershell | Out-String | Invoke-Expression
```
