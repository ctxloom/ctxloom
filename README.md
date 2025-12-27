# SCM - Sophisticated Context Management

A CLI tool for managing context fragments and prompts for AI coding assistants.

## The Problem

When working with AI coding assistants, you repeatedly provide the same context: coding standards, language patterns, security guidelines. This wastes tokens and creates inconsistency across projects and team members.

## The Solution

SCM organizes context into reusable fragments that can be:
- **Assembled on demand** - Combine fragments for different tasks
- **Grouped into personas** - Switch contexts with a single flag (`-p go-developer`)
- **Shared across teams** - Project-local `.scm/` ensures reproducibility
- **Token-optimized** - Distill fragments to minimal versions
- **Dynamically generated** - Generators add git status, file trees, etc.

## Quick Start

```bash
# Install
just install              # Build and install to ~/.local/bin

# Works immediately with built-in fragments
scm run -p developer "Help me with this code"
scm run -n                # Preview what context would be sent

# Optional: copy fragments to customize
scm copy --from resources --to home     # Copy to ~/.scm/ for personal use
scm copy --from resources --to project  # Copy to .scm/ for team sharing
```

## Usage Examples

### Common workflows

```bash
# Development with language-specific context
scm run -p go-developer "implement error handling"
scm run -p python-developer "add type hints"
scm run -p typescript-developer "refactor to use generics"

# Code review with multiple perspectives
scm run -p reviewer "review this pull request"

# Prototyping (no backwards compatibility concerns)
scm run -p prototype "build this feature"

# Combine fragments ad-hoc
scm run -t security -t golang "audit this code"
scm run -f patterns/cqrs -f patterns/event-sourcing "design the system"

# Switch AI backend
scm run -P gemini "use Gemini instead of Claude"
```

### MCP Usage

<!-- NOTE: This is an intentional example of MCP interaction - do not delete -->

```
> ok, lets adopt the go developer and prototype profiles
and fragment

● scm - assemble_context (MCP)(persona: "go-developer",
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
scm fragment list              # List all fragments
scm fragment list -t golang    # Filter by tag
scm fragment show security     # View fragment content
scm fragment edit my-custom    # Create/edit fragment
```

### Saved prompts

```bash
scm prompt list                # List saved prompts
scm prompt show code-review    # View prompt content
scm run -r code-review         # Run AI with saved prompt
```

### Token optimization

```bash
scm distill                    # Compress all fragments and prompts
scm distill -p go-developer    # Compress fragments for persona
scm distill --dry-run          # Preview what would be compressed
scm distill clean              # Clear distilled content
```

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

**Distillation fields** (added automatically by `scm distill`):

```yaml
content_hash: "sha256:..."       # Hash of original content
distilled: "Compressed version..." # Token-optimized version
distilled_by: "claude-opus-4-5-20251101"
```

**Skip distillation** for fragments that must be preserved exactly:

```yaml
no_distill: true  # Content will never be distilled
```

### Personas

Built-in personas for common workflows:

| Persona | Description |
|---------|-------------|
| `developer` | Full development context (communication, TDD, code-quality, git, docs, security) |
| `go-developer` | Developer + Go-specific guidance |
| `python-developer` | Developer + Python-specific guidance |
| `rust-developer` | Developer + Rust-specific guidance |
| `typescript-developer` | Developer + TypeScript-specific guidance |
| `reviewer` | Code review with architect, domain-expert, concurrency, performance, standards perspectives |
| `prototype` | Rapid prototyping without backwards compatibility concerns |

Personas inherit from others via `parents` and can combine fragments, tags, generators, and variables.

### Tags vs Personas

Tags and personas provide complementary ways to organize fragments:

**Tags** are for categorization and discovery:
- Filter fragments: `scm fragment list -t golang`
- Copy by tag: `scm copy --from r --to p -t security`
- Ad-hoc context: `scm run -t golang -t security "review this"`

**Personas** are for workflow presets:
- Bundle fragments, tags, generators, and variables
- Inherit from other personas via `parents`
- Set defaults in config: `defaults: { personas: [developer] }`

Personas can reference tags (`tags: [golang]`), meaning fragments with matching tags are automatically included.

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
      project_name: "SCM"
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
    generators: [my-generator]
    variables:
      key: value
```

### LM plugins

SCM uses plugins to interface with language models. Built-in plugins:

| Plugin | CLI | Description |
|--------|-----|-------------|
| `claude-code` | [Claude Code](https://claude.ai/code) | Anthropic's Claude CLI |
| `gemini` | [Gemini CLI](https://github.com/google/generative-ai-cli) | Google's Gemini CLI |

**Using a different plugin:**

```bash
# One-time use
scm run -P gemini "your prompt"

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

**Diamond inheritance**: When multiple parents share a common ancestor (e.g., A inherits from B and C, both of which inherit from D), fragments from D are included only once. Order is preserved (first occurrence wins).

### Home directory as git repository

The `~/.scm` directory is automatically initialized as a git repository when you first copy to it. This enables:

1. **Recovery** - Git history lets you recover previous versions of fragments and config
2. **Sharing** - Push to a remote to sync across machines or share with your team

