# MLCM - Machine Learning Context Manager

A CLI tool for managing context fragments and prompts for AI interactions. MLCM assembles context from project-local sources and passes it to AI backends like Claude Code or Gemini.

## Why MLCM?

When working with AI coding assistants, you often provide the same context repeatedly: coding standards, language patterns, security guidelines. MLCM solves this by:

- **Organizing context into reusable fragments** - Write once, use everywhere
- **Grouping fragments into personas** - Switch contexts with a single flag
- **Supporting dynamic content** - Generators add git status, file trees, etc.
- **Optimizing for tokens** - Distill fragments to minimal versions
- **Working across projects** - Personal fragments in `~/.mlcm`, project-specific in `.mlcm/`

## Installation

```bash
just install-local    # Build static binaries and install to ~/.local/bin
just uninstall        # Remove binaries from ~/.local/bin
```

Or install to GOPATH/bin:

```bash
just install          # Install to $GOPATH/bin (requires Go)
```

### Prerequisites

- Go 1.21+
- An AI CLI tool: [Claude Code](https://claude.ai/code) or [Gemini CLI](https://github.com/google/generative-ai-cli)

## Quick Start

```bash
mlcm init                                  # Initialize .mlcm at git root (or pwd)
mlcm run -f my-fragment "Help me"          # Run with context fragment
mlcm run -p developer "Review this code"   # Run with persona
mlcm run -f my-fragment -n "Preview"       # Dry run to preview context
```

## Commands

### `mlcm init`

Initialize the `.mlcm` directory structure. Automatically detects git repository root, or falls back to current directory.

```bash
mlcm init                        # Create .mlcm with all fragments
mlcm init go-developer           # Copy only fragments for specific persona(s)
mlcm init --skip-fragments       # Skip embedded fragments (copy ~/.mlcm only)
mlcm init --skip-fragments=local # Skip ~/.mlcm fragments (copy embedded only)
mlcm init --skip-fragments=both  # Skip all fragment copying
mlcm init --from-git <url>       # Clone fragments from a git repository
mlcm init -v                     # Verbose output (list individual files)
```

Creates:
- `config.yaml` - Configuration
- `context-fragments/` - Context fragment files
- `prompts/` - Prompt templates

Fragment sources (in order, later overwrites earlier):
1. Embedded default fragments
2. `~/.mlcm/context-fragments/` (your personal fragments)
3. Git repository (if `--from-git` specified)

#### Initialize Home Directory

```bash
mlcm init home                   # Initialize ~/.mlcm with embedded fragments
mlcm init home go-developer      # Initialize with specific persona(s)
```

### Persisting Your Fragments

Your personal fragments in `~/.mlcm/` are valuable configuration. Consider storing this directory in git:

```bash
cd ~/.mlcm
git init
git add .
git commit -m "Initial context fragments"
git remote add origin <your-repo-url>
git push -u origin main
```

### `mlcm run`

Assemble context and run the AI plugin.

```bash
mlcm run [flags] "your prompt"
```

Flags:
- `-f, --fragment` - Fragment(s) to include (repeatable)
- `-t, --tag` - Include fragments with this tag (repeatable)
- `-p, --persona` - Use a named persona
- `-P, --plugin` - AI plugin to use (default: claude-code)
- `-n, --dry-run` - Preview assembled context without running AI
- `-q, --quiet` - Suppress warnings

Examples:
```bash
mlcm run -p go-developer "review this code"
mlcm run -t security -t review "Check for vulnerabilities"
mlcm run -P gemini "use Gemini instead of Claude"
```

### `mlcm fragment`

Manage context fragments.

```bash
mlcm fragment list           # List all fragments
mlcm fragment list -t golang # List fragments with tag
mlcm fragment edit <name>    # Edit or create
mlcm fragment show <name>    # Display content
mlcm fragment delete <name>  # Remove
```

Flags for `edit`:
- `-l, --local` - Create in local .mlcm directory

### `mlcm prompt`

Manage saved prompts.

> **Note:** Saved prompts overlap with Claude Code's slash commands. However, MLCM prompts integrate with context fragments and support variable substitution.

```bash
mlcm prompt list           # List all prompts
mlcm prompt edit <name>    # Edit or create
mlcm prompt show <name>    # Display content
mlcm prompt delete <name>  # Remove
```

### `mlcm persona`

Manage personas - named collections of fragments, generators, and variables.

```bash
mlcm persona list
mlcm persona show <name>
mlcm persona add <name> -f <fragments...> -g <generators...> -d "description"
mlcm persona update <name> --add-fragment <name>
mlcm persona remove <name>
```

### `mlcm generator`

Manage context generators - executables that produce dynamic context.

```bash
mlcm generator list
mlcm generator run <name>
mlcm generator add <name> -c <command> -d "description"
mlcm generator remove <name>
```

### `mlcm distill`

Create minimal-token versions of fragments and prompts using AI compression.

```bash
mlcm distill                    # Distill all fragments and prompts
mlcm distill -p go-developer    # Distill fragments for a persona
mlcm distill -f style/direct    # Distill specific fragment
mlcm distill --dry-run          # Preview what would be distilled
mlcm distill --force            # Re-distill even if unchanged
mlcm distill clean              # Remove all distilled files and hashes
mlcm distill clean --home       # Clean ~/.mlcm distilled files
```

Distillation creates `.distilled.yaml` files alongside originals and `.sha256` files to track changes. When `use_distilled: true` (default), the distilled versions are preferred.

### `mlcm mcp`

Run as an MCP (Model Context Protocol) server for AI agent integration.

```bash
mlcm mcp                        # Run local MCP server
mlcm mcp --addr host:port       # Connect to remote fragment server
```

**Local tools provided:**
- `list_fragments` - List available fragments
- `get_fragment` - Get fragment content by name
- `list_personas` - List configured personas
- `get_persona` - Get persona configuration
- `assemble_context` - Assemble context from fragments/tags
- `list_prompts` - List saved prompts
- `get_prompt` - Get prompt content

**Remote tools (with `--addr`):**
- `server_list_fragments`, `server_get_fragment`, `server_search_fragments`
- `server_create_fragment`, `server_list_personas`, `server_get_persona`

### `--home` Flag

The global `--home` flag operates on `~/.mlcm` instead of project directories:

```bash
mlcm fragment list --home       # List fragments in ~/.mlcm
mlcm distill --home             # Distill ~/.mlcm fragments
mlcm distill clean --home       # Clean ~/.mlcm distilled files
```

## Context Fragments

YAML files in `context-fragments/` that define context with optional metadata.

```yaml
version: "1.0"
author: "username"
tags:
  - golang
  - code-style
variables:
  - project_name
  - language
content: |
  Your context here. Use {{variable_name}} for template variables.
```

Fields:
- `content` - The actual context (required)
- `tags` - For filtering and persona assembly
- `variables` - Template variables used in content
- `version`, `author` - Optional metadata

### Fragment Organization

```
.mlcm/context-fragments/
├── general/              # General guidance
│   ├── code-quality.yaml
│   ├── communication.yaml
│   ├── git.yaml
│   ├── security.yaml
│   └── tdd.yaml
├── lang/                 # Language-specific
│   ├── golang/
│   ├── python/
│   ├── rust/
│   └── typescript/
├── patterns/             # Design patterns
│   ├── cqrs/
│   └── event-sourcing/
└── personas/             # Review perspectives
    ├── architect/
    ├── junior-dev/
    └── domain-expert/
```

### Fragment Discovery

MLCM walks up from the current directory looking for `.mlcm/context-fragments/`, then checks `~/.mlcm/context-fragments/`. Later sources can override earlier ones.

### Variable Substitution

Fragments can include variables that get substituted at runtime:

```yaml
variables:
  - project_name
  - language
content: |
  # {{project_name}} Guidelines
  This project uses {{language}}.
```

Variables are filled from:
1. Persona `variables` definition
2. Generator output (`VarValues`)
3. Left as-is if not provided

## Personas

Named collections of fragments, tags, generators, and variables.

```yaml
personas:
  go-developer:
    description: Go development with full standards
    tags:
      - golang              # Include all fragments with this tag
    fragments:
      - general/communication
      - general/tdd
      - general/code-quality
    generators:
      - git-context
    variables:
      language: go

  security-reviewer:
    description: Security-focused code review
    tags:
      - security
    fragments:
      - general/security
```

Use personas to quickly switch context:
```bash
mlcm run -p go-developer "implement error handling"
mlcm run -p security-reviewer "audit for injection vulnerabilities"
```

## Generators

Executables that output context fragments dynamically.

### Built-in Generators

- `mlcm-gen-git-context` - Git repository info (branch, status, commits)
- `mlcm-gen-simple` - Runs any command and wraps output as a fragment

### Usage

```bash
mlcm generator add git-context -c mlcm-gen-git-context -d "Git info"
mlcm persona add git-aware -f base-context -g git-context
mlcm run -p git-aware "Review my changes"
```

### Custom Generators

Generators output YAML with optional variable values:

```yaml
content: |
  # Git Context
  Branch: main
  Status: clean
var_values:
  git_branch: main
```

## Configuration

Stored in `.mlcm/config.yaml`:

```yaml
ai:
  default_plugin: claude-code
  plugins:
    claude-code:
      binary_path: ""              # Optional custom path
      args:
        - --dangerously-skip-permissions
      env: {}
    gemini:
      binary_path: ""
      args:
        - --yolo
      env: {}

editor:
  command: vim
  args: []

defaults:
  persona: developer               # Default persona when none specified
  fragments: []                    # Always include these fragments
  generators: []                   # Always run these generators
  use_distilled: true              # Prefer distilled versions

generators:
  git-context:
    description: Git repository information
    command: mlcm-gen-git-context

personas:
  developer:
    description: Full development context
    fragments:
      - general/communication
      - general/tdd
      - general/code-quality
    generators:
      - git-context
```

### Config Hierarchy

1. **Project**: `.mlcm/config.yaml` (highest priority)
2. **Home**: `~/.mlcm/config.yaml`
3. **Embedded**: Built-in defaults

### Editor Priority

1. `editor.command` in config.yaml
2. `VISUAL` environment variable
3. `EDITOR` environment variable
4. `nano` (default)

## AI Plugins

MLCM uses a plugin architecture for AI provider integration.

### Claude Code (Default)

```yaml
ai:
  default_plugin: claude-code
  plugins:
    claude-code:
      binary_path: claude
      args:
        - --dangerously-skip-permissions
```

### Google Gemini

```yaml
ai:
  default_plugin: gemini
  plugins:
    gemini:
      binary_path: gemini
      args:
        - --yolo
```

### Switching Plugins

```bash
mlcm run "prompt"                  # Use default plugin
mlcm run -P gemini "prompt"        # Override for single command
```

## Fragment Server

MLCM includes an optional gRPC server for centralized fragment management.

### Storage Backends

- **MongoDB** - `STORAGE_TYPE=mongodb`
- **DynamoDB** - `STORAGE_TYPE=dynamodb`
- **Firestore** - `STORAGE_TYPE=firestore`

### Deployment

```bash
just server-run                    # Run locally
just server-docker                 # Build Docker image
just server-deploy-cloudrun        # Deploy to Cloud Run
just lambda-build                  # Build AWS Lambda package
```

## Development

All development tasks use [just](https://github.com/casey/just) as a command runner.

### Building

| Command | Description |
|---------|-------------|
| `just build` | Build all binaries (main app + generators) |
| `just build-mlcm` | Build only the main binary |
| `just build-generators` | Build all generator binaries |
| `just build-static` | Build static binaries (CGO_ENABLED=0, stripped) |

### Testing

| Command | Description |
|---------|-------------|
| `just test` | Run all tests |
| `just test-verbose` | Run tests with verbose output |
| `just test-coverage` | Run tests with coverage report |

### Code Quality

| Command | Description |
|---------|-------------|
| `just fmt` | Format code with `go fmt` |
| `just lint` | Lint code (requires [golangci-lint](https://golangci-lint.run/)) |

### Installation

| Command | Description |
|---------|-------------|
| `just install` | Install to `$GOPATH/bin` |
| `just install-local` | Build static binaries and install to `~/.local/bin` |
| `just uninstall` | Remove binaries from `~/.local/bin` |

### Server

| Command | Description |
|---------|-------------|
| `just server-build` | Build server binary |
| `just server-run` | Run server locally |
| `just server-docker` | Build Docker image |
| `just lambda-build` | Build AWS Lambda package |

### Cleanup

| Command | Description |
|---------|-------------|
| `just clean` | Remove build artifacts |

## Project Structure

```
├── cmd/                    # CLI commands
│   ├── root.go             # Root command, --home flag
│   ├── run.go              # mlcm run
│   ├── init.go             # mlcm init
│   ├── fragment.go         # mlcm fragment
│   ├── persona.go          # mlcm persona
│   ├── distill.go          # mlcm distill
│   ├── mcp.go              # mlcm mcp
│   └── generators/         # Built-in generators
├── internal/
│   ├── config/             # Configuration loading
│   ├── fragments/          # Fragment parsing
│   ├── ai/                 # AI plugin system
│   │   ├── claudecode/     # Claude Code plugin
│   │   └── gemini/         # Gemini plugin
│   └── ...
├── resources/              # Embedded defaults
│   ├── context-fragments/  # Default fragments
│   ├── prompts/            # Default prompts
│   └── config.yaml         # Default config
└── server/                 # gRPC fragment server
    ├── proto/              # Protocol buffers
    ├── service/            # gRPC service
    ├── storage/            # Storage backends
    └── cmd/                # Server entry points
```

## Environment Variables

| Variable | Description |
|----------|-------------|
| `MLCM_VERBOSE=1` | Enable verbose logging |
| `EDITOR` | Fallback editor |
| `VISUAL` | Preferred over `EDITOR` |
| `STORAGE_TYPE` | Server storage backend |
| `MONGODB_URI` | MongoDB connection string |
| `AWS_REGION` | AWS region for DynamoDB |

## License

[Add your license here]
