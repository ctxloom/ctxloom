# ctxloom CLI Reference

Complete reference for all ctxloom commands and options.

## Quick Reference

```bash
ctxloom run -p <profile> "prompt"     # Run with profile
ctxloom search -t <tag>               # Search by tag
ctxloom fragment list                 # List fragments
ctxloom remote sync                   # Sync dependencies
```

## Commands

### Workflow Commands

#### `ctxloom run`

Assemble context and run AI.

```bash
ctxloom run [flags] [prompt...]
```

**Flags:**
| Flag | Short | Description |
|------|-------|-------------|
| `--fragment` | `-f` | Add fragment by reference (repeatable) |
| `--profile` | `-p` | Load a predefined profile |
| `--tag` | `-t` | Include fragments with tag (repeatable) |
| `--prompt` | | Custom prompt text |
| `--saved-prompt` | | Load saved prompt template |
| `--plugin` | `-l` | LLM plugin to use |
| `--dry-run` | `-n` | Preview context without running |
| `--print` | | Print assembled context |
| `--verbose` | `-v` | Increase verbosity (-v, -vv, -vvv) |

**Examples:**
```bash
ctxloom run -p developer "explain this code"
ctxloom run -f core#fragments/tdd -t golang "review PR"
ctxloom run -n  # Preview what context would be sent
```

#### `ctxloom init`

Initialize a new .ctxloom directory.

```bash
ctxloom init [flags]
```

**Flags:**
| Flag | Description |
|------|-------------|
| `--home` | Initialize in ~/.ctxloom instead of current directory |
| `--non-interactive` | Skip interactive prompts |
| `--skip-launch` | Don't auto-launch AI after init |
| `--engine` | Pre-select AI engine (claude-code, gemini, etc.) |

**Examples:**
```bash
ctxloom init                    # Interactive setup
ctxloom init --engine gemini    # Pre-select engine
ctxloom init --home             # Initialize global config
```

---

### Content Commands

#### `ctxloom fragment`

Manage context fragments.

| Subcommand | Description |
|------------|-------------|
| `list` | List all fragments |
| `show <ref>` | Show fragment content |
| `create <bundle> <name>` | Create new fragment |
| `delete <ref>` | Delete fragment |
| `edit <ref>` | Edit fragment in editor |
| `distill <ref>` | Create token-efficient version |
| `search [query]` | Search fragments |
| `install <reference>` | Install bundle from remote |
| `push <bundle> [remote]` | Push bundle to remote |

**Reference format:** `bundle#fragments/name`

**Examples:**
```bash
ctxloom fragment list
ctxloom fragment list --bundle core
ctxloom fragment show core#fragments/tdd
ctxloom fragment show core#fragments/tdd --distilled
ctxloom fragment create my-bundle coding-standards
ctxloom fragment edit core#fragments/tdd
ctxloom fragment search -t golang
ctxloom fragment install ctxloom-default/core
```

#### `ctxloom prompt`

Manage prompts. Same subcommands as `fragment`.

**Reference format:** `bundle#prompts/name`

**Examples:**
```bash
ctxloom prompt list
ctxloom prompt show my-bundle#prompts/code-review
ctxloom prompt create my-bundle review-pr
```

#### `ctxloom profile`

Manage profiles (named fragment collections).

| Subcommand | Description |
|------------|-------------|
| `list` | List all profiles |
| `show <name>` | Show profile details |
| `create <name>` | Create new profile |
| `delete <name>` | Delete profile |
| `edit <name>` | Edit profile YAML |
| `modify <name>` | Modify profile configuration |
| `install <reference>` | Install profile from remote |
| `push <name> [remote]` | Push profile to remote |
| `export <name> <dir>` | Export profile to directory |
| `import <file>` | Import profile from file |

**Examples:**
```bash
ctxloom profile list
ctxloom profile show developer
ctxloom profile create backend --parent developer --bundle go-tools
ctxloom profile modify backend --add-bundle security
```

#### `ctxloom search`

Search fragments and prompts.

```bash
ctxloom search [query] [flags]
```

**Flags:**
| Flag | Short | Description |
|------|-------|-------------|
| `--tag` | `-t` | Filter by tags (comma-separated, repeatable) |
| `--type` | | Filter by type: `fragment` or `prompt` |

**Examples:**
```bash
ctxloom search cache                    # Search by name
ctxloom search -t golang                # Search by tag
ctxloom search -t golang,testing        # Multiple tags
ctxloom search --type fragment cache    # Only fragments
```

---

### Remote Commands

#### `ctxloom remote`

Manage remote repositories.

| Subcommand | Description |
|------------|-------------|
| `list` | List configured remotes |
| `add <name> <url>` | Add remote source |
| `remove <name>` | Remove remote |
| `default [name]` | Get/set default remote |
| `sync` | Sync dependencies from profiles |
| `search <query>` | Search across remotes |
| `browse <remote>` | Browse remote contents |
| `discover` | Discover ctxloom repositories |
| `lock` | Generate lockfile from installed items |
| `update` | Check for and apply updates |
| `vendor` | Copy dependencies locally for offline use |
| `replace` | Manage local overrides for development |

**URL formats:**
- `user/repo` - GitHub shorthand
- `https://github.com/user/repo` - Full URL
- `https://gitlab.com/corp/repo` - GitLab

**Examples:**
```bash
ctxloom remote list
ctxloom remote add personal myuser/ctxloom-profiles
ctxloom remote default personal
ctxloom remote sync --force
ctxloom remote search golang
ctxloom remote browse ctxloom-default
ctxloom remote discover --min-stars 10
ctxloom remote lock                     # Generate lockfile
ctxloom remote update                   # Check for updates
ctxloom remote vendor --enable          # Enable offline mode
ctxloom remote replace add alice/core ./local/core.yaml
```

