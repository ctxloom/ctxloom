# ctxloom - Your context, always in the right thread.

A CLI tool for managing context fragments and prompts for AI coding assistants.

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
# Install
just install              # Build and install to ~/go/bin

# Initialize a project
ctxloom init                  # Create .ctxloom directory in current project

# Install content from a remote
ctxloom fragment install ctxloom-default/core

# Search for content
ctxloom search -t golang      # Find fragments by tag
ctxloom fragment list         # List all fragments

# Run with your context
ctxloom run -p developer "Help me with this code"
ctxloom run -f core#fragments/tdd "Review this PR"
ctxloom run -n                # Preview what context would be sent
```

See the [Quick Start Guide](https://ctxloom.dev/getting-started/quickstart) for more.

## Key Concepts

| Concept | Description |
|---------|-------------|
| **Bundle** | A YAML file containing related fragments, prompts, and MCP server configs |
| **Fragment** | A reusable context snippet within a bundle (coding standards, patterns, etc.) |
| **Prompt** | A saved prompt template within a bundle |
| **Profile** | A named configuration that references bundles, tags, and variables |
| **Remote** | A Git repository for sharing bundles and profiles |

Learn more: [Concepts](https://ctxloom.dev/concepts/bundles)

## Commands

| Command | Description |
|---------|-------------|
| `ctxloom run` | Assemble context and run AI |
| `ctxloom init` | Initialize .ctxloom directory |
| `ctxloom search` | Search fragments and prompts |
| `ctxloom fragment` | Manage fragments (list, show, create, edit, install) |
| `ctxloom prompt` | Manage prompts |
| `ctxloom profile` | Manage profiles |
| `ctxloom remote` | Manage remotes (add, sync, search, browse) |
| `ctxloom mcp` | Run MCP server or manage MCP configs |
| `ctxloom config` | Show or modify configuration |

See [CLI Reference](docs/cli-reference.md) for complete documentation.

## Documentation

- [Installation](https://ctxloom.dev/getting-started/installation)
- [Quick Start](https://ctxloom.dev/getting-started/quickstart)
- [Configuration Guide](https://ctxloom.dev/guides/configuration)
- [MCP Server Setup](https://ctxloom.dev/guides/mcp-server)
- [CLI Reference](https://ctxloom.dev/reference/cli)
- [Contributing](https://ctxloom.dev/contributing)

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
just test           # Run all tests
just lint           # Lint code
just install        # Build and install to ~/go/bin
```

See [Contributing](https://ctxloom.dev/contributing) for full development guide.
