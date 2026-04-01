<#
.SYNOPSIS
    ctxloom installer for Windows - Because PowerShell deserves nice things too.

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

    VirusTotal scan: https://www.virustotal.com/gui/file/[hash will be here]
    Source: https://github.com/ctxloom/ctxloom/blob/main/scripts/install.ps1

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
    # The classic one-liner. YOLO but with style.

.EXAMPLE
    .\install.ps1 -InstallDir "C:\Tools\ctxloom"
    # For those who organize their tools like a true artisan.

.EXAMPLE
    .\install.ps1 -Version "0.3.3"
    # When you need that specific vintage. A sommelier of software.

.NOTES
    Author: ctxloom team (definitely not raccoons)
    License: MIT (the chill license)
    PowerShell: 5.1+ (though 7+ will make you cooler at parties)
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

# Fun facts - because installation should be educational
$FunFacts = @(
    "Fun fact: PowerShell was originally called 'Monad'. ctxloom was almost called 'ContextMaster3000'."
    "Did you know? The average developer copy-pastes context 47 times per day. We counted. It was depressing."
    "Pro tip: Ctrl+C in PowerShell doesn't mean 'copy'. Ask us how we know. Actually don't."
    "Historical note: Before ctxloom, developers used sticky notes. Physical ones. On monitors."
    "Random thought: If an AI reads a prompt in a forest and no developer is there, does it still hallucinate?"
    "While you wait: Studies show that reading installation scripts increases trust by 73%. We made that up."
    "Fun fact: The loom was invented around 4400 BCE. This PowerShell script was invented today. Progress!"
)

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
    Write-Host "[SUCCESS] " -ForegroundColor Green -NoNewline
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

function Write-Fun {
    $fact = $FunFacts | Get-Random
    Write-Host "[" -NoNewline
    Write-Host "!" -ForegroundColor Magenta -NoNewline
    Write-Host "] " -NoNewline
    Write-Host $fact -ForegroundColor Cyan
}

function Write-Banner {
    param([string]$Message)
    $line = "=" * 64
    Write-Host ""
    Write-Host $line -ForegroundColor Cyan
    Write-Host $Message -ForegroundColor Cyan
    Write-Host $line -ForegroundColor Cyan
    Write-Host ""
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
            Write-Err "32-bit Windows? Really? It's not 2005 anymore!"
            Write-Err "Please upgrade to 64-bit Windows. Your RAM will thank you."
            exit 1
        }
        default {
            Write-Err "Unknown architecture: $arch"
            Write-Err "Are you running this on a smart toaster? We don't support those. Yet."
            exit 1
        }
    }
}

# ╔═══════════════════════════════════════════════════════════════════════════╗
# ║ Version fetching - politely asking GitHub what's new                      ║
# ╚═══════════════════════════════════════════════════════════════════════════╝

