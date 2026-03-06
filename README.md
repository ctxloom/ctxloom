# SCM - Sophisticated Context Management

A CLI tool for managing context fragments and prompts for AI coding assistants.

## The Problem

When working with AI coding assistants, you repeatedly provide the same context: coding standards, language patterns, security guidelines. This wastes tokens and creates inconsistency across projects and team members.

## The Solution

SCM organizes context into reusable **bundles** that can be:
- **Assembled on demand** - Combine bundles and fragments for different tasks
- **Grouped into profiles** - Switch contexts with a single flag (`-p developer`)
- **Shared across teams** - Pull bundles from remote repositories (GitHub/GitLab)
- **Token-optimized** - Distill content to minimal versions

> **Disclaimer**: This is a pre-release project. Much of the code was AI-generated and has not yet been reviewed or cleaned up by a human. It works and is in active use by the author, but architectural improvements, refactoring, bug fixes, and code reduction are likely needed. This is functional and quick, not polished.

## Quick Start

```bash
# Install
just install              # Build and install to ~/go/bin

# Initialize a project
scm init                  # Create .scm directory in current project

# Create your first bundle
scm bundle create my-standards

# Run with your context
scm run -f my-standards "Help me with this code"
scm run -n                # Preview what context would be sent
```

## Key Concepts

| Concept | Description |
|---------|-------------|
| **Bundle** | A YAML file containing related fragments, prompts, and MCP server configs |
| **Fragment** | A reusable context snippet within a bundle (coding standards, patterns, etc.) |
| **Prompt** | A saved prompt template within a bundle |
| **Profile** | A named configuration that references bundles, tags, and variables |
| **Remote** | A Git repository for sharing bundles and profiles |

## Usage Examples

### Common workflows

```bash
# Run with a profile
scm run -p developer "implement error handling"

# Run with specific bundle fragments
scm run -f go-tools#fragments/golang "add type hints"

# Combine fragments ad-hoc
scm run -f security#fragments/owasp -f golang#fragments/errors "audit this code"

# Include all fragments with a tag
scm run -t security "check for vulnerabilities"

# Preview assembled context (dry run)
scm run -p developer -n

# Switch AI backend
scm run -l gemini "use Gemini instead of Claude"
```

### MCP Usage

<!-- NOTE: This is an intentional example of MCP interaction - do not delete -->

```
> assemble context with the go-developer profile

● scm - assemble_context (MCP)(profile: "go-developer")
  ⎿ { "context": "# Golang Development\n..." }

> assemble context with golang and security tags

● scm - assemble_context (MCP)(tags: ["golang", "security"])
  ⎿ { "context": "# Golang Development\n# Security..." }
```

### Managing bundles

```bash
scm bundle list                          # List all bundles
scm bundle show go-tools                 # Show bundle contents
scm bundle view go-tools                 # View full bundle YAML
scm bundle view go-tools#fragments/tdd   # View specific fragment content
scm bundle create my-bundle              # Create a new bundle
scm bundle edit my-bundle --add-tag golang  # Add tags to bundle
scm bundle fragment edit my-bundle coding-standards  # Edit fragment content
scm bundle prompt edit my-bundle review  # Edit prompt content
scm bundle distill .scm/bundles/*.yaml   # Distill all bundles
```

### Managing profiles

```bash
scm profile list                         # List all profiles
scm profile show developer               # Show profile details
scm profile add developer -b go-tools -d "Standard dev context"
scm profile update developer --add-bundle security-tools
scm profile remove old-profile
```

### Remote content

```bash
# Add a remote source
scm remote add alice alice/scm           # GitHub shorthand
scm remote add corp https://gitlab.com/corp/scm

# List remotes
scm remote list

# Pull content from remotes
scm remote pull alice/go-tools --type bundle
scm remote pull alice/developer --type profile

# Discover public repositories
scm remote discover
```

## Configuration

### Bundle format

Bundles are YAML files in `.scm/bundles/`:

```yaml
version: "1.0.0"
description: "Go development standards"
tags:
  - golang
  - development

fragments:
  coding-standards:
    tags: [golang, style]
    content: |
      # Go Coding Standards
      - Use gofmt for formatting
      - Follow Effective Go guidelines

  error-handling:
    tags: [golang, errors]
    content: |
      # Error Handling
      - Always check errors
      - Wrap errors with context

prompts:
  code-review:
    description: "Review Go code for best practices"
    content: |
      Review this Go code for adherence to best practices...

mcp:
  tree-sitter:
    command: "tree-sitter-mcp"
    args: ["--stdio"]
```