#### `ctxloom bundle` (Advanced)

Manage bundles directly. Most users should use `fragment` and `prompt` commands instead.

| Subcommand | Description |
|------------|-------------|
| `list` | List installed bundles |
| `show <name>` | Show bundle contents |
| `view <name>` | View bundle content (supports `#path` drilling) |
| `create <name>` | Create new bundle |
| `edit <name>` | Edit bundle metadata |
| `delete <name>` | Delete bundle |
| `distill <patterns>` | Distill multiple bundles |
| `export <name> <dir>` | Export bundle to directory |
| `import <file>` | Import bundle from file |
| `push <name> [remote]` | Push bundle to remote |

**Examples:**
```bash
ctxloom bundle list
ctxloom bundle show my-bundle
ctxloom bundle view my-bundle#fragments/coding
ctxloom bundle create my-bundle --description "My standards"
ctxloom bundle distill "*.yaml" --force
ctxloom bundle export my-bundle ./exported/
```

---

### Infrastructure Commands

#### `ctxloom mcp`

Run as MCP server or manage MCP configurations.

| Subcommand | Description |
|------------|-------------|
| `serve` | Run as MCP server over stdio |
| `list` | List configured MCP servers |
| `add <name>` | Add MCP server |
| `remove <name>` | Remove MCP server |
| `show <name>` | Show MCP server details |
| `auto-register` | Configure auto-registration |

**Examples:**
```bash
ctxloom mcp serve                           # Run MCP server
ctxloom mcp list
ctxloom mcp add my-server -c npx -a my-mcp
ctxloom mcp auto-register --enable
```

#### `ctxloom config`

Show or modify configuration.

| Subcommand | Description |
|------------|-------------|
| `show` | Show full configuration |
| `get <section>` | Get specific section |

**Sections:** `defaults`, `llm`, `mcp`, `profiles`

**Examples:**
```bash
ctxloom config show
ctxloom config get defaults
ctxloom config get llm
```

---

### Utility Commands

#### `ctxloom memory`

Manage session memory for compaction.

| Subcommand | Description |
|------------|-------------|
| `list` | List all sessions |
| `show <session>` | Show session details |
| `compact` | Compact session log |

**Examples:**
```bash
ctxloom memory list
ctxloom memory show abc123
ctxloom memory compact --session abc123
```

#### `ctxloom plugin`

Manage AI backend plugins.

| Subcommand | Description |
|------------|-------------|
| `list` | List available plugins |
| `default [name]` | Get/set default plugin |
| `extract` | Extract built-in plugins |

**Examples:**
```bash
ctxloom plugin list
ctxloom plugin default claude-code
```

#### `ctxloom meta stamp`

Output metadata for session tracking.

```bash
ctxloom meta stamp
```

#### `ctxloom version`

Print version number.

```bash
ctxloom version
```

#### `ctxloom completion`

Generate shell completion scripts.

```bash
ctxloom completion bash
ctxloom completion zsh
ctxloom completion fish
ctxloom completion powershell
```

---

## MCP Tools

When running as an MCP server (`ctxloom mcp serve`), the following tools are exposed:

### Content Management
- `list_fragments` - List available fragments
- `get_fragment` - Get fragment content
- `create_fragment` - Create new fragment
- `delete_fragment` - Delete fragment
- `list_prompts` - List prompts
- `get_prompt` - Get prompt content
- `search_content` - Search all content

### Profile Management
- `list_profiles` - List profiles
- `get_profile` - Get profile config
- `create_profile` - Create profile
- `update_profile` - Update profile
- `delete_profile` - Delete profile
- `assemble_context` - Assemble context from profile/fragments

### Remote Operations
- `list_remotes` - List remotes
- `add_remote` - Add remote
- `remove_remote` - Remove remote
- `search_remotes` - Search across remotes
- `browse_remote` - Browse remote contents
- `pull_remote` - Install from remote
- `discover_remotes` - Discover repositories
- `sync_dependencies` - Sync from config

### MCP Server Management
- `list_mcp_servers` - List MCP servers
- `add_mcp_server` - Add MCP server
- `remove_mcp_server` - Remove MCP server
- `set_mcp_auto_register` - Configure auto-registration
- `apply_hooks` - Apply hooks to backend configs

### Session Management
- `list_sessions` - List sessions
- `compact_session` - Compact session log
- `load_session` - Load session context
- `recover_session` - Recover after /clear
- `browse_session_history` - Browse recent sessions
- `get_previous_session` - Get previous session

---

## Reference Syntax

### Bundle References
```
bundle#fragments/name     # Fragment in bundle
bundle#prompts/name       # Prompt in bundle
bundle#mcp/name           # MCP config in bundle
```

### Remote References
```
remote/bundle             # Bundle from remote
remote/bundle@v1.0.0      # Versioned bundle
user/repo                 # GitHub shorthand
```

---

## Environment Variables

| Variable | Description |
|----------|-------------|
| `CTXLOOM_HOME` | Override default config directory |
| `EDITOR` | Editor for edit commands |
| `GITHUB_TOKEN` | GitHub API authentication |

---

## Configuration Files

| File | Description |
|------|-------------|
| `.ctxloom/config.yaml` | Main configuration |
| `.ctxloom/remotes.yaml` | Remote sources |
| `.ctxloom/bundles/*.yaml` | Bundle files |
| `.ctxloom/profiles/*.yaml` | Profile files |
| `.ctxloom/lock.yaml` | Dependency lockfile |
