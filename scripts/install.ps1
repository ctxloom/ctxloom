<#
.SYNOPSIS
    ctxloom installer for Windows

.DESCRIPTION
    ╔═══════════════════════════════════════════════════════════════════════════╗
    ║   ___ _____ __  __ _     ___   ___  __  __                                ║
    ║  / __|_   _\ \/ /| |    / _ \ / _ \|  \/  |                               ║
    ║ | (__  | |  >  < | |__ | (_) | (_) | |\/| |                               ║
    ║  \___| |_| /_/\_\|____| \___/ \___/|_|  |_|                               ║
    ║                                                                           ║
    ║  Context Loom - Weave context for AI coding agents                       ║
    ║  https://ctxloom.dev                                                      ║
    ╚═══════════════════════════════════════════════════════════════════════════╝

    SECURITY NOTICE: You're reading this! Excellent life choices!
    ════════════════════════════════════════════════════════════════════════════
    Thou shalt be paranoid. The author (who may or may not be three raccoons
    in a trenchcoat operating a laptop) says this script is safe. But verifying
    that claim is the mark of a true IT professional. Or conspiracy theorist.
    The line is thin.

    What this script does:
      1. Downloads the latest ctxloom release from GitHub
      2. Extracts it to your preferred location
      3. Adds it to your PATH (with your permission, because we're polite)
      4. Makes you feel accomplished about security

    What this script does NOT do:
      - Install Edge extensions (we respect your browser choices)
      - Change your desktop wallpaper (that's between you and Bing)
      - Add shortcuts to the taskbar (those are sacred spaces)
      - Send telemetry (we don't even know what that is) (okay we do, but we don't)

    Checksums: https://github.com/ctxloom/ctxloom/releases (see checksums.txt)
    VirusTotal: Search for this script's SHA256 at https://www.virustotal.com
    Source: https://github.com/ctxloom/ctxloom/blob/main/scripts/install.ps1

    Verify this script: Get-FileHash install.ps1 -Algorithm SHA256

    If you've read this far, you're our kind of people. Have this ASCII art:
           /\_/\
          ( o.o )
           > ^ <  "Meow, I trust you've reviewed this script."

.PARAMETER InstallDir
    Where to install ctxloom. Defaults to ~\bin (we're not monsters who use AppData)

.PARAMETER NoPath
    Skip adding to PATH. For those who like to live dangerously.

.PARAMETER Version
    Specific version to install. Defaults to latest. Time travelers welcome.

.EXAMPLE
    irm https://ctxloom.dev/install.ps1 | iex

.EXAMPLE
    .\install.ps1 -InstallDir "C:\Tools\ctxloom"

.EXAMPLE
    .\install.ps1 -Version "0.3.3"

.NOTES
    Author: ctxloom team (definitely not raccoons)
    License: MIT
    PowerShell: 5.1+
#>

[CmdletBinding()]
param(
    [Parameter()]
    [string]$InstallDir = "$env:USERPROFILE\bin",

    [Parameter()]
    [switch]$NoPath,

    [Parameter()]
    [string]$Version
)

# ╔═══════════════════════════════════════════════════════════════════════════╗
# ║ Configuration - the knobs and dials of this operation                     ║
# ╚═══════════════════════════════════════════════════════════════════════════╝

$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"  # Makes downloads faster. Science!

$Repo = "ctxloom/ctxloom"
$ReleasesUrl = "https://api.github.com/repos/$Repo/releases/latest"
$DownloadBase = "https://github.com/$Repo/releases/download"

# ╔═══════════════════════════════════════════════════════════════════════════╗
# ║ Pretty printing - because stdout deserves aesthetics                      ║
# ╚═══════════════════════════════════════════════════════════════════════════╝

function Write-Info {
    param([string]$Message)
    Write-Host "[INFO] " -ForegroundColor Blue -NoNewline
    Write-Host $Message
}

function Write-Success {
    param([string]$Message)
    Write-Host "[OK] " -ForegroundColor Green -NoNewline
    Write-Host $Message
}

function Write-Warn {
    param([string]$Message)
    Write-Host "[WARN] " -ForegroundColor Yellow -NoNewline
    Write-Host $Message
}

function Write-Err {
    param([string]$Message)
    Write-Host "[ERROR] " -ForegroundColor Red -NoNewline
    Write-Host $Message
}

# ╔═══════════════════════════════════════════════════════════════════════════╗
# ║ Architecture detection - CSI: Computer System Investigation (Windows Ed.) ║
# ╚═══════════════════════════════════════════════════════════════════════════╝

function Get-Architecture {
    # PowerShell makes this surprisingly easy. Gold star, Microsoft!
    $arch = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture

    switch ($arch) {
        "X64" { return "amd64" }
        "Arm64" { return "arm64" }
        "X86" {
            # 32-bit Windows in 2024+? Brave, but unsupported.
            Write-Err "32-bit architecture is not supported."
            exit 1
        }
        default {
            # When Windows reports something unexpected
            Write-Err "Unsupported architecture: $arch"
            exit 1
        }
    }
}

# ╔═══════════════════════════════════════════════════════════════════════════╗
# ║ Version fetching - politely asking GitHub what's new                      ║
# ╚═══════════════════════════════════════════════════════════════════════════╝

function Get-LatestVersion {
    try {
        # GitHub likes to know who's asking. We're polite guests.
        $release = Invoke-RestMethod -Uri $ReleasesUrl -Headers @{
            "User-Agent" = "ctxloom-installer"
        }
        $version = $release.tag_name -replace '^v', ''
        return $version
    }
    catch {
        Write-Err "Failed to fetch version from GitHub."
        Write-Err "Check your network connection and try again."
        exit 1
    }
}

# ╔═══════════════════════════════════════════════════════════════════════════╗
# ║ Download and extract - the main event                                     ║
# ╚═══════════════════════════════════════════════════════════════════════════╝

function Install-Ctxloom {
    param(
        [string]$TargetVersion,
        [string]$Arch,
        [string]$Destination
    )

    $archiveName = "ctxloom_${TargetVersion}_windows_${Arch}.zip"
    $downloadUrl = "$DownloadBase/v$TargetVersion/$archiveName"
    # Get-Random: for when you need a unique temp folder name and creativity is lacking
    $tempDir = Join-Path $env:TEMP "ctxloom-install-$(Get-Random)"
    $archivePath = Join-Path $tempDir $archiveName

    try {
        # Create temp directory (it's temporary, like our patience for slow downloads)
        New-Item -ItemType Directory -Path $tempDir -Force | Out-Null

        Write-Info "Downloading ctxloom v$TargetVersion for windows/$Arch..."

        # Download with progress disabled (it's faster, trust us)
        Invoke-WebRequest -Uri $downloadUrl -OutFile $archivePath

        Write-Success "Downloaded"
        Write-Info "Extracting..."

        # Extract (unzip, but make it sound fancier)
        Expand-Archive -Path $archivePath -DestinationPath $tempDir -Force

        # Create install directory (if it doesn't exist, we make it exist)
        if (-not (Test-Path $Destination)) {
            New-Item -ItemType Directory -Path $Destination -Force | Out-Null
        }

        # Move binary (the moment you've been waiting for)
        $binarySource = Join-Path $tempDir "ctxloom.exe"
        $binaryDest = Join-Path $Destination "ctxloom.exe"

        if (Test-Path $binaryDest) {
            # Out with the old, in with the new
            Remove-Item $binaryDest -Force
        }

        Move-Item $binarySource $binaryDest -Force
        Write-Success "Installed to $binaryDest"
    }
    catch {
        Write-Err "Installation failed: $_"
        exit 1
    }
    finally {
        # Cleanup temp directory (leave no trace, like a ninja)
        if (Test-Path $tempDir) {
            Remove-Item $tempDir -Recurse -Force -ErrorAction SilentlyContinue
        }
    }
}

# ╔═══════════════════════════════════════════════════════════════════════════╗
# ║ PATH management - making Windows find things (it needs all the help)     ║
# ╚═══════════════════════════════════════════════════════════════════════════╝

function Add-ToPath {
    param([string]$Directory)

    $currentPath = [Environment]::GetEnvironmentVariable("PATH", "User")

    # Check if already in PATH (no duplicates, we're not savages)
    if ($currentPath -split ";" -contains $Directory) {
        Write-Info "$Directory already in PATH"
        return
    }

    # Add to user PATH (permanent, survives reboots, the works)
    $newPath = $currentPath + ";" + $Directory
    [Environment]::SetEnvironmentVariable("PATH", $newPath, "User")

    # Also update current session (instant gratification)
    $env:PATH = $env:PATH + ";" + $Directory

    Write-Success "Added to PATH"
}

# ╔═══════════════════════════════════════════════════════════════════════════╗
# ║ Verification - did it actually work? (spoiler: probably!)                 ║
# ╚═══════════════════════════════════════════════════════════════════════════╝

function Test-Installation {
    param([string]$Directory)

    $binaryPath = Join-Path $Directory "ctxloom.exe"

    if (Test-Path $binaryPath) {
        try {
            $version = & $binaryPath --version 2>&1
            Write-Success "Verified: $version"
            return $true
        }
        catch {
            Write-Warn "Binary exists but couldn't execute"
            return $false
        }
    }

    Write-Err "Installation verification failed"
    return $false
}

# ╔═══════════════════════════════════════════════════════════════════════════╗
# ║ Main - where the magic happens (PowerShell edition)                       ║
# ╚═══════════════════════════════════════════════════════════════════════════╝

function Main {
    Write-Host ""
    Write-Host "ctxloom installer" -ForegroundColor Cyan
    Write-Host ""

    # Detect architecture (asking Windows nicely what it is)
    $arch = Get-Architecture
    Write-Success "Detected windows/$arch"

    # Get version (specified or latest)
    $targetVersion = if ($Version) {
        $Version
    } else {
        Get-LatestVersion
    }
    Write-Success "Version: v$targetVersion"

    # Install (the main event)
    Install-Ctxloom -TargetVersion $targetVersion -Arch $arch -Destination $InstallDir

    # Update PATH (unless user said no)
    if (-not $NoPath) {
        Add-ToPath -Directory $InstallDir
    } else {
        Write-Warn "Skipping PATH update"
        Write-Info "Add manually: $InstallDir"
    }

    # Verify (trust but verify)
    Test-Installation -Directory $InstallDir | Out-Null

    Write-Host ""
    Write-Host "Get started:"
    Write-Host "  ctxloom init"
    Write-Host "  ctxloom --help"
    Write-Host ""
    Write-Host "Docs: https://ctxloom.dev"
    Write-Host ""
}

# Execute! (here we go)
Main
