---
sidebar_position: 1
---

# Installation

Choose the installation method that works best for you.

## Go Install (Recommended)

If you have Go 1.21+ installed:

```bash
go install github.com/SophisticatedContextManager/scm@latest
```

Make sure `~/go/bin` is in your PATH:

```bash
export PATH=$PATH:$(go env GOPATH)/bin
```

## Download Binary

Download precompiled binaries from the [releases page](https://github.com/SophisticatedContextManager/scm/releases).

### macOS

```bash
# Apple Silicon (M1/M2/M3)
curl -L https://github.com/SophisticatedContextManager/scm/releases/latest/download/scm_darwin_arm64.tar.gz | tar xz
sudo mv scm /usr/local/bin/

# Intel
curl -L https://github.com/SophisticatedContextManager/scm/releases/latest/download/scm_darwin_amd64.tar.gz | tar xz
sudo mv scm /usr/local/bin/
```

### Linux

```bash
# x86_64
curl -L https://github.com/SophisticatedContextManager/scm/releases/latest/download/scm_linux_amd64.tar.gz | tar xz
sudo mv scm /usr/local/bin/

# ARM64
curl -L https://github.com/SophisticatedContextManager/scm/releases/latest/download/scm_linux_arm64.tar.gz | tar xz
sudo mv scm /usr/local/bin/
```

## Build from Source

For development or to get the latest unreleased features.

### Prerequisites

- Go 1.21+
- [just](https://github.com/casey/just) command runner (optional)
- C compiler (required for CGO/tree-sitter support)

### Clone and Build

```bash
# Clone the repository
git clone https://github.com/SophisticatedContextManager/scm.git
cd scm

# Build
just build
# or: go build -ldflags "-s -w" -o scm .

# Install
sudo mv scm /usr/local/bin/
```

### Build Commands

| Command | Description |
|---------|-------------|
| `just build` | Build SCM binary |
| `just install` | Build and install to `~/go/bin` |
| `just install-local` | Build and install to `~/.local/bin` |
| `just test` | Run all tests |

## Verify Installation

```bash
scm --version
```

Expected output:
```
scm version 0.2.0
```

## Shell Completion

Generate shell completion scripts for better CLI experience:

### Bash

```bash
# Current session only
source <(scm completion bash)

# Permanent (Linux)
scm completion bash > /etc/bash_completion.d/scm

# Permanent (macOS)
scm completion bash > /usr/local/etc/bash_completion.d/scm
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

### Go Install

```bash
go install github.com/SophisticatedContextManager/scm@latest
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
