---
sidebar_position: 1
---

# Installation

## Prerequisites

- Go 1.21+
- [just](https://github.com/casey/just) command runner
- [protoc](https://grpc.io/docs/protoc-installation/) for plugin protocol
- AI CLI: [Claude Code](https://claude.ai/code) or [Gemini CLI](https://github.com/google/generative-ai-cli)

## Install from Source

```bash
# Clone the repository
git clone https://github.com/SophisticatedContextManager/scm.git
cd scm

# Build and install to ~/go/bin
just install
```

### Alternative Installation Options

| Command | Description |
|---------|-------------|
| `just install` | Build and install to `~/go/bin` |
| `just install-local` | Build static and install to `~/.local/bin` |
| `just uninstall` | Remove from `~/.local/bin` |

## Verify Installation

```bash
scm --version
```

## Shell Completion

Generate shell completion scripts for better CLI experience:

**Bash:**
```bash
source <(scm completion bash)                              # Current session
scm completion bash > /etc/bash_completion.d/scm           # Permanent (Linux)
```

**Zsh:**
```bash
scm completion zsh > "${fpath[1]}/_scm"                    # Install, restart shell
```

**Fish:**
```fish
scm completion fish > ~/.config/fish/completions/scm.fish  # Permanent
```
