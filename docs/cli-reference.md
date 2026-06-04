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
| `--llm` | `-l` | Config label to use (e.g. claude-code, claude-fast, gemini-code, gemini-fast); overrides the configured default |
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
| `add <name> <url>` | Add remote source (optionally `--forge <label>`) |
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
- `user/repo` - GitHub shorthand (expands to `https://github.com/user/repo`)
- `https://github.com/user/repo` - Full GitHub URL
- `https://git.example.com/corp/repo` - Any other git host (GitLab, Gitea, Bitbucket, self-hosted)
- `git@github.com:user/repo.git` - SSH URL (converted to HTTPS)

**Forges:**

A remote binds to a *forge* — the adapter ctxloom uses to read and publish:

- `github` — rich adapter over the GitHub REST API (file/dir reads, ref
  resolution, repo search, PR publish). Serves github.com and GitHub Enterprise
  (the host comes from the remote URL; the API endpoint is derived from it).
  Token: the env var named by the forge's `token_env`, default `GITHUB_TOKEN`.
- `git` — generic adapter that clones over HTTPS/SSH and reads the working copy.
  Works against any git host for consumption (no API search or PR publish). Auth
  is ambient git (credential helper, ssh-agent, `~/.ssh/config`, per-host
  `.gitconfig`).

Without `--forge`, the forge resolves from the URL host: github.com (and the
`owner/repo` shorthand) use `github`; every other host uses `git`. Pass
`--forge <label>` to override with `github`, `git`, or a `forges:` label
configured in `remotes.yaml` (for example a GitHub Enterprise instance with its
own `base_url`/`token_env`).

**Examples:**
```bash
ctxloom remote list
ctxloom remote add personal myuser/ctxloom-profiles
ctxloom remote add corp https://git.example.com/corp/repo --forge git
ctxloom remote add work https://github.mycorp.com/me/ctxloom --forge work-ghe
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

#### `ctxloom manage`

Install, inspect, and remove ctxloom's project harness. Everything that mutates
the harness (`.ctxloom`, hooks, statusline, MCP registration, command files,
`.gitignore`, config) lives here.

| Subcommand | Description |
|------------|-------------|
| `init` | Scaffold a new `.ctxloom` directory (top-level `ctxloom init` is an alias) |
| `install [--print]` | One-shot non-interactive setup: scaffold + gitignore + hooks/MCP/statusline |
| `uninstall` | Remove ctxloom hooks, statusline, MCP entry, and command files |
| `status` | Show what ctxloom has wired into the project |
| `hooks [install\|uninstall\|status]` | Manage ctxloom backend hooks |
| `mcp [install\|uninstall]` | Toggle auto-registration of ctxloom's own MCP server |
| `mcp servers [add\|remove\|list\|show]` | Manage configured MCP servers |
| `statusline [install\|uninstall]` | Toggle ctxloom's HUD statusline (disable to keep your own) |
| `config [show\|get\|edit\|init]` | Show or modify configuration |
| `gitignore install` | Add ctxloom's private-state and transient-artifact ignores |

**Examples:**
```bash
ctxloom manage install                      # Wire ctxloom into this project
ctxloom manage status                       # What's wired in?
ctxloom manage hooks install                # Re-apply hooks after editing bundles
ctxloom manage mcp servers add my-server -c npx -a my-mcp
ctxloom manage mcp uninstall                # Stop auto-registering ctxloom's server
ctxloom manage statusline uninstall         # Keep your own statusline, not ctxloom's HUD
ctxloom manage config show
ctxloom manage config get llm
ctxloom manage uninstall                    # Remove integration (keeps .ctxloom)
```

#### `ctxloom mcp`

Run ctxloom as an MCP server over stdio (the runtime entrypoint referenced by
generated `.mcp.json`). Server-config management lives under `ctxloom manage mcp`.

| Subcommand | Description |
|------------|-------------|
| `serve` | Run as MCP server over stdio (same as bare `ctxloom mcp`) |

**Examples:**
```bash
ctxloom mcp serve                           # Run MCP server
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

#### `ctxloom llm`

Manage LLM backends.

| Subcommand | Description |
|------------|-------------|
| `list` | List available LLMs |
| `default [name]` | Get/set the default LLM |

**Examples:**
```bash
ctxloom llm list
ctxloom llm default claude-code
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
| `GITHUB_TOKEN` | Default token for the `github` forge (override per forge via `token_env`) |

---

## Configuration Files

| File | Description |
|------|-------------|
| `.ctxloom/config.yaml` | Main configuration |
| `.ctxloom/remotes.yaml` | Remote sources |
| `.ctxloom/bundles/*.yaml` | Bundle files |
| `.ctxloom/profiles/*.yaml` | Profile files |
| `.ctxloom/lock.yaml` | Dependency lockfile |
