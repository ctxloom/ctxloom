---
sidebar_position: 1
---

# Installation

Choose the installation method that works best for you.

## Homebrew (Recommended)

The easiest way to install on macOS or Linux:

```bash
brew install benjaminabbitt/tap/scm
```

## Go Install

If you have Go 1.21+ installed:

```bash
go install github.com/benjaminabbitt/scm@latest
```

Make sure `~/go/bin` is in your PATH:

```bash
export PATH=$PATH:$(go env GOPATH)/bin
```

## Download Binary

Download precompiled binaries from the [releases page](https://github.com/benjaminabbitt/scm/releases).

### macOS

```bash
# Apple Silicon (M1/M2/M3)
curl -L https://github.com/benjaminabbitt/scm/releases/latest/download/scm_0.0.8_darwin_arm64.tar.gz | tar xz
sudo mv scm /usr/local/bin/

# Intel
curl -L https://github.com/benjaminabbitt/scm/releases/latest/download/scm_0.0.8_darwin_amd64.tar.gz | tar xz
sudo mv scm /usr/local/bin/
```

### Linux

```bash
# x86_64
curl -L https://github.com/benjaminabbitt/scm/releases/latest/download/scm_0.0.8_linux_amd64.tar.gz | tar xz
sudo mv scm /usr/local/bin/

# ARM64
curl -L https://github.com/benjaminabbitt/scm/releases/latest/download/scm_0.0.8_linux_arm64.tar.gz | tar xz
sudo mv scm /usr/local/bin/
```

## Build from Source

For development or to get the latest unreleased features.

### Which Build?

SCM offers two build variants:

| Build | Size | CGO | Best For |
|-------|------|-----|----------|
| **Standard** | ~27MB | No | Most users - includes all core features |
| **Full** | ~31MB | Yes | Users who need AST-based code compression |

**Standard build** includes everything except tree-sitter code compression. This is the recommended build for most users.

**Full build** adds tree-sitter for AST-aware code compression when distilling context fragments. This requires CGO and C compilers, making cross-compilation more complex.

### Prerequisites

- Go 1.21+
- [just](https://github.com/casey/just) command runner (optional)
- C compiler (only for full build)

### Clone and Build

```bash
# Clone the repository
git clone https://github.com/benjaminabbitt/scm.git
cd scm

# Standard build (recommended)
just build
# or: go build -ldflags "-s -w" -o scm .

# Full build with tree-sitter
just build-scm-full
# or: go build -tags treesitter -ldflags "-s -w" -o scm .

# Install
sudo mv scm /usr/local/bin/
```

### Build Commands

| Command | Description |
|---------|-------------|
| `just build` | Standard build (no CGO, smaller) |
| `just build-scm-full` | Full build with tree-sitter (requires CGO) |
| `just install` | Build and install to `~/go/bin` |
| `just install-local` | Build static and install to `~/.local/bin` |
| `just test` | Run tests (standard) |
| `just test -tags treesitter` | Run all tests including tree-sitter |

## Verify Installation

```bash
scm --version
```

Expected output:
```
scm version 0.0.8
```

## Shell Completion

Generate shell completion scripts for better CLI experience:

### Bash

```bash
# Current session only
source <(scm completion bash)

# Permanent (Linux)
scm completion bash > /etc/bash_completion.d/scm

# Permanent (macOS with Homebrew)
scm completion bash > $(brew --prefix)/etc/bash_completion.d/scm
```

### Zsh

```bash
# Add to fpath and restart shell
scm completion zsh > "${fpath[1]}/_scm"
```

### Fish

```fish
scm completion fish > ~/.config/fish/completions/scm.fish
```

### PowerShell

```powershell
scm completion powershell | Out-String | Invoke-Expression
```

## Updating

### Homebrew

```bash
brew upgrade scm
```

### Go Install

```bash
go install github.com/benjaminabbitt/scm@latest
```

### Binary

Download the latest release and replace the existing binary.

## Troubleshooting

### Command not found

Ensure the installation directory is in your PATH:

```bash
# For go install
echo $PATH | grep -q "$(go env GOPATH)/bin" || export PATH=$PATH:$(go env GOPATH)/bin

# For manual install
echo $PATH | grep -q "/usr/local/bin" || export PATH=$PATH:/usr/local/bin
```

### Permission denied

Use `sudo` when installing to system directories, or install to a user directory:

```bash
# Install to user directory instead
mkdir -p ~/.local/bin
mv scm ~/.local/bin/
export PATH=$PATH:~/.local/bin
```

## Next Steps

After installation:

1. [Quick Start](/getting-started/quickstart) - Get up and running
2. [Configuration](/guides/configuration) - Set up your environment