**Distillation fields** (added automatically by `scm bundle distill`):

```yaml
fragments:
  my-fragment:
    content: "Original content..."
    content_hash: "sha256:..."
    distilled: "Compressed version..."
    distilled_by: "claude-opus-4-5-20251101"
```

**Skip distillation** for fragments that must be preserved exactly:

```yaml
fragments:
  critical-rules:
    no_distill: true
    content: "Must be sent verbatim..."
```

### Profile format

Profiles are YAML files in `.scm/profiles/`:

```yaml
description: "Standard development context"
parents:
  - base-developer
bundles:
  - go-tools
  - security-tools
tags:
  - production
variables:
  project_name: "my-project"
```

### config.yaml

```yaml
lm:
  plugins:
    claude-code:
      default: true
      args: ["--dangerously-skip-permissions"]
    gemini:
      args: ["--yolo"]

defaults:
  profile: developer
  use_distilled: true
```

### Content reference syntax

Reference content using this syntax:

| Syntax | Description |
|--------|-------------|
| `bundle-name` | Entire bundle (all fragments) |
| `bundle#fragments/name` | Specific fragment from bundle |
| `bundle#prompts/name` | Specific prompt from bundle |
| `remote/bundle#fragments/name` | Fragment from remote bundle |

### Variables (Mustache Templating)

