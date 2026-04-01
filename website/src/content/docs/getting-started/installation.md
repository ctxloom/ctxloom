---
title: "Installation"
---

Choose the installation method that works best for you.

## Download Binary (Recommended)

Download precompiled binaries from the [releases page](https://github.com/ctxloom/ctxloom/releases).

### macOS

```bash
# Apple Silicon (M1/M2/M3)
curl -L https://github.com/ctxloom/ctxloom/releases/latest/download/ctxloom_darwin_arm64.tar.gz | tar xz
sudo mv ctxloom /usr/local/bin/

# Intel
curl -L https://github.com/ctxloom/ctxloom/releases/latest/download/ctxloom_darwin_amd64.tar.gz | tar xz
sudo mv ctxloom /usr/local/bin/
```

### Linux

```bash
# x86_64
curl -L https://github.com/ctxloom/ctxloom/releases/latest/download/ctxloom_linux_amd64.tar.gz | tar xz
sudo mv ctxloom /usr/local/bin/

# ARM64
curl -L https://github.com/ctxloom/ctxloom/releases/latest/download/ctxloom_linux_arm64.tar.gz | tar xz
sudo mv ctxloom /usr/local/bin/
```

## Go Install

If you have Go 1.21+ installed:

```bash
go install github.com/ctxloom/ctxloom@latest
```

Make sure `~/go/bin` is in your PATH:

```bash
export PATH=$PATH:$(go env GOPATH)/bin
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
git clone https://github.com/ctxloom/ctxloom.git
cd ctxloom

# Build
just build
# or: go build -ldflags "-s -w" -o ctxloom .

# Install
sudo mv ctxloom /usr/local/bin/
```

### Build Commands

| Command | Description |
|---------|-------------|
| `just build` | Build ctxloom binary |
| `just install` | Build and install to `~/go/bin` |
| `just install-local` | Build and install to `~/.local/bin` |
| `just test` | Run all tests |

## Verify Installation

```bash
ctxloom --version
```

Expected output:
```
ctxloom version 0.2.0
```

## Shell Completion

Generate shell completion scripts for better CLI experience:

### Bash

```bash
# Current session only
source <(ctxloom completion bash)

# Permanent (Linux)
ctxloom completion bash > /etc/bash_completion.d/ctxloom

# Permanent (macOS)
ctxloom completion bash > /usr/local/etc/bash_completion.d/ctxloom
```

### Zsh

```bash
# Add to fpath and restart shell
ctxloom completion zsh > "${fpath[1]}/_ctxloom"
```

### Fish

```fish
ctxloom completion fish > ~/.config/fish/completions/ctxloom.fish
```

### PowerShell

```powershell
ctxloom completion powershell | Out-String | Invoke-Expression
```

## Updating

### Go Install

```bash
go install github.com/ctxloom/ctxloom@latest
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
mv ctxloom ~/.local/bin/
export PATH=$PATH:~/.local/bin
```

### macOS: "Cannot be opened" or "Unverified developer"

macOS Gatekeeper blocks unsigned binaries downloaded from the internet. You may see:

- "ctxloom cannot be opened because it is from an unidentified developer"
- "ctxloom cannot be opened because Apple cannot check it for malicious software"

**Solution 1: Remove the quarantine attribute (Recommended)**

```bash
# Remove the quarantine flag that macOS adds to downloaded files
xattr -d com.apple.quarantine /usr/local/bin/ctxloom
```

**Solution 2: Allow in System Settings**

1. Try to run `ctxloom` - macOS will block it
2. Open **System Settings** → **Privacy & Security**
3. Scroll down to find the blocked app message
4. Click **"Open Anyway"**
5. Confirm by clicking **"Open"** in the dialog

**Solution 3: Build from source**

Building from source avoids Gatekeeper entirely since the binary is created locally:

```bash
go install github.com/ctxloom/ctxloom@latest
```

**Why this happens:** ctxloom binaries are not code-signed or notarized with Apple. This is common for open-source CLI tools distributed via GitHub releases.

## Next Steps

After installation:

1. [Quick Start](/getting-started/quickstart) - Get up and running
2. [Configuration](/guides/configuration) - Set up your environment
