# MLCM - Machine Learning Context Manager

A CLI tool for managing context fragments and prompts for AI interactions. MLCM assembles context from project-local sources and passes it to AI backends like Claude Code.

## Installation

```bash
just build         # Build main app and generators
just install       # Install to GOPATH/bin
```

## Quick Start

```bash
mlcm init --local                          # Initialize .mlcm in current project
mlcm run -f my-fragment "Help me"          # Run with context fragment
mlcm run -p developer "Review this code"   # Run with persona
mlcm run -f my-fragment -n "Preview"       # Dry run to preview context
```

## Commands

### `mlcm init`

Initialize the `.mlcm` directory structure.

```bash
mlcm init           # Create ~/.mlcm (template directory)
mlcm init --local   # Create .mlcm in current project directory
```

Creates:
- `config.yaml` - Configuration
- `context-fragments/` - Context fragment files
- `prompts/` - Prompt templates

### `mlcm run`

Assemble context and run the AI plugin.

```bash
mlcm run [flags] "your prompt"
```

Flags:
- `-f, --fragment` - Fragment(s) to include (repeatable)
- `-p, --persona` - Use a named persona
- `-P, --plugin` - AI plugin to use
- `-n, --dry-run` - Preview assembled context
- `-q, --quiet` - Suppress warnings

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

## Context Fragments

Markdown files in `context-fragments/` that define context and variables.

```markdown
## Context

Your context here. Use {{variable_name}} for template variables.

## Variables

```yaml
variable_name: value
```
```

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
    fragments:
      - coding-standards
    generators:
      - git-context
```

### Editor Priority

1. `editor.command` in config.yaml
2. `VISUAL` environment variable
3. `EDITOR` environment variable
4. `nano` (default)

## Development

```bash
just test          # Run tests
just fmt           # Format code
just lint          # Lint code
just clean         # Clean build artifacts
```

## Environment Variables

- `MLCM_VERBOSE=1` - Enable verbose logging