function Get-LatestVersion {
    try {
        Write-Info "Fetching latest version from GitHub..."
        $release = Invoke-RestMethod -Uri $ReleasesUrl -Headers @{
            "User-Agent" = "ctxloom-installer"  # GitHub likes to know who's asking
        }
        $version = $release.tag_name -replace '^v', ''
        Write-Success "Latest version: v$version"
        return $version
    }
    catch {
        Write-Err "Failed to fetch version from GitHub!"
        Write-Err "Error: $_"
        Write-Err ""
        Write-Err "Possible causes:"
        Write-Err "  - No internet connection (try turning it off and on again)"
        Write-Err "  - GitHub is down (check https://status.github.com)"
        Write-Err "  - Your firewall is being overprotective (it means well)"
        Write-Err "  - Mercury is in retrograde (check your horoscope)"
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
    $tempDir = Join-Path $env:TEMP "ctxloom-install-$(Get-Random)"
    $archivePath = Join-Path $tempDir $archiveName

    try {
        # Create temp directory
        New-Item -ItemType Directory -Path $tempDir -Force | Out-Null

        Write-Info "Downloading ctxloom v$TargetVersion for windows/$Arch..."
        Write-Info "URL: $downloadUrl"
        Write-Host ""
        Write-Fun
        Write-Host ""

        # Download with progress (okay, we lied about SilentlyContinue, but it's faster)
        Invoke-WebRequest -Uri $downloadUrl -OutFile $archivePath

        Write-Success "Download complete! Extracting..."

        # Extract
        Expand-Archive -Path $archivePath -DestinationPath $tempDir -Force

        # Create install directory
        if (-not (Test-Path $Destination)) {
            Write-Info "Creating installation directory: $Destination"
            New-Item -ItemType Directory -Path $Destination -Force | Out-Null
        }

        # Move binary
        $binarySource = Join-Path $tempDir "ctxloom.exe"
        $binaryDest = Join-Path $Destination "ctxloom.exe"

        if (Test-Path $binaryDest) {
            Write-Info "Removing existing installation..."
            Remove-Item $binaryDest -Force
        }

        Move-Item $binarySource $binaryDest -Force
        Write-Success "Installed to: $binaryDest"
    }
    catch {
        Write-Err "Installation failed: $_"
        exit 1
    }
    finally {
        # Cleanup temp directory
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

    if ($currentPath -split ";" -contains $Directory) {
        Write-Info "$Directory is already in PATH. Nice!"
        return
    }

    Write-Info "Adding $Directory to PATH..."

    $newPath = $currentPath + ";" + $Directory
    [Environment]::SetEnvironmentVariable("PATH", $newPath, "User")

    # Also update current session
    $env:PATH = $env:PATH + ";" + $Directory

    Write-Success "Added to PATH! New terminals will see ctxloom automatically."
    Write-Info "Your current terminal session has been updated too. No restart needed!"
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
            Write-Success "ctxloom is installed and ready!"
            Write-Host ""
            Write-Host "  $version"
            Write-Host ""
            return $true
        }
        catch {
            Write-Warn "Binary exists but couldn't run it. That's... concerning."
            Write-Warn "Error: $_"
            return $false
        }
    }

    Write-Err "Installation verification failed. This is awkward."
    return $false
}

# ╔═══════════════════════════════════════════════════════════════════════════╗
# ║ Main - where the magic happens (PowerShell edition)                       ║
# ╚═══════════════════════════════════════════════════════════════════════════╝

function Main {
    Write-Banner "ctxloom Installer for Windows - Let's weave some context!"

    # Detect architecture
    Write-Info "Detecting system architecture..."
    $arch = Get-Architecture
    Write-Success "Detected: windows/$arch"

    # Get version
    $targetVersion = if ($Version) {
        Write-Info "Using specified version: v$Version"
        $Version
    } else {
        Get-LatestVersion
    }

    # Install
    Install-Ctxloom -TargetVersion $targetVersion -Arch $arch -Destination $InstallDir

    # Update PATH (unless user said no)
    if (-not $NoPath) {
        Add-ToPath -Directory $InstallDir
    } else {
        Write-Warn "Skipping PATH update as requested. You rebel, you."
        Write-Info "Add this to your PATH manually: $InstallDir"
    }

    # Verify
    $success = Test-Installation -Directory $InstallDir

    if ($success) {
        Write-Banner "Installation complete! What's next?"

        Write-Host "Quick start:" -ForegroundColor White
        Write-Host ""
        Write-Host "  # Initialize ctxloom in your project" -ForegroundColor Gray
        Write-Host "  ctxloom init"
        Write-Host ""
        Write-Host "  # Run with context fragments" -ForegroundColor Gray
        Write-Host "  ctxloom run -f go-development -f security 'help with code'"
        Write-Host ""
        Write-Host "Documentation: " -NoNewline
        Write-Host "https://ctxloom.dev" -ForegroundColor Cyan
        Write-Host "GitHub: " -NoNewline
        Write-Host "https://github.com/ctxloom/ctxloom" -ForegroundColor Cyan
        Write-Host ""
        Write-Host "Thanks for installing ctxloom!" -ForegroundColor Green
        Write-Host "May your contexts be woven and your tokens be optimized." -ForegroundColor Cyan
        Write-Host ""

        # One last fun fact
        Write-Fun
    }
}

# Execute!
Main
