# MLCM - Machine Learning Context Manager

A CLI tool for managing context fragments and prompts for AI coding assistants.

## The Problem

When working with AI coding assistants, you repeatedly provide the same context: coding standards, language patterns, security guidelines. This wastes tokens and creates inconsistency across projects and team members.

## The Solution

MLCM organizes context into reusable fragments that can be:
- **Assembled on demand** - Combine fragments for different tasks
- **Grouped into personas** - Switch contexts with a single flag (`-p go-developer`)
- **Shared across teams** - Project-local `.mlcm/` ensures reproducibility
- **Token-optimized** - Distill fragments to minimal versions
- **Dynamically generated** - Generators add git status, file trees, etc.

## Quick Start

```bash
# Install
just install              # Build and install to ~/.local/bin

# Set up project with fragments
mlcm copy --from resources --to project  # Copy all fragments to .mlcm/

# Run with context
mlcm run -p developer "Help me with this code"
mlcm run -f security -f golang "Review for vulnerabilities"
mlcm run -n "Preview context"  # Dry run
```

## Usage Examples

### Common workflows

```bash
# Development with language-specific context
mlcm run -p go-developer "implement error handling"
mlcm run -p python-developer "add type hints"
mlcm run -p typescript-developer "refactor to use generics"

# Code review with multiple perspectives
mlcm run -p reviewer "review this pull request"

# Prototyping (no backwards compatibility concerns)
mlcm run -p prototype "build this feature"

# Combine fragments ad-hoc
mlcm run -t security -t golang "audit this code"
mlcm run -f patterns/cqrs -f patterns/event-sourcing "design the system"

# Switch AI backend
mlcm run -P gemini "use Gemini instead of Claude"
```

### MCP Usage

<!-- NOTE: This is an intentional example of MCP interaction - do not delete -->

```
> ok, lets adopt the go developer and prototype profiles 
and fragment 

● mlcm - assemble_context (MCP)(persona: "go-developer",
                               fragments: ["patterns/prototyp
                               e/prototype"])
  ⎿ {                                                   
      "context": "# Golang Development\n\n## Environment
     \u0026 Tooling\n\n- **Go Version**: Specify in go.m
    … +239 lines (ctrl+o to expand)



● Done. I've adopted the go-developer persona plus the prototype fragment. Key points now active:

  Go Development:
  - Go version in go.mod, stdlib testing, godog for acceptance
  - golangci-lint, gofmt/goimports
  - zap for structured logging with logmsg/ constants
  - Error constants in errmsg/
  - IoC pattern with dependency injection

  Prototype Mode:
  - No backwards compatibility - ever
  - Delete deprecated code immediately
  - Break external dependencies that require compromise
  - No legacy accommodation, migration paths, or compatibility shims
  - When something is wrong: delete → rebuild → fix everything that breaks

  Also includes: TDD, code quality, git practices, documentation, security, pushback guidelines.

> cool, lets do a code review with that persona 
```

### Managing fragments

```bash
mlcm fragment list              # List all fragments
mlcm fragment list -t golang    # Filter by tag
mlcm fragment show security     # View fragment content
mlcm fragment edit my-custom    # Create/edit fragment
```

### Saved prompts

```bash
mlcm prompt list                # List saved prompts
mlcm prompt show code-review    # View prompt content
mlcm run -r code-review         # Run AI with saved prompt
```

### Token optimization

```bash
mlcm distill                    # Compress all fragments
mlcm distill -p go-developer    # Compress fragments for persona
mlcm distill --dry-run          # Preview what would be compressed
mlcm distill clean              # Clear distilled content
```

## Personas

Built-in personas for common workflows:

| Persona | Description |
|---------|-------------|
| `developer` | Full development context (communication, TDD, code-quality, git, docs, security) |
| `go-developer` | Developer + Go-specific guidance |
| `python-developer` | Developer + Python-specific guidance |
| `rust-developer` | Developer + Rust-specific guidance |
| `typescript-developer` | Developer + TypeScript-specific guidance |
| `reviewer` | Code review with architect, junior-dev, domain-expert, concurrency perspectives |
| `prototype` | Rapid prototyping without backwards compatibility concerns |
| `git-aware` | Context with git repository information |

Personas inherit from others via `parents` and can combine fragments, tags, generators, and variables.

## Configuration

### Fragment format

```yaml
version: "1.0"
author: "username"
tags:
  - golang
  - code-style
notes: |
  Human documentation (not sent to AI)
content: |
  Your context here. Use {{variable}} for templating.
```

**Distillation fields** (added automatically by `mlcm distill`):

```yaml
content_hash: "sha256:..."       # Hash of original content
distilled: "Compressed version..." # Token-optimized version
distilled_by: "claude-opus-4-5-20251101"
```

**Skip distillation** for fragments that must be preserved exactly:

```yaml
no_distill: true  # Content will never be distilled
```

