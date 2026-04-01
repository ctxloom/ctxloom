#!/usr/bin/env bash
# ╔═══════════════════════════════════════════════════════════════════════════╗
# ║   ___ _____ __  __ _     ___   ___  __  __                                ║
# ║  / __|_   _\ \/ /| |    / _ \ / _ \|  \/  |                               ║
# ║ | (__  | |  >  < | |__ | (_) | (_) | |\/| |                               ║
# ║  \___| |_| /_/\_\|____| \___/ \___/|_|  |_|                               ║
# ║                                                                           ║
# ║  Context Loom - Weave context for AI coding agents                       ║
# ║  https://ctxloom.dev                                                      ║
# ╚═══════════════════════════════════════════════════════════════════════════╝
#
# SECURITY NOTICE: You're reading this! Good human!
# ════════════════════════════════════════════════════════════════════════════
# Thou shalt be paranoid. The author (who may or may not be three raccoons
# in a trenchcoat) says this script is safe, but you should verify that claim.
#
# What this script does:
#   1. Detects your OS and architecture (like a doctor, but for computers)
#   2. Downloads the latest ctxloom release from GitHub
#   3. Extracts it (like a dentist, but less painful)
#   4. Puts it somewhere useful (unlike my college degree)
#   5. Makes it executable (gives it a hall pass)
#
# What this script does NOT do:
#   - Mine cryptocurrency (we prefer mining sarcasm)
#   - Phone home (we don't have your number anyway)
#   - Install surprise toolbars (this isn't 2007)
#   - Judge your browser history (that's between you and your ISP)
#
# VirusTotal scan: https://www.virustotal.com/gui/file/[hash will be here]
# Source: https://github.com/ctxloom/ctxloom/blob/main/scripts/install.sh
#
# If you're still reading, congratulations! You've demonstrated more patience
# than most people show when Terms of Service pop up. Here's a cookie: 🍪
# ════════════════════════════════════════════════════════════════════════════

set -euo pipefail

# Colors - because life is too short for monochrome terminals
readonly RED='\033[0;31m'
readonly GREEN='\033[0;32m'
readonly YELLOW='\033[1;33m'
readonly BLUE='\033[0;34m'
readonly MAGENTA='\033[0;35m'
readonly CYAN='\033[0;36m'
readonly NC='\033[0m' # No Color (the fashion choice of terminals everywhere)

# GitHub coordinates - not actual GPS, sadly
readonly REPO="ctxloom/ctxloom"
readonly RELEASES_URL="https://api.github.com/repos/${REPO}/releases/latest"
readonly DOWNLOAD_BASE="https://github.com/${REPO}/releases/download"

# Installation directory - where software goes to be useful
INSTALL_DIR="${INSTALL_DIR:-/usr/local/bin}"
BINARY_NAME="ctxloom"

# Fun facts to display while we work (because watching progress bars is boring)
readonly FUN_FACTS=(
    "Fun fact: The term 'weaving' in ctxloom is a metaphor. No actual looms were harmed."
    "Did you know? Context windows are like memory, but for robots. And they forget everything after each session. Relatable."
    "Pro tip: Reading installation scripts is a sign of wisdom. Or paranoia. Both are valid."
    "The 'ctx' in ctxloom stands for 'context'. The 'loom' stands for... loom. We're not very creative with acronyms."
    "While you wait: The average developer spends 30% of their time re-explaining context to AI. ctxloom wants that time back."
    "Historical note: Before ctxloom, developers used CLAUDE.md files. Some still do. We don't judge. Much."
    "Random thought: If a context fragment falls in a forest and no AI is there to read it, does it still reduce token usage?"
)

# ╔═══════════════════════════════════════════════════════════════════════════╗
# ║ Helper functions - the unsung heroes of shell scripting                   ║
# ╚═══════════════════════════════════════════════════════════════════════════╝

log_info() {
    echo -e "${BLUE}[INFO]${NC} $*"
}

log_success() {
    echo -e "${GREEN}[SUCCESS]${NC} $*"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $*"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $*" >&2
}