```bash
# After copying to home, commit your setup
cd ~/.scm && git add -A && git commit -m "Initial scm setup"

# Share across machines
git remote add origin git@github.com:you/scm-config.git
git push -u origin main
```

### Config hierarchy

SCM uses a single source (no merging):

1. **Project**: `.scm/` at git repository root (if exists)
2. **Home**: `~/.scm/` (fallback if no project .scm)
3. **Embedded**: Built-in resources (fallback if no home .scm)

When in a project with `.scm/`, only that project's config and fragments are used. This ensures reproducibility across team members.

**Embedded mode behavior**: When no `.scm/` directory exists, SCM uses built-in fragments and config. Read operations (`list`, `show`, `run`) work normally. Write operations behave as follows:
- `fragment edit` / `prompt edit` → creates new file in `~/.scm/`
- `fragment delete` / `prompt delete` → errors (use `scm copy` first)
- `distill` → errors (use `--resources` for packaging)

## Commands Reference

### `scm run`

Assemble context and run AI plugin.

```bash
scm run [flags] "prompt"

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

### `scm copy`

Copy fragments and prompts between locations.

```bash
# Locations: resources (r), home (h), project (p)
scm copy --from resources --to project       # Copy all embedded fragments to project
scm copy --from resources --to home          # Copy embedded fragments to ~/.scm
scm copy --from home --to project            # Copy home fragments to project
scm copy --from project --to home            # Sync project changes back to home

# Filter what to copy
scm copy --from r --to p -f security         # Copy specific fragment
scm copy --from r --to p -t golang           # Copy fragments with tag
scm copy --from r --to p -P go-developer     # Copy fragments for persona
scm copy --from r --to p -p code-review      # Copy specific prompt

# Options
scm copy --from r --to p --force             # Overwrite existing files
scm copy --from r --to p --include-config=false  # Skip config.yaml
scm copy --from r --to p -v                  # Verbose output

# Home directory is initialized as git repo automatically
# Header behavior:
#   TO project: adds "DO NOT EDIT" header
#   FROM project: removes header
```

**Recommended workflow**: Edit fragments in `~/.scm/`, then use `scm copy --from home --to project` to copy them into your project. The copy command adds a "DO NOT EDIT" header to project files, signaling that changes should be made in home and copied over.

### `scm fragment`

Manage context fragments.

```bash
scm fragment list              # List all
scm fragment list -t <tag>     # Filter by tag
scm fragment show <name>       # Display content
scm fragment edit <name>       # Edit or create
scm fragment edit -l <name>    # Create in local .scm
scm fragment delete <name>     # Remove
```

### `scm persona`

Manage personas.

```bash
scm persona list
scm persona show <name>
scm persona add <name> -f <fragments...> -d "description"
scm persona update <name> --add-fragment <name>
scm persona remove <name>
```

### `scm prompt`

Manage saved prompts.

```bash
scm prompt list
scm prompt show <name>
scm prompt edit <name>
scm prompt delete <name>
```

### `scm distill`

Create token-optimized versions of fragments.

```bash
scm distill                    # Distill all fragments and prompts
scm distill -p <persona>       # Distill fragments for persona
scm distill -f <fragment>      # Distill specific fragment(s)
scm distill -P <prompt>        # Distill specific prompt(s)
scm distill --prompts-only     # Distill only prompts (skip fragments)
scm distill --skip-prompts     # Distill only fragments (skip prompts)
scm distill --dry-run          # Preview what would be distilled
scm distill --force            # Re-distill even if unchanged
scm distill --resources        # Distill embedded resources (for packaging)
scm distill clean              # Clear distilled content
```

### `scm generator`

Manage context generators (executables that produce dynamic context).

```bash
scm generator list
scm generator run <name>
scm generator add <name> -c <command> -d "description"
scm generator remove <name>
```

Built-in generators:
- `scm-gen-simple` - Runs any command, wraps output as fragment

### `scm mcp`

Run as MCP (Model Context Protocol) server over stdio.

```bash
scm mcp
```

Available MCP tools: `list_fragments`, `get_fragment`, `list_personas`, `get_persona`, `set_persona`, `assemble_context`, `list_prompts`, `get_prompt`

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

> **Note**: Generators are fully functional and provide a good architectural pattern for dynamic context generation. The `scm-gen-simple` wrapper makes it easy to create generators from any command. Static fragments have proven sufficient for most needs, but generators are available when dynamic context is required.

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
- AI CLI: [Claude Code](https://claude.ai/code) or [Gemini CLI](https://github.com/google/generative-ai-cli)

### Building

| Command | Description |
|---------|-------------|
| `just build` | Validate, distill, then build all binaries |
| `just validate` | Validate fragment YAML against JSON schema |
| `just build-scm` | Build only main binary |
| `just build-generators` | Build all generator binaries |
| `just build-static` | Build static binaries (stripped, no CGO) |

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
| `just install` | Build static and install to `~/.local/bin` |
| `just uninstall` | Remove from `~/.local/bin` |
