# ctxloom - Sophisticated Context Manager

A CLI tool for managing context fragments and prompts for AI coding assistants.

**Documentation:** [ctxloom.abbitt.me](https://ctxloom.abbitt.me)

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

# Create your first bundle
ctxloom bundle create my-standards

# Run with your context
ctxloom run -f my-standards "Help me with this code"
ctxloom run -n                # Preview what context would be sent
```

See the [Quick Start Guide](https://ctxloom.abbitt.me/getting-started/quickstart) for more.

## Key Concepts

| Concept | Description |
|---------|-------------|
| **Bundle** | A YAML file containing related fragments, prompts, and MCP server configs |
| **Fragment** | A reusable context snippet within a bundle (coding standards, patterns, etc.) |
| **Prompt** | A saved prompt template within a bundle |
| **Profile** | A named configuration that references bundles, tags, and variables |
| **Remote** | A Git repository for sharing bundles and profiles |

Learn more: [Concepts](https://ctxloom.abbitt.me/concepts/bundles)

## Documentation

- [Installation](https://ctxloom.abbitt.me/getting-started/installation)
- [Quick Start](https://ctxloom.abbitt.me/getting-started/quickstart)
- [Configuration Guide](https://ctxloom.abbitt.me/guides/configuration)
- [MCP Server Setup](https://ctxloom.abbitt.me/guides/mcp-server)
- [CLI Reference](https://ctxloom.abbitt.me/reference/cli)
- [Contributing](https://ctxloom.abbitt.me/contributing)

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

See [Contributing](https://ctxloom.abbitt.me/contributing) for full development guide.