log_fun() {
    # Sprinkle some joy into the installation process
    local fact="${FUN_FACTS[$((RANDOM % ${#FUN_FACTS[@]}))]}"
    echo -e "${MAGENTA}[💡]${NC} ${fact}"
}

# Check if a command exists - like checking if your keys are in your pocket
command_exists() {
    command -v "$1" &> /dev/null
}

# Get the user's attention for important messages
attention() {
    echo ""
    echo -e "${CYAN}════════════════════════════════════════════════════════════════${NC}"
    echo -e "${CYAN}$*${NC}"
    echo -e "${CYAN}════════════════════════════════════════════════════════════════${NC}"
    echo ""
}

# ╔═══════════════════════════════════════════════════════════════════════════╗
# ║ Detection functions - CSI: Computer System Investigation                  ║
# ╚═══════════════════════════════════════════════════════════════════════════╝

detect_os() {
    local os
    os="$(uname -s)"
    case "${os}" in
        Linux*)  echo "linux";;
        Darwin*) echo "darwin";;
        CYGWIN*|MINGW*|MSYS*)
            log_error "Windows detected! Please use install.ps1 instead."
            log_error "PowerShell is waiting for you with open arms (and better paths)."
            exit 1
            ;;
        *)
            log_error "Unknown operating system: ${os}"
            log_error "Are you perhaps running this on a smart fridge? We don't support those. Yet."
            exit 1
            ;;
    esac
}

detect_arch() {
    local arch
    arch="$(uname -m)"
    case "${arch}" in
        x86_64|amd64)  echo "amd64";;
        aarch64|arm64) echo "arm64";;
        armv7l)        echo "arm";;
        i386|i686)
            log_error "32-bit architecture detected. It's not 1999 anymore!"
            log_error "Please upgrade to a 64-bit system. Your computer will thank you."
            exit 1
            ;;
        *)
            log_error "Unknown architecture: ${arch}"
            log_error "Is this a quantum computer? Those aren't supported until version 2.0."
            exit 1
            ;;
    esac
}

# ╔═══════════════════════════════════════════════════════════════════════════╗
# ║ Version fetching - stalking GitHub's API (legally)                        ║
# ╚═══════════════════════════════════════════════════════════════════════════╝

get_latest_version() {
    local version

    if command_exists curl; then
        version=$(curl -sL "${RELEASES_URL}" | grep '"tag_name"' | sed -E 's/.*"v([^"]+)".*/\1/' | head -1)
    elif command_exists wget; then
        version=$(wget -qO- "${RELEASES_URL}" | grep '"tag_name"' | sed -E 's/.*"v([^"]+)".*/\1/' | head -1)
    else
        log_error "Neither curl nor wget found. How do you even internet?"
        log_error "Please install curl or wget and try again."
        exit 1
    fi

    if [[ -z "${version}" ]]; then
        log_error "Could not fetch latest version from GitHub."
        log_error "Either GitHub is down (unlikely) or your network is broken (more likely)."
        log_error "Or maybe Mercury is in retrograde? Check your horoscope."
        exit 1
    fi

    echo "${version}"
}

# ╔═══════════════════════════════════════════════════════════════════════════╗
# ║ Download function - the actual work (finally!)                            ║
# ╚═══════════════════════════════════════════════════════════════════════════╝