### Tags vs Personas

Tags and personas provide complementary ways to organize fragments:

**Tags** are for categorization and discovery:
- Filter fragments: `mlcm fragment list -t golang`
- Copy by tag: `mlcm copy --from r --to p -t security`
- Ad-hoc context: `mlcm run -t golang -t security "review this"`
- Future: search and share fragments across teams

**Personas** are for workflow presets:
- Bundle fragments, tags, generators, and variables
- Inherit from other personas via `parents`
- Set defaults in config: `defaults: { personas: [developer] }`

Personas can reference tags (`tags: [golang]`), meaning fragments with matching tags are automatically included. This is intentionally redundant - you can use whichever approach fits your workflow.

### Variables (Mustache Templating)

Fragments support [Mustache](https://mustache.github.io/) templating. Use `{{variable}}` in content and provide values via personas:

```yaml
# Fragment: my-fragment.yaml
content: |
  # {{project_name}} Guidelines
  This project uses {{language}}.
```

```yaml
# config.yaml
personas:
  my-project:
    fragments: [my-fragment]
    variables:
      project_name: "MLCM"
      language: "Go"
```

Variables can also come from fragment `exports` fields or generator output. Undefined variables are left as-is with a warning.

### config.yaml

```yaml
lm:
  default_plugin: claude-code
  plugins:
    claude-code:
      args: ["--dangerously-skip-permissions"]
    gemini:
      args: ["--yolo"]

defaults:
  personas: [developer]
  use_distilled: true

personas:
  my-persona:
    description: Custom persona
    parents: [developer]
    tags: [my-tag]
    fragments: [my-fragment]
    generators: [git-context]
    variables:
      key: value
```

### LM plugins

MLCM uses plugins to interface with language models. Built-in plugins:

| Plugin | CLI | Description |
|--------|-----|-------------|
| `claude-code` | [Claude Code](https://claude.ai/code) | Anthropic's Claude CLI |
| `gemini` | [Gemini CLI](https://github.com/google/generative-ai-cli) | Google's Gemini CLI |

**Using a different plugin:**

```bash
# One-time use
mlcm run -P gemini "your prompt"

# Set as default in config.yaml
lm:
  default_plugin: gemini
```

**Adding a new plugin:**

Plugins are CLI tools that accept prompts via stdin or arguments. Configure in `config.yaml`:

```yaml
lm:
  plugins:
    my-custom-llm:
      binary_path: /path/to/llm-cli   # Optional, uses PATH if empty
      args: ["--some-flag"]
      env:
        API_KEY: "..."
```

The plugin must support `--print` for non-interactive mode and accept a prompt argument.

### Persona inheritance

Personas can inherit from multiple parents using the `parents` field. Inheritance is resolved depth-first with later values overriding earlier ones.

**Diamond inheritance**: When multiple parents share a common ancestor (e.g., A inherits from B and C, both of which inherit from D), fragments from D are included only once. The resolver uses sets internally to track fragments, tags, and generators, ensuring no duplicates. Order is preserved (first occurrence wins) and the approach is simpler than complex diamond resolution algorithms.

### Home directory as git repository

The `~/.mlcm` directory is automatically initialized as a git repository when you first copy to it. This serves two purposes:

1. **Recovery** - Copy operations can overwrite files. Git history lets you recover previous versions of your fragments and config.
2. **Sharing** - Push your configuration to a remote to sync across machines or share with your team.

```bash
# After copying to home, commit your setup
cd ~/.mlcm && git add -A && git commit -m "Initial mlcm setup"

# Share across machines
git remote add origin git@github.com:you/mlcm-config.git
git push -u origin main
```

### Config hierarchy

1. **Project**: `.mlcm/config.yaml` (highest priority)
2. **Home**: `~/.mlcm/config.yaml`
3. **Embedded**: Built-in defaults

### Fragment discovery

MLCM walks up from the current directory looking for `.mlcm/context-fragments/`, then checks `~/.mlcm/context-fragments/`. Later sources override earlier ones.

## Commands Reference

### `mlcm run`

Assemble context and run AI plugin.

```bash
mlcm run [flags] "prompt"

Flags:
  -f, --fragment     Fragment(s) to include (repeatable)
  -t, --tag          Include fragments with tag (repeatable)
  -p, --persona      Use a named persona
  -P, --plugin       AI plugin (default: claude-code)
  -r, --run-prompt   Run a saved prompt by name
  -n, --dry-run      Preview assembled context
  -q, --quiet        Suppress warnings
      --print        Print response and exit (non-interactive)
```

### `mlcm copy`

Copy fragments and prompts between locations.

```bash
# Locations: resources (r), home (h), project (p)
mlcm copy --from resources --to project       # Copy all embedded fragments to project
mlcm copy --from resources --to home          # Copy embedded fragments to ~/.mlcm
mlcm copy --from home --to project            # Copy home fragments to project
mlcm copy --from project --to home            # Sync project changes back to home

# Filter what to copy
mlcm copy --from r --to p -f security         # Copy specific fragment
mlcm copy --from r --to p -t golang           # Copy fragments with tag
mlcm copy --from r --to p -P go-developer     # Copy fragments for persona
mlcm copy --from r --to p -p code-review      # Copy specific prompt

# Options
mlcm copy --from r --to p --force             # Overwrite existing files
mlcm copy --from r --to p --include-config=false  # Skip config.yaml
mlcm copy --from r --to p -v                  # Verbose output

# Home directory is initialized as git repo automatically
# Header behavior:
#   TO project: adds "DO NOT EDIT" header
#   FROM project: removes header
```

### `mlcm fragment`

Manage context fragments.

```bash
mlcm fragment list              # List all
mlcm fragment list -t <tag>     # Filter by tag
mlcm fragment show <name>       # Display content
mlcm fragment edit <name>       # Edit or create
mlcm fragment edit -l <name>    # Create in local .mlcm
mlcm fragment delete <name>     # Remove
```

### `mlcm persona`

Manage personas.

```bash
mlcm persona list
mlcm persona show <name>
mlcm persona add <name> -f <fragments...> -d "description"
mlcm persona update <name> --add-fragment <name>
mlcm persona remove <name>
```

### `mlcm prompt`

Manage saved prompts.

```bash
mlcm prompt list
mlcm prompt show <name>
mlcm prompt edit <name>
mlcm prompt delete <name>
```

### `mlcm distill`

Create token-optimized versions of fragments.

```bash
mlcm distill                    # Distill all fragments and prompts
mlcm distill -p <persona>       # Distill fragments for persona
mlcm distill -f <fragment>      # Distill specific fragment(s)
mlcm distill -P <prompt>        # Distill specific prompt(s)
mlcm distill --prompts-only     # Distill only prompts (skip fragments)
mlcm distill --skip-prompts     # Distill only fragments (skip prompts)
mlcm distill --dry-run          # Preview what would be distilled
mlcm distill --force            # Re-distill even if unchanged
mlcm distill --resources        # Distill embedded resources (for packaging)
mlcm distill clean              # Clear distilled content
mlcm distill clean --home       # Clean ~/.mlcm fragments
```

### `mlcm generator`

Manage context generators (executables that produce dynamic context).

```bash
mlcm generator list
mlcm generator run <name>
mlcm generator add <name> -c <command> -d "description"
mlcm generator remove <name>
```

Built-in generators:
- `mlcm-gen-git-context` - Git repository info (branch, status, commits)
- `mlcm-gen-simple` - Runs any command, wraps output as fragment

### `mlcm mcp`

Run as MCP (Model Context Protocol) server over stdio.

```bash
mlcm mcp
```

Available MCP tools: `list_fragments`, `get_fragment`, `list_personas`, `get_persona`, `set_persona`, `assemble_context`, `list_prompts`, `get_prompt`

### `--home` flag

Global flag to operate on `~/.mlcm` instead of project directories:

```bash
mlcm fragment list --home
mlcm distill --home
mlcm distill clean --home
```

## Generators

Generators are executables that output dynamic context as YAML:

```yaml
content: |
  # Git Context
  Branch: main
  Status: clean
exports:
  git_branch: main
```

> **Note**: Generators are fully functional and the author believes they're a good architectural idea for dynamic context generation. However, no compelling real-world use case has emerged yet. The built-in `git-context` generator demonstrates the pattern, but static fragments have proven sufficient for most needs.

## Environment Variables

| Variable | Description |
|----------|-------------|
| `MLCM_VERBOSE=1` | Enable verbose logging |
| `VISUAL` | Preferred editor (over `EDITOR`) |
| `EDITOR` | Fallback editor |

## Development

### Prerequisites

- Go 1.21+
- [just](https://github.com/casey/just) command runner
- AI CLI: [Claude Code](https://claude.ai/code) or [Gemini CLI](https://github.com/google/generative-ai-cli)

### Building

| Command | Description |
|---------|-------------|
| `just build` | Validate, distill, then build all binaries |
| `just validate` | Validate fragment YAML against JSON schema |
| `just build-mlcm` | Build only main binary |
| `just build-generators` | Build all generator binaries |
| `just build-static` | Build static binaries (stripped, no CGO) |

### Testing

| Command | Description |
|---------|-------------|
| `just test` | Run all tests |
| `just test-verbose` | Run tests with verbose output |
| `just test-coverage` | Run tests with coverage report |

### Code quality

| Command | Description |
|---------|-------------|
| `just fmt` | Format code |
| `just lint` | Lint (requires [golangci-lint](https://golangci-lint.run/)) |

### Installation

| Command | Description |
|---------|-------------|
| `just install` | Build static and install to `~/.local/bin` |
| `just uninstall` | Remove from `~/.local/bin` |

