# SCM - Sophisticated Context Manager

A CLI tool for managing context fragments and prompts for AI coding assistants.

**Documentation:** [scm.abbitt.me](https://scm.abbitt.me)

## The Problem

When working with AI coding assistants, you repeatedly provide the same context: coding standards, language patterns, security guidelines. This wastes tokens and creates inconsistency across projects and team members.

## The Solution

SCM organizes context into reusable **bundles** that can be:
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
scm init                  # Create .scm directory in current project

# Create your first bundle
scm bundle create my-standards

# Run with your context
scm run -f my-standards "Help me with this code"
scm run -n                # Preview what context would be sent
```

See the [Quick Start Guide](https://scm.abbitt.me/getting-started/quickstart) for more.

## Key Concepts

| Concept | Description |
|---------|-------------|
| **Bundle** | A YAML file containing related fragments, prompts, and MCP server configs |
| **Fragment** | A reusable context snippet within a bundle (coding standards, patterns, etc.) |
| **Prompt** | A saved prompt template within a bundle |
| **Profile** | A named configuration that references bundles, tags, and variables |
| **Remote** | A Git repository for sharing bundles and profiles |

Learn more: [Concepts](https://scm.abbitt.me/concepts/bundles)

## Documentation

- [Installation](https://scm.abbitt.me/getting-started/installation)
- [Quick Start](https://scm.abbitt.me/getting-started/quickstart)
- [Configuration Guide](https://scm.abbitt.me/guides/configuration)
- [MCP Server Setup](https://scm.abbitt.me/guides/mcp-server)
- [CLI Reference](https://scm.abbitt.me/reference/cli)
- [Contributing](https://scm.abbitt.me/contributing)

## Development

### Prerequisites

- Go 1.21+
- [just](https://github.com/casey/just) command runner
- [protoc](https://grpc.io/docs/protoc-installation/) for plugin protocol

### Building

```bash
just build          # Validate, generate proto, build binary
just test           # Run all tests
just lint           # Lint code
just install        # Build and install to ~/go/bin
```

See [Contributing](https://scm.abbitt.me/contributing) for full development guide.
