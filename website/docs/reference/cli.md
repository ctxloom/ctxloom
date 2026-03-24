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
| `-f, --fragment` | Fragment(s) to include (repeatable) |
| `-t, --tag` | Include fragments with tag (repeatable) |
| `-p, --profile` | Use a named profile |
| `-l, --plugin` | AI plugin (default: claude-code) |
| `-r, --run-prompt` | Run a saved prompt by name |
| `-n, --dry-run` | Preview assembled context |
| `-q, --quiet` | Suppress warnings |
| `--print` | Print response and exit (non-interactive) |
| `-v, --verbose` | Increase verbosity (repeatable: -v, -vv, -vvv) |

### Examples

```bash
scm run -p developer "implement error handling"
scm run -f python-tools#fragments/typing "add type hints"
scm run -f security#fragments/owasp -f python#fragments/errors "audit"
scm run -t security "check for vulnerabilities"
scm run -l gemini "use Gemini"
scm run -n  # Preview only
```

## scm bundle

Manage bundles.

```bash
scm bundle list                     # List all bundles
scm bundle show <name>              # Show bundle contents
scm bundle view <name[#path]>       # View bundle or item content
scm bundle create <name>            # Create a new bundle
scm bundle edit <name>              # Edit bundle metadata
scm bundle export <name> <dir>      # Export bundle to directory
scm bundle import <path>            # Import bundle from file
scm bundle distill <patterns...>    # Distill bundle files
```

### Fragment/Prompt/MCP Editing

```bash
scm bundle fragment edit <bundle> <fragment>  # Edit fragment content
scm bundle prompt edit <bundle> <prompt>      # Edit prompt content
scm bundle mcp edit <bundle> <mcp>            # Edit MCP config
```

## scm profile

Manage profiles.

```bash
scm profile list                    # List all profiles
scm profile show <name>             # Show profile details
scm profile add <name>              # Create a new profile
scm profile update <name>           # Update profile
scm profile remove <name>           # Delete a profile
scm profile export <name> <dir>     # Export profile to directory
scm profile import <path>           # Import profile from file
```

### Profile Creation

```bash
scm profile add developer -b python-tools -d "Standard dev context"
```

### Profile Update

```bash
scm profile update developer --add-bundle security-tools
scm profile update developer --add-parent scm-main/base
scm profile update developer --remove-bundle old-bundle
```

## scm remote

Manage remote sources.

```bash
scm remote add <name> <url>         # Register a remote source
scm remote remove <name>            # Remove a remote
scm remote list                     # List configured remotes
scm remote pull <ref> --type <type> # Pull content from remote
scm remote discover                 # Find public SCM repositories
scm remote browse <name>            # Browse remote contents
```

### Examples

```bash
scm remote add myteam myorg/scm-team      # GitHub shorthand
scm remote add corp https://gitlab.com/corp/scm
scm remote pull scm-main/testing --type bundle
scm remote pull scm-main/python-developer --type profile
```

## scm mcp

Run as MCP server over stdio.

```bash
scm mcp
```

## scm mcp-servers

Manage MCP server configurations.

```bash
scm mcp-servers list                # List configured MCP servers
scm mcp-servers add <name>          # Add MCP server config
scm mcp-servers remove <name>       # Remove MCP server config
```

## scm completion

Generate shell completion scripts.

```bash
scm completion [bash|zsh|fish|powershell]
```

### Installation

**Bash:**
```bash
source <(scm completion bash)                              # Current session
scm completion bash > /etc/bash_completion.d/scm           # Permanent
```

**Zsh:**
```bash
scm completion zsh > "${fpath[1]}/_scm"
```

**Fish:**
```fish
scm completion fish > ~/.config/fish/completions/scm.fish
```
