---
title: "Installation"
---

Choose the installation method that works best for you.

:::caution[Security Notice: Be Paranoid]
While the author says these scripts are safe, **thou shalt be paranoid**. We encourage you to:

1. **Read the installation scripts** before running them
2. **Check the VirusTotal scans** linked below
3. **Verify checksums** if you're extra cautious
4. **Build from source** if you trust no one (we respect that)

The scripts are open source and designed to be auditable. Your security paranoia is valid and encouraged!
:::

## Quick Install (Recommended)

### macOS / Linux

```bash
curl -fsSL https://raw.githubusercontent.com/ctxloom/ctxloom/main/scripts/install.sh | bash
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

**[View install.sh source](https://github.com/ctxloom/ctxloom/blob/main/scripts/install.sh)** | **[VirusTotal scan](https://github.com/ctxloom/ctxloom/releases/latest)** (see release notes for SHA256)

### Windows (PowerShell)

```powershell
irm https://raw.githubusercontent.com/ctxloom/ctxloom/main/scripts/install.ps1 | iex
```

Or download and review first:

```powershell
# Download the script
Invoke-WebRequest -Uri "https://raw.githubusercontent.com/ctxloom/ctxloom/main/scripts/install.ps1" -OutFile install.ps1

# Read it - it's open source and auditable
Get-Content install.ps1 | more

# Run it when you trust us (or at least trust your antivirus)
.\install.ps1
```

**[View install.ps1 source](https://github.com/ctxloom/ctxloom/blob/main/scripts/install.ps1)** | **[VirusTotal scan](https://github.com/ctxloom/ctxloom/releases/latest)** (see release notes for SHA256)

## Manual Download

If you prefer to download binaries directly without running scripts.

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

- Go 1.21+
- [Protocol Buffers](https://protobuf.dev/downloads/) compiler (`protoc`)
- Go protobuf plugins:
  ```bash
  go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
  go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
  ```
- [just](https://github.com/casey/just) command runner (optional)
- C compiler (required for CGO/tree-sitter support)

### Clone and Build

```bash
# Clone the repository
git clone https://github.com/ctxloom/ctxloom.git
cd ctxloom

# Generate protobuf files
go generate ./...

# Build
just build
# or: go build -ldflags "-s -w" -o ctxloom .

# Install
sudo mv ctxloom /usr/local/bin/
```

### Go Install (requires protobuf tools)

If you have Go 1.21+ and protobuf tools installed:

```bash
# Clone, generate, and install
git clone https://github.com/ctxloom/ctxloom.git
cd ctxloom
go generate ./...
go install .
```

Make sure `~/go/bin` is in your PATH:

```bash
export PATH=$PATH:$(go env GOPATH)/bin
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
ctxloom version {{VERSION}}
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
go generate ./...
go install .
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
go generate ./...
go install .
```

**Why this happens:** ctxloom binaries are not code-signed or notarized with Apple. This is common for open-source CLI tools distributed via GitHub releases.

## Next Steps

After installation:

1. [Quick Start](/getting-started/quickstart) - Get up and running
2. [Configuration](/guides/configuration) - Set up your environment