download_and_install() {
    local version="$1"
    local os="$2"
    local arch="$3"
    local archive_name="ctxloom_${version}_${os}_${arch}.tar.gz"
    local download_url="${DOWNLOAD_BASE}/v${version}/${archive_name}"
    local temp_dir

    temp_dir=$(mktemp -d)
    trap "rm -rf ${temp_dir}" EXIT

    log_info "Downloading ctxloom v${version} for ${os}/${arch}..."
    log_info "URL: ${download_url}"
    echo ""
    log_fun
    echo ""

    # Download the archive
    if command_exists curl; then
        if ! curl -fsSL "${download_url}" -o "${temp_dir}/${archive_name}"; then
            log_error "Download failed! The internet gremlins strike again."
            log_error "Check if the release exists: https://github.com/${REPO}/releases"
            exit 1
        fi
    else
        if ! wget -q "${download_url}" -O "${temp_dir}/${archive_name}"; then
            log_error "Download failed! wget tried its best, but alas..."
            exit 1
        fi
    fi

    log_success "Download complete! Extracting..."

    # Extract the archive
    tar -xzf "${temp_dir}/${archive_name}" -C "${temp_dir}"

    # Check if we need sudo
    local use_sudo=""
    if [[ ! -w "${INSTALL_DIR}" ]]; then
        log_warn "Installing to ${INSTALL_DIR} requires sudo."
        log_info "You'll be asked for your password. This is normal. Trust issues are also normal."
        use_sudo="sudo"
    fi

    # Create install directory if it doesn't exist
    if [[ ! -d "${INSTALL_DIR}" ]]; then
        ${use_sudo} mkdir -p "${INSTALL_DIR}"
    fi

    # Install the binary
    ${use_sudo} mv "${temp_dir}/${BINARY_NAME}" "${INSTALL_DIR}/${BINARY_NAME}"
    ${use_sudo} chmod +x "${INSTALL_DIR}/${BINARY_NAME}"

    # macOS quarantine removal (because Apple loves to "protect" us)
    if [[ "${os}" == "darwin" ]] && command_exists xattr; then
        log_info "Removing macOS quarantine attribute..."
        ${use_sudo} xattr -d com.apple.quarantine "${INSTALL_DIR}/${BINARY_NAME}" 2>/dev/null || true
    fi

    log_success "Installed to ${INSTALL_DIR}/${BINARY_NAME}"
}

# ╔═══════════════════════════════════════════════════════════════════════════╗
# ║ Verification - trust but verify (especially with strangers from the net) ║
# ╚═══════════════════════════════════════════════════════════════════════════╝

verify_installation() {
    log_info "Verifying installation..."

    if command_exists ctxloom; then
        local version
        version=$(ctxloom --version 2>/dev/null || echo "unknown")
        log_success "ctxloom is installed and ready!"
        echo ""
        echo "  ${version}"
        echo ""
        return 0
    fi

    # Check if it's a PATH issue
    if [[ -x "${INSTALL_DIR}/${BINARY_NAME}" ]]; then
        log_warn "ctxloom was installed but isn't in your PATH."
        log_info "Add this to your shell profile (.bashrc, .zshrc, etc.):"
        echo ""
        echo "  export PATH=\"\$PATH:${INSTALL_DIR}\""
        echo ""
        log_info "Then restart your shell or run:"
        echo ""
        echo "  source ~/.bashrc  # or ~/.zshrc"
        echo ""
        return 0
    fi

    log_error "Installation verification failed. This is awkward."
    return 1
}

# ╔═══════════════════════════════════════════════════════════════════════════╗
# ║ Main function - where the magic happens (or at least tries to)            ║
# ╚═══════════════════════════════════════════════════════════════════════════╝

main() {
    attention "ctxloom Installer - Let's weave some context!"

    # Detect the environment
    log_info "Detecting your system..."
    local os arch version
    os=$(detect_os)
    arch=$(detect_arch)
    log_success "Detected: ${os}/${arch}"

    # Get latest version
    log_info "Fetching latest version from GitHub..."
    version=$(get_latest_version)
    log_success "Latest version: v${version}"

    # Download and install
    download_and_install "${version}" "${os}" "${arch}"

    # Verify
    verify_installation

    attention "Installation complete! What's next?"

    echo "Quick start:"
    echo ""
    echo "  # Initialize ctxloom in your project"
    echo "  ctxloom init"
    echo ""
    echo "  # Run with context fragments"
    echo "  ctxloom run -f go-development -f security 'help with code'"
    echo ""
    echo "Documentation: https://ctxloom.dev"
    echo "GitHub: https://github.com/ctxloom/ctxloom"
    echo ""
    echo -e "${GREEN}Thanks for installing ctxloom!${NC}"
    echo -e "${CYAN}May your contexts be woven and your tokens be optimized.${NC}"
    echo ""

    # One last fun fact for the road
    log_fun
}

# Run the thing
main "$@"
