# ctxloom - Your context, always in the right thread.

A CLI tool for managing context fragments and commands for AI coding assistants.

**Documentation:** [ctxloom.dev](https://ctxloom.dev)

## The Problem

When working with AI coding assistants, you repeatedly provide the same context: coding standards, language patterns, security guidelines. This wastes tokens and creates inconsistency across projects and team members.

## The Solution

ctxloom organizes context into reusable **bundles** that can be:
- **Assembled on demand** - Combine bundles and fragments for different tasks
- **Grouped into profiles** - Switch contexts with a single flag (`-p developer`)
- **Shared across teams** - Pull bundles from remote repositories (GitHub/GitLab)
- **Token-optimized** - Distill content to minimal versions

> **Disclaimer**: This is a pre-release project. It works and is in active use, but architectural improvements and refactoring are ongoing.

## Quick Start

```bash
# Install — macOS (Homebrew): ctxloom + companions (taskloom, ltk)
brew install ctxloom/tap/{ctxloom-full,taskloom,ltk}   # ctxloom instead of ctxloom-full for the lighter build

# Install — Linux / Windows (script): installs companions too (--no-companions to skip)
# Note: script installs are unsigned binaries — macOS/Windows may need a trust
# step; see https://ctxloom.dev/getting-started/binary-trust. On macOS, pass
# `--brew` (… | bash -s -- --brew) to delegate to Homebrew and skip that entirely.
curl -fsSL https://raw.githubusercontent.com/ctxloom/ctxloom/main/scripts/install.sh | bash

# Initialize a project
ctxloom init                  # Create .ctxloom directory in current project

# Reference remote content from a local profile, then pull it
ctxloom profile create developer -b ctxloom-default/core
ctxloom deps pull           # Fetch referenced bundles and update the lockfile

# Search for content
ctxloom search -t golang      # Find fragments by tag
ctxloom fragment list         # List all fragments
ctxloom command list          # List all commands

# Run with your context
ctxloom run -p developer "Help me with this code"
ctxloom run -f core#fragments/tdd "Review this PR"
ctxloom run -n                # Preview what context would be sent
```

See the [Quick Start Guide](https://ctxloom.dev/getting-started/quickstart) for more.

## Key Concepts

| Concept | Description |
|---------|-------------|
| **Bundle** | A YAML file containing related fragments, commands, and MCP server configs |
| **Fragment** | A reusable context snippet within a bundle (coding standards, patterns, etc.) |
| **Command** | A saved prompt template within a bundle, exported to the engine as a slash command |
| **Profile** | A named configuration that references bundles, tags, and variables |
| **Agent** | A named binding of an LLM engine to the profiles it runs with (`ctxloom run --agent`) |
| **Remote** | A Git repository for sharing bundles and profiles |

Learn more: [Concepts](https://ctxloom.dev/concepts/bundles)

## Commands

| Command | Description |
|---------|-------------|
| `ctxloom run` | Assemble context and run AI |
| `ctxloom init` | Initialize a new .ctxloom directory |
| `ctxloom search` | Search content across local and remote sources |
| `ctxloom fragment` | Manage context fragments |
| `ctxloom command` | Manage commands |
| `ctxloom profile` | Manage profiles (named fragment collections) |
| `ctxloom agent` | Inspect local agents (engine↔profile bindings) |
| `ctxloom remote` | Manage remotes and discover content |
| `ctxloom review` | Review pending items: accept or reject what the agent may see |
| `ctxloom trust` | Accept an item's current content (fragment, command, MCP server, or hook) |
| `ctxloom trust reject` | Reject an item so it is withheld from the agent |
| `ctxloom session` | Browse and manage harp-named sessions |
| `ctxloom memory` | Manage session memory (external compaction) |
| `ctxloom mcp` | List configured MCP servers (`ctxloom mcp serve` runs ctxloom as one) |
| `ctxloom acp` | Serve ctxloom as an Agent Client Protocol agent (stdio) |
| `ctxloom manage` | Install and manage ctxloom's project harness |
| `ctxloom container` | Manage agent container images |
| `ctxloom container tooling` | Agent-image tooling declarations from trusted bundles |
| `ctxloom llm` | Manage LLM backends |
| `ctxloom version` | Print the version number |

Every command carries its own help (`ctxloom <command> --help`). The generated
per-command reference — flags, arguments, examples — is at
[ctxloom.dev/reference/cli](https://ctxloom.dev/reference/cli/), and release
archives ship man pages (`man ctxloom`).

## Documentation

- [Installation](https://ctxloom.dev/getting-started/installation)
- [Quick Start](https://ctxloom.dev/getting-started/quickstart)
- [Configuration Guide](https://ctxloom.dev/guides/configuration)
- [MCP Server Setup](https://ctxloom.dev/guides/mcp-server)
- [CLI Reference](https://ctxloom.dev/reference/cli)
- [Environment Variables](docs/environment.md)
- [Contributing](https://ctxloom.dev/contributing)

## Bundled companions

ctxloom ships with two standalone companion tools, built from this repo
(`cmd/taskloom`, `cmd/ltk`) and delivered by the install script and brew (each
also installs on its own). ctxloom's built-in bundles wire them into your agent
**when the binary is on PATH** — a missing companion degrades to a one-line
warning, never a broken session:

- **taskloom** — per-project task tracking: an append-only task log with a CLI
  and an MCP server (`task_list`/`task_add`/`task_set_status`/`task_edit`).
  Standalone use: `taskloom manage install` registers it with Claude Code,
  Antigravity, or Codex directly.
- **ltk** — a pre-tool hook that redirects commands you'd rather the agent not
  run (e.g. `go test` → "use the task runner" → the agent retries `just test`).
  ctxloom registers the hook; rules are opt-in per project via
  `.ltk/config.yaml` (without one, ltk allows everything). Standalone use:
  `ltk manage install`. Rule model: [docs/ltk/RULES.md](docs/ltk/RULES.md).

What works with what:

| Installed | You get |
|---|---|
| ctxloom only | Context assembly, profiles, remotes — task tools and command redirects disabled (warned once) |
| + taskloom | `task_*` MCP tools in every backend, `taskloom` CLI, `ctxloom run --seed-task` |
| + ltk | Pre-tool command redirects per project rules |
| taskloom/ltk without ctxloom | Each fully works standalone; self-register with `<tool> manage install` |

Opt out at install time: `--no-taskloom`, `--no-ltk`, `--no-companions`
(script) or just don't `brew install` them. Check wiring anytime:
`ctxloom manage check`.

## Development

### Prerequisites

- Go 1.21+
- [just](https://github.com/casey/just) command runner
- [protoc](https://grpc.io/docs/protoc-installation/) for plugin protocol

### Building

Two build variants are available:

| Build | Command | Size | Description |
|-------|---------|------|-------------|
| **Standard** | `just build` | ~27MB | All features except tree-sitter code compression |
| **Full** | `just build-ctxloom-full` | ~31MB | Includes tree-sitter AST compression (requires CGO) |

Most users should use the standard build. The full build adds tree-sitter for AST-aware code compression when distilling fragments.

```bash
just build          # Standard build (recommended)
just build-ctxloom-full # Full build with tree-sitter
just test           # Run unit + race tests (integration compiles but does NOT run — see below)
just lint           # Lint code
just install        # Build and install to ~/go/bin
```

**What `just test` actually covers:** exit 0 means the unit/race suites are
green and `tests/integration/...` (the `-tags integration` build fence)
*compiles* cleanly. It does not mean those integration tests ran — neither
`go test` invocation inside the `test` recipe passes `-tags integration`, so
they're skipped by construction; only `go vet -tags integration` runs
against them, as a cheap rot gate against the tag-gated files bit-rotting
unseen. For real coverage of the CLI/bundle/path surface and cross-surface
journeys, run these explicitly:

```bash
just test-integration  # Actually executes tests/integration/... (requires the built binary)
just test-acceptance   # Full-stack godog journeys (CLI + MCP + files)
```

See [Contributing](https://ctxloom.dev/contributing) for full development guide.
