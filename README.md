# MLCM - Machine Learning Context Manager

A CLI tool for managing context fragments and prompts for AI interactions. MLCM assembles context from project-local sources and passes it to AI backends like Claude Code.

## Installation

```bash
just install-local    # Build static binaries and install to ~/.local/bin
just uninstall        # Remove binaries from ~/.local/bin
```

Or install to GOPATH/bin:

```bash
just install          # Install to $GOPATH/bin (requires Go)
```

## Quick Start

```bash
mlcm init                                  # Initialize .mlcm in current directory
mlcm run -f my-fragment "Help me"          # Run with context fragment
mlcm run -p developer "Review this code"   # Run with persona
mlcm run -f my-fragment -n "Preview"       # Dry run to preview context
```

## Commands

### `mlcm init`

Initialize the `.mlcm` directory structure in the current directory.

```bash
mlcm init                        # Create .mlcm with all fragments
mlcm init --skip-fragments       # Skip embedded fragments (copy ~/.mlcm only)
mlcm init --skip-fragments=local # Skip ~/.mlcm fragments (copy embedded only)
mlcm init --skip-fragments=both  # Skip all fragment copying
```

Creates:
- `config.yaml` - Configuration
- `context-fragments/` - Context fragment files
- `prompts/` - Prompt templates

Fragment sources (in order, later overwrites earlier):
1. Embedded default fragments
2. `~/.mlcm/context-fragments/` (your personal fragments)

### Persisting Your Fragments

Your personal fragments in `~/.mlcm/` are valuable configuration. Consider storing this directory in git for:

- **Backup** - Never lose your carefully crafted fragments
- **Sync** - Share fragments across machines
- **History** - Track changes over time

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
- `-P, --plugin` - AI plugin to use
- `-n, --dry-run` - Preview assembled context
- `-q, --quiet` - Suppress warnings

Example with tags:
```bash
mlcm run -t security -t review "Check for vulnerabilities"
```

### `mlcm fragment`

Manage context fragments.

```bash
mlcm fragment list           # List all fragments
mlcm fragment edit <name>    # Edit or create
mlcm fragment show <name>    # Display content
mlcm fragment delete <name>  # Remove
```

Flags for `edit`:
- `-l, --local` - Create in local .mlcm directory

### `mlcm prompt`

Manage saved prompts.

> **Note:** Saved prompts overlap with Claude Code's slash commands. However, MLCM prompts integrate with context fragments and support variable substitution, which may be useful for complex workflows.

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
mlcm persona add <name> -f <fragments...> -g <generators...> -d "description"
mlcm persona show <name>
mlcm persona update <name> --add-fragment <name>
mlcm persona remove <name>
```

### `mlcm generator`

Manage context generators - executables that produce dynamic context.

```bash
mlcm generator list
mlcm generator add <name> -c <command> -d "description"
mlcm generator run <name>
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

### Fragment Discovery

MLCM walks up from the current directory looking for `.mlcm/context-fragments/`.

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

## Configuration

Stored in `.mlcm/config.yaml`:

```yaml
ai:
  default_plugin: claude-code
  plugins:
    claude-code:
      args:
        - "--print"
        - "--dangerously-skip-permissions"

editor:
  command: nano

generators:
  git-context:
    command: mlcm-gen-git-context

personas:
  developer:
    tags:
      - golang           # Include all fragments with these tags
      - code-style
    fragments:
      - coding-standards  # Explicit fragments (in addition to tagged)
    generators:
      - git-context

defaults:
  use_distilled: true     # Prefer distilled versions (default)
```

### Editor Priority

1. `editor.command` in config.yaml
2. `VISUAL` environment variable
3. `EDITOR` environment variable
4. `nano` (default)

## Development

All development tasks use [just](https://github.com/casey/just) as a command runner.

### Building

| Command | Description |
|---------|-------------|
| `just build` | Build all binaries (main app + generators) |
| `just build-mlcm` | Build only the main binary |
| `just build-generators` | Build all generator binaries |
| `just build-git-context` | Build git-context generator |
| `just build-simple` | Build simple wrapper generator |
| `just build-static` | Build static binaries (CGO_ENABLED=0, stripped) |
| `just build-verbose` | Build all with verbose output |

### Testing

| Command | Description |
|---------|-------------|
| `just test` | Run all tests |
| `just test-verbose` | Run tests with verbose output |
| `just test-coverage` | Run tests with coverage report |
| `just test-generator` | Test the git-context generator |

### Code Quality

| Command | Description |
|---------|-------------|
| `just fmt` | Format code with `go fmt` |
| `just lint` | Lint code (requires [golangci-lint](https://golangci-lint.run/)) |

### Dependencies

| Command | Description |
|---------|-------------|
| `just deps` | Download dependencies (`go mod download`) |
| `just tidy` | Tidy dependencies (`go mod tidy`) |

### Installation

| Command | Description |
|---------|-------------|
| `just install` | Install to `$GOPATH/bin` |
| `just install-local` | Build static binaries and install to `~/.local/bin` |
| `just uninstall` | Remove binaries from `~/.local/bin` |

### Running

| Command | Description |
|---------|-------------|
| `just run <args>` | Run the CLI via `go run` (e.g., `just run --help`) |
| `just init` | Initialize `.mlcm` directory using built binary |
| `just dry-run <prompt>` | Dry run with test fragments |
| `just help` | Show CLI help |

### Cleanup

| Command | Description |
|---------|-------------|
| `just clean` | Remove build artifacts (`mlcm`, `bin/`, `go clean`) |

## Environment Variables

- `MLCM_VERBOSE=1` - Enable verbose logging