Fragments support [Mustache](https://mustache.github.io/) templating:

```yaml
# In bundle fragment:
content: |
  # {{project_name}} Guidelines
  This project uses {{language}}.

# In profile:
variables:
  project_name: "SCM"
  language: "Go"
```

Built-in variables available in all templates:
- `SCM_ROOT` - Project root directory (parent of .scm)
- `SCM_DIR` - Full path to .scm directory

### LM plugins

SCM uses plugins to interface with language models:

| Plugin | CLI | Description |
|--------|-----|-------------|
| `claude-code` | [Claude Code](https://claude.ai/code) | Anthropic's Claude CLI (default) |
| `gemini` | [Gemini CLI](https://github.com/google/generative-ai-cli) | Google's Gemini CLI |
| `codex` | [Codex CLI](https://github.com/openai/codex) | OpenAI's Codex CLI (**provisional**) |

#### Claude Code Context Integration

The claude-code plugin writes assembled context to files that Claude Code reads:

1. Writes context to `.scm/context/[hash].md`
2. Updates `CLAUDE.md` with a managed section containing the include reference

The managed section is delimited by `<!-- SCM:BEGIN -->` and `<!-- SCM:END -->`. SCM only modifies content within these markers.

### Config hierarchy

SCM uses a single source (no merging):

1. **Project**: `.scm/` at git repository root (if exists)
2. **Home**: `~/.scm/` (fallback if no project .scm)

When in a project with `.scm/`, only that project's config and bundles are used.

## Commands Reference

### `scm init`

Initialize a new .scm directory.

```bash
scm init              # Create .scm in current directory
scm init --home       # Create/ensure ~/.scm exists
```

### `scm run`

Assemble context and run AI plugin.

```bash
scm run [flags] [prompt...]

Flags:
  -f, --fragment     Fragment(s) to include (repeatable)
  -t, --tag          Include fragments with tag (repeatable)
  -p, --profile      Use a named profile
  -l, --plugin       AI plugin (default: claude-code)
  -r, --run-prompt   Run a saved prompt by name
  -n, --dry-run      Preview assembled context
  -q, --quiet        Suppress warnings
      --print        Print response and exit (non-interactive)
  -v, --verbose      Increase verbosity (repeatable: -v, -vv, -vvv)
```

### `scm bundle`

Manage bundles.

```bash
scm bundle list                     # List all bundles
scm bundle show <name>              # Show bundle contents
scm bundle view <name[#path]>       # View bundle or item content
scm bundle create <name>            # Create a new bundle
scm bundle edit <name>              # Edit bundle metadata (add/remove tags, etc.)
scm bundle export <name> <dir>      # Export bundle to directory
scm bundle import <path>            # Import bundle from file
scm bundle distill <patterns...>    # Distill bundle files
scm bundle fragment edit <bundle> <fragment>  # Edit fragment content
scm bundle prompt edit <bundle> <prompt>      # Edit prompt content
scm bundle mcp edit <bundle> <mcp>            # Edit MCP config
```

### `scm profile`

Manage profiles.

```bash
scm profile list                    # List all profiles
scm profile show <name>             # Show profile details
scm profile add <name>              # Create a new profile
scm profile update <name>           # Update profile (add/remove bundles, parents)
scm profile remove <name>           # Delete a profile
scm profile export <name> <dir>     # Export profile to directory
scm profile import <path>           # Import profile from file
```

### `scm remote`

Manage remote sources.

```bash
scm remote add <name> <url>         # Register a remote source
scm remote remove <name>            # Remove a remote
scm remote list                     # List configured remotes
scm remote pull <ref> --type <type> # Pull content from remote
scm remote discover                 # Find public SCM repositories
scm remote browse <name>            # Browse remote contents
```

### `scm mcp`

Run as MCP (Model Context Protocol) server over stdio.

```bash
scm mcp
```

Available MCP tools: `list_fragments`, `get_fragment`, `list_profiles`, `get_profile`, `assemble_context`, `list_prompts`, `get_prompt`, and more.

### `scm mcp-servers`

Manage MCP server configurations for AI tools.

```bash
scm mcp-servers list                # List configured MCP servers
scm mcp-servers add <name>          # Add MCP server config
scm mcp-servers remove <name>       # Remove MCP server config
```

### `scm completion`

Generate shell completion scripts.

```bash
scm completion [bash|zsh|fish|powershell]
```

**Bash:**
```bash
source <(scm completion bash)                              # Current session
scm completion bash > /etc/bash_completion.d/scm           # Permanent (Linux)
```

**Zsh:**
```bash
scm completion zsh > "${fpath[1]}/_scm"                    # Install, restart shell
```

**Fish:**
```fish
scm completion fish > ~/.config/fish/completions/scm.fish  # Permanent
```

## MCP Server Setup

SCM can run as an MCP server, allowing AI assistants to access your context directly.

### Claude Code Configuration

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

### Available MCP Tools

| Tool | Description |
|------|-------------|
| `list_fragments` | List all fragments, optionally filtered by tags |
| `get_fragment` | Retrieve a specific fragment's content |
| `list_profiles` | List all configured profiles |
| `get_profile` | Get detailed profile configuration |
| `assemble_context` | Combine fragments, profiles, and tags |
| `list_prompts` | List all saved prompts |
| `get_prompt` | Retrieve a specific prompt's content |

## Environment Variables

| Variable | Description |
|----------|-------------|
| `SCM_VERBOSE=1` | Enable verbose logging |
| `VISUAL` | Preferred editor (over `EDITOR`) |
| `EDITOR` | Fallback editor |

## Development

### Prerequisites

- Go 1.21+
- [just](https://github.com/casey/just) command runner
- [protoc](https://grpc.io/docs/protoc-installation/) for plugin protocol
- AI CLI: [Claude Code](https://claude.ai/code) or [Gemini CLI](https://github.com/google/generative-ai-cli)

### Building

| Command | Description |
|---------|-------------|
| `just build` | Validate, generate proto, build binary |
| `just validate` | Validate fragment YAML against JSON schema |
| `just build-scm` | Build only main binary |
| `just build-static` | Build static binaries (stripped, no CGO) |
| `just proto` | Generate protobuf code |

### Testing

| Command | Description |
|---------|-------------|
| `just test` | Run all tests |
| `just test-verbose` | Run tests with verbose output |
| `just test-coverage` | Run tests with coverage report |
| `just test-acceptance` | Run acceptance tests (requires built binary) |
| `just test-container` | Run all tests in Docker (matches CI) |

### Code quality

| Command | Description |
|---------|-------------|
| `just fmt` | Format code |
| `just lint` | Lint (requires [golangci-lint](https://golangci-lint.run/)) |

### Installation

| Command | Description |
|---------|-------------|
| `just install` | Build and install to `~/go/bin` |
| `just install-local` | Build static and install to `~/.local/bin` |
| `just uninstall` | Remove from `~/.local/bin` |
