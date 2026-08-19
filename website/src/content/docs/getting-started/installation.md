---
title: "Installation"
---

Choose the installation method that works best for you.

:::caution[Security Notice: Be Paranoid]
While the author says these scripts are safe, **thou shalt be paranoid**. We encourage you to:

1. **Read the installation scripts** before running them
2. **Check the release notes** for script checksums and VirusTotal lookup links
3. **Verify checksums** if you're extra cautious
4. **Build from source** if you trust no one (we respect that)

The scripts are open source and designed to be auditable. Your security paranoia is valid and encouraged!
:::

## Homebrew (macOS)

The simplest way to install on macOS:

```bash
brew install ctxloom/tap/ctxloom-full   # full build (tree-sitter AST compression)
# or, for the lighter build without tree-sitter:
brew install ctxloom/tap/ctxloom
```

The `ctxloom/tap/...` shorthand auto-taps [`ctxloom/homebrew-tap`](https://github.com/ctxloom/homebrew-tap) — no separate `brew tap` needed. The two casks both install the `ctxloom` binary and are declared to conflict, so install just one. Upgrade later with `brew upgrade ctxloom/tap/ctxloom-full`.

Homebrew casks are macOS-only. On Linux or Windows, use the install script or manual download below.

## Quick Install (Recommended)

:::note[Unsigned binaries]
Script and manual installs place **unsigned binaries** — macOS Gatekeeper or
Windows SmartScreen may require a one-time trust step. The script clears
macOS quarantine automatically where it can; if anything is still blocked,
see [Trusting the Binaries](/getting-started/binary-trust/). Homebrew
installs never need this — pass `--brew` to the script to delegate to brew.
:::

### macOS / Linux

```bash
curl -fsSL https://raw.githubusercontent.com/ctxloom/ctxloom/main/scripts/install.sh | bash
```

The script also installs the companions [taskloom](/taskloom/)
and [ltk](/ltk/), and always fetches the **light build** (no tree-sitter —
see the Homebrew section above for the `_full` archives and the
`ctxloom-full` cask). With Homebrew available, delegate the whole install to
brew (`--brew`; the script also takes `-h`/`--help` for usage) — no
unsigned-binary trust steps. Note `--brew` installs the same light
`ctxloom/tap/ctxloom` cask, not `ctxloom-full`:

```bash
curl -fsSL https://raw.githubusercontent.com/ctxloom/ctxloom/main/scripts/install.sh | bash -s -- --brew
```

Or download and review first (recommended for the security-conscious):

```bash
# Download the script
curl -fsSL https://raw.githubusercontent.com/ctxloom/ctxloom/main/scripts/install.sh -o install.sh

# Read it - it's open source and auditable
less install.sh

# Run it when you're satisfied it's not evil
bash install.sh
```

**[View install.sh source](https://github.com/ctxloom/ctxloom/blob/main/scripts/install.sh)** | **[Release notes](https://github.com/ctxloom/ctxloom/releases/latest)** — SHA256 checksums and VirusTotal lookup links for the install scripts (the scripts are scanned, not the ctxloom binary itself)

### Windows (PowerShell)

```powershell
irm https://raw.githubusercontent.com/ctxloom/ctxloom/main/scripts/install.ps1 | iex
```

The PowerShell script also installs the companions taskloom and ltk. To opt
out, download the script and run it with `-NoCompanions` (or individually,
`-NoTaskloom` / `-NoLtk`).

Or download and review first:

```powershell
# Download the script
Invoke-WebRequest -Uri "https://raw.githubusercontent.com/ctxloom/ctxloom/main/scripts/install.ps1" -OutFile install.ps1

# Read it - it's open source and auditable
Get-Content install.ps1 | more

# Run it when you trust us (or at least trust your antivirus)
.\install.ps1
```

**[View install.ps1 source](https://github.com/ctxloom/ctxloom/blob/main/scripts/install.ps1)** | **[Release notes](https://github.com/ctxloom/ctxloom/releases/latest)** — SHA256 checksums and VirusTotal lookup links for the install scripts (the scripts are scanned, not the ctxloom binary itself)

## Manual Download

If you prefer to download binaries directly without running scripts. These
archives (no `_full` suffix) are the **light build** — no tree-sitter AST
compression. For that, use the Homebrew `ctxloom-full` cask above, or the
`_full` archives / build-from-source instructions below.

### macOS

```bash
# Get latest version
VERSION=$(curl -s https://api.github.com/repos/ctxloom/ctxloom/releases/latest | grep '"tag_name"' | sed -E 's/.*"v([^"]+)".*/\1/')

# Apple Silicon (M1/M2/M3)
curl -L "https://github.com/ctxloom/ctxloom/releases/download/v${VERSION}/ctxloom_${VERSION}_darwin_arm64.tar.gz" | tar xz
sudo mv ctxloom /usr/local/bin/

# Intel
curl -L "https://github.com/ctxloom/ctxloom/releases/download/v${VERSION}/ctxloom_${VERSION}_darwin_amd64.tar.gz" | tar xz
sudo mv ctxloom /usr/local/bin/
```

### Linux

```bash
# Get latest version
VERSION=$(curl -s https://api.github.com/repos/ctxloom/ctxloom/releases/latest | grep '"tag_name"' | sed -E 's/.*"v([^"]+)".*/\1/')

# x86_64
curl -L "https://github.com/ctxloom/ctxloom/releases/download/v${VERSION}/ctxloom_${VERSION}_linux_amd64.tar.gz" | tar xz
sudo mv ctxloom /usr/local/bin/

# ARM64
curl -L "https://github.com/ctxloom/ctxloom/releases/download/v${VERSION}/ctxloom_${VERSION}_linux_arm64.tar.gz" | tar xz
sudo mv ctxloom /usr/local/bin/
```

### Windows

Download the ZIP archive from the [releases page](https://github.com/ctxloom/ctxloom/releases) and extract it.

**PowerShell (manual):**

```powershell
# Get latest version
$VERSION = (Invoke-RestMethod -Uri "https://api.github.com/repos/ctxloom/ctxloom/releases/latest").tag_name -replace '^v', ''

# Download and extract (x64)
Invoke-WebRequest -Uri "https://github.com/ctxloom/ctxloom/releases/download/v$VERSION/ctxloom_${VERSION}_windows_amd64.zip" -OutFile ctxloom.zip
Expand-Archive ctxloom.zip -DestinationPath .
Remove-Item ctxloom.zip

# Move to a directory in PATH (e.g., create one in your user profile)
New-Item -ItemType Directory -Force -Path "$env:USERPROFILE\bin"
Move-Item ctxloom.exe "$env:USERPROFILE\bin\"

# Add to PATH (current session)
$env:PATH += ";$env:USERPROFILE\bin"

# Add to PATH (permanent - run once)
[Environment]::SetEnvironmentVariable("PATH", $env:PATH + ";$env:USERPROFILE\bin", "User")
```

**Or manually:**

1. Go to [releases](https://github.com/ctxloom/ctxloom/releases) and find the latest version
2. Download `ctxloom_<version>_windows_amd64.zip` (e.g., `ctxloom_{{VERSION}}_windows_amd64.zip`)
3. Extract `ctxloom.exe` from the ZIP
4. Move it to a directory in your PATH (e.g., `C:\Users\<username>\bin`)
5. Add that directory to your PATH if needed

## Build from Source

For development or to get the latest unreleased features. Also the most secure option if you're truly paranoid (we appreciate you).

### Prerequisites

- Go 1.25+
- [buf](https://buf.build/docs/installation) for protobuf code generation
- [just](https://github.com/casey/just) command runner (optional)
- C compiler — only needed for the tree-sitter build below (`-tags treesitter`
  with `CGO_ENABLED=1`); the plain build is CGO-free and doesn't need one

The module root has no Go files — the main package is `./cmd/ctxloom`, so
every build/install command below points there, not at `.`.

### Clone and Build

```bash
# Clone the repository
git clone https://github.com/ctxloom/ctxloom.git
cd ctxloom

# Generate protobuf files
buf generate

# Build (light build: memory + vector search, no tree-sitter)
go build -tags memory,vectors -ldflags "-s -w" -o ctxloom ./cmd/ctxloom

# Install
sudo mv ctxloom /usr/local/bin/
```

Omitting `-tags treesitter` (and `CGO_ENABLED=1`) means no AST-based code
compression — the build above matches the light release. For the full build:

```bash
CGO_ENABLED=1 go build -tags memory,vectors,treesitter -ldflags "-s -w" -o ctxloom ./cmd/ctxloom
```

`just build` produces the full build (tree-sitter and friends) inside the
project devcontainer — it needs Docker or Podman on the host.

### Go Install (requires buf)

If you have Go 1.25+ and buf installed:

```bash
# Clone, generate, and install
git clone https://github.com/ctxloom/ctxloom.git
cd ctxloom
buf generate
go install -tags memory,vectors ./cmd/ctxloom
```

Or, from inside the repo, skip the manual buf/tags dance entirely:

```bash
just install
```

`just install` builds the full build (via the devcontainer) and installs
ctxloom, ltk, and taskloom to `~/go/bin`.

Make sure `~/go/bin` is in your PATH:

```bash
export PATH=$PATH:$(go env GOPATH)/bin
```

### Build Commands

| Command | Description |
|---------|-------------|
| `just build` | Build the ctxloom binary (in the devcontainer) |
| `just install` | Build and install ctxloom, ltk, and taskloom to `~/go/bin` |
| `just test` | Run all tests |

## Verify Installation

```bash
ctxloom --version
```

Expected output:
```
ctxloom version {{VERSION}}
```

## Prerequisites for Specific Features

The `ctxloom` binary itself has no run-time dependencies — installing it (any method above) is
enough to run `ctxloom init` and assemble/browse local context. A few features shell out to
tools you install separately, and you only need them for the feature you actually use. This
section exists so a missing one is a documented prerequisite, not a confusing failure.

### Running an AI engine

`ctxloom run` (and `acp`, delegated `agent_run` children) launches the **vendor's own CLI** as a child process —
ctxloom holds no model API client of its own (this is a licensing requirement, not a choice; see
[Architecture](/concepts/architecture/)). Each backend needs its own binary installed and on
`PATH`:

| Backend | Binary | Install |
|---|---|---|
| `claude-code` | `claude` | [claude.ai/code](https://claude.ai/code) |
| `antigravity` | `agy` | `curl -fsSL https://antigravity.google/cli/install.sh \| bash` |
| `codex` | `codex` | [github.com/openai/codex](https://github.com/openai/codex) |
| `kiro` | `kiro-cli` | AWS Kiro |
| `opencode` | `opencode` | [opencode.ai](https://opencode.ai) |

If the backend you launch (the configured default, or `--llm <label>`) has no binary on `PATH`,
`ctxloom run` fails immediately with an error naming which backends **are** currently usable —
it never silently substitutes a different engine. Install (and authenticate) the CLI for the
backend you configured, or point `llm.defaults.primary` at one you already have. See
[Configuration → LLMs](/guides/configuration/#llms) for the config shape.

### Signing and publishing (needs SSH)

Verifying a signature (accepting a remote bundle, trusting a publisher) is pure Go and needs no
external binary at all — it runs the same inside a minimal container with no SSH tooling
present. **Producing** one does need SSH tooling, because ctxloom never generates, stores, or
reads private key material itself (see [Key management](/security/key-management/)) — it always
signs through your existing `ssh-agent`. `ctxloom bundle sign`, `ctxloom review --project`, and
countersigning a `ctxloom review` decision all need:

- The OpenSSH client tools (`ssh-keygen`, `ssh-add`) — to create a key, if you don't already have
  one. These ship with the OS on macOS/Linux and with Git for Windows; nothing extra to install
  on those platforms.
- A running `ssh-agent` with a key loaded (`ssh-add ~/.ssh/id_ed25519`), or `git config
  user.signingkey` naming one — either satisfies ctxloom's key-discovery chain.

If you already sign git commits over SSH, there is nothing extra to set up — ctxloom reuses that
key. If you have neither, `ctxloom bundle sign` and `ctxloom review --project` fail with an actionable
error (the exact `ssh-add`/`ssh-keygen` commands to run) rather than a silent no-op. Reviewing
content for yourself only — `ctxloom review` without `--project` — never requires a key: with
none found, it offers an explicit, confirmed **unsigned** path instead.

### Container isolation (`runtime: container-rootless` / `container-rootful`)

Only needed if you opt into `runtime: container-rootless` or `runtime: container-rootful` (or run `ctxloom container build`) — the
default `runtime: host` needs no container runtime at all. Container-isolated agents need
**docker or podman** installed, on `PATH`, with the daemon reachable (`docker info` / `podman
info` succeeding). Run `ctxloom container check` to diagnose whether the current host can launch
one — see [`ctxloom container`](/reference/cli/ctxloom_container/) and [The engine you don't
control](/security/isolation/#containers-dont-ask).

## Shell Completion

Generate shell completion scripts for better CLI experience:

### Bash

```bash
# Current session only
source <(ctxloom completion bash)

# Permanent (Linux)
ctxloom completion bash > /etc/bash_completion.d/ctxloom

# Permanent (macOS)
ctxloom completion bash > $(brew --prefix)/etc/bash_completion.d/ctxloom
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

### Using Install Scripts

Just run the install script again - it will download and replace the existing binary:

**macOS/Linux:**
```bash
curl -fsSL https://raw.githubusercontent.com/ctxloom/ctxloom/main/scripts/install.sh | bash
```

**Windows:**
```powershell
irm https://raw.githubusercontent.com/ctxloom/ctxloom/main/scripts/install.ps1 | iex
```

### From Source

```bash
cd ctxloom
git pull
buf generate
go install -tags memory,vectors ./cmd/ctxloom
# or, from inside the repo: just install
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

**Solution 1: Use the install script (Recommended)**

The install script automatically removes the quarantine attribute:

```bash
curl -fsSL https://raw.githubusercontent.com/ctxloom/ctxloom/main/scripts/install.sh | bash
```

**Solution 2: Remove the quarantine attribute manually**

```bash
# Remove the quarantine flag that macOS adds to downloaded files
xattr -d com.apple.quarantine /usr/local/bin/ctxloom
```

**Solution 3: Allow in System Settings**

1. Try to run `ctxloom` - macOS will block it
2. Open **System Settings** → **Privacy & Security**
3. Scroll down to find the blocked app message
4. Click **"Open Anyway"**
5. Confirm by clicking **"Open"** in the dialog

**Solution 4: Build from source**

Building from source avoids Gatekeeper entirely since the binary is created locally:

```bash
git clone https://github.com/ctxloom/ctxloom.git
cd ctxloom
buf generate
go install -tags memory,vectors ./cmd/ctxloom
```

**Why this happens:** ctxloom binaries are not code-signed or notarized with Apple. This is common for open-source CLI tools distributed via GitHub releases.

## Next Steps

After installation:

1. [Quick Start](/getting-started/quickstart) - Get up and running
2. [Configuration](/guides/configuration) - Set up your environment
