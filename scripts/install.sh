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
#   6. Sets up shell completion (tab-tab-tab-happiness)
#
# What this script does NOT do:
#   - Mine cryptocurrency (we prefer mining sarcasm)
#   - Phone home (we don't have your number anyway)
#   - Install surprise toolbars (this isn't 2007)
#   - Judge your browser history (that's between you and your ISP)
#
# Checksums: https://github.com/ctxloom/ctxloom/releases (see checksums.txt)
# VirusTotal: Search for this script's SHA256 at https://www.virustotal.com
# Source: https://github.com/ctxloom/ctxloom/blob/main/scripts/install.sh
#
# Verify this script: sha256sum install.sh
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
readonly CYAN='\033[0;36m'
readonly NC='\033[0m' # No Color (the fashion choice of terminals everywhere)

# GitHub coordinates - not actual GPS, sadly
readonly REPO="ctxloom/ctxloom"
readonly RELEASES_URL="https://api.github.com/repos/${REPO}/releases/latest"
readonly DOWNLOAD_BASE="https://github.com/${REPO}/releases/download"

# Installation directory - where software goes to be useful
INSTALL_DIR="${INSTALL_DIR:-/usr/local/bin}"
BINARY_NAME="ctxloom"

# ╔═══════════════════════════════════════════════════════════════════════════╗
# ║ Helper functions - the unsung heroes of shell scripting                   ║
# ╚═══════════════════════════════════════════════════════════════════════════╝

log_info() {
    echo -e "${BLUE}[INFO]${NC} $*"
}

log_success() {
    echo -e "${GREEN}[OK]${NC} $*"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $*"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $*" >&2
}

# Check if a command exists - like checking if your keys are in your pocket
command_exists() {
    command -v "$1" &> /dev/null
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
            # Windows snuck in here like it owns the place
            log_error "Windows detected. Please use install.ps1 instead."
            exit 1
            ;;
        *)
            # Ah yes, the mysterious "other" category
            log_error "Unsupported operating system: ${os}"
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
            # 32-bit called, it wants its architecture back
            log_error "32-bit architecture is not supported."
            exit 1
            ;;
        *)
            # When uname returns something we've never seen before
            log_error "Unsupported architecture: ${arch}"
            exit 1
            ;;
    esac
}

# ╔═══════════════════════════════════════════════════════════════════════════╗
# ║ Version fetching - stalking GitHub's API (legally)                        ║
# ╚═══════════════════════════════════════════════════════════════════════════╝

get_latest_version() {
    local version

    # curl vs wget: the eternal debate, solved by "use whatever exists"
    if command_exists curl; then
        version=$(curl -sL "${RELEASES_URL}" | grep '"tag_name"' | sed -E 's/.*"v([^"]+)".*/\1/' | head -1)
    elif command_exists wget; then
        version=$(wget -qO- "${RELEASES_URL}" | grep '"tag_name"' | sed -E 's/.*"v([^"]+)".*/\1/' | head -1)
    else
        log_error "curl or wget is required. Please install one and try again."
        exit 1
    fi

    if [[ -z "${version}" ]]; then
        log_error "Could not fetch latest version from GitHub."
        log_error "Check your network connection and try again."
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

    # mktemp: creating directories that will be forgotten, since 1979
    temp_dir=$(mktemp -d)
    trap "rm -rf ${temp_dir}" EXIT

    log_info "Downloading ctxloom v${version} for ${os}/${arch}..."

    # Download the archive (the moment of truth)
    if command_exists curl; then
        if ! curl -fsSL "${download_url}" -o "${temp_dir}/${archive_name}"; then
            log_error "Download failed. Check if the release exists:"
            log_error "https://github.com/${REPO}/releases"
            exit 1
        fi
    else
        if ! wget -q "${download_url}" -O "${temp_dir}/${archive_name}"; then
            log_error "Download failed."
            exit 1
        fi
    fi

    log_success "Downloaded"
    log_info "Extracting..."

    # Extract the archive (unboxing video, but for binaries)
    tar -xzf "${temp_dir}/${archive_name}" -C "${temp_dir}"

    # Check if we need sudo (the "please sir may I have some permissions" check)
    local use_sudo=""
    if [[ ! -w "${INSTALL_DIR}" ]]; then
        log_info "Installing to ${INSTALL_DIR} requires sudo"
        use_sudo="sudo"
    fi

    # Create install directory if it doesn't exist
    if [[ ! -d "${INSTALL_DIR}" ]]; then
        ${use_sudo} mkdir -p "${INSTALL_DIR}"
    fi

    # Install the binary (the grand finale)
    ${use_sudo} mv "${temp_dir}/${BINARY_NAME}" "${INSTALL_DIR}/${BINARY_NAME}"
    ${use_sudo} chmod +x "${INSTALL_DIR}/${BINARY_NAME}"

    # macOS Gatekeeper handling (because Apple loves to "protect" us from ourselves)
    # On macOS Sequoia+, unsigned binaries downloaded from the internet get both
    # com.apple.quarantine and com.apple.provenance xattrs. The quarantine flag
    # triggers the "are you sure?" dialog, while provenance can cause the kernel
    # to outright kill the process ("zsh: killed") before it even starts.
    # Removing quarantine + re-signing ad-hoc clears both issues.
    if [[ "${os}" == "darwin" ]]; then
        if command_exists xattr; then
            ${use_sudo} xattr -d com.apple.quarantine "${INSTALL_DIR}/${BINARY_NAME}" 2>/dev/null || true
            ${use_sudo} xattr -d com.apple.provenance "${INSTALL_DIR}/${BINARY_NAME}" 2>/dev/null || true
        fi
        if command_exists codesign; then
            ${use_sudo} codesign --force --sign - "${INSTALL_DIR}/${BINARY_NAME}" 2>/dev/null || true
        fi
    fi

    log_success "Installed to ${INSTALL_DIR}/${BINARY_NAME}"
}

# ╔═══════════════════════════════════════════════════════════════════════════╗
# ║ Verification - trust but verify (especially with strangers from the net) ║
# ╚═══════════════════════════════════════════════════════════════════════════╝

verify_installation() {
    if command_exists ctxloom; then
        local version
        version=$(ctxloom --version 2>/dev/null || echo "unknown")
        log_success "Verified: ${version}"
        return 0
    fi

    # Check if it's a PATH issue (the classic "it's installed but where")
    if [[ -x "${INSTALL_DIR}/${BINARY_NAME}" ]]; then
        log_warn "ctxloom installed but not in PATH"
        echo ""
        echo "Add to your shell profile:"
        echo "  export PATH=\"\$PATH:${INSTALL_DIR}\""
        echo ""
        return 0
    fi

    log_error "Installation verification failed"
    return 1
}

# ╔═══════════════════════════════════════════════════════════════════════════╗
# ║ Shell completion - because typing is overrated                            ║
# ╚═══════════════════════════════════════════════════════════════════════════╝

setup_completion() {
    local ctxloom_bin="${INSTALL_DIR}/${BINARY_NAME}"

    # Can't set up completion if the binary isn't accessible
    if ! command_exists ctxloom && [[ ! -x "${ctxloom_bin}" ]]; then
        log_warn "Skipping shell completion (binary not in PATH)"
        return
    fi

    # Use the binary path if not in PATH yet
    local ctxloom_cmd="ctxloom"
    if ! command_exists ctxloom; then
        ctxloom_cmd="${ctxloom_bin}"
    fi

    # Detect current shell (the one that brought us to this dance)
    local current_shell
    current_shell="$(basename "${SHELL:-bash}")"

    case "${current_shell}" in
        bash)
            setup_bash_completion "${ctxloom_cmd}"
            ;;
        zsh)
            setup_zsh_completion "${ctxloom_cmd}"
            ;;
        fish)
            setup_fish_completion "${ctxloom_cmd}"
            ;;
        *)
            log_info "Shell completion not configured for ${current_shell}"
            ;;
    esac
}

setup_bash_completion() {
    local ctxloom_cmd="$1"
    local completion_dir=""
    local use_sudo=""

    # Find the best completion directory (like house hunting, but for scripts)
    if [[ -d "/etc/bash_completion.d" ]] && [[ -w "/etc/bash_completion.d" ]]; then
        completion_dir="/etc/bash_completion.d"
    elif [[ -d "/etc/bash_completion.d" ]]; then
        completion_dir="/etc/bash_completion.d"
        use_sudo="sudo"
    elif [[ -d "/usr/local/etc/bash_completion.d" ]]; then
        completion_dir="/usr/local/etc/bash_completion.d"
        if [[ ! -w "${completion_dir}" ]]; then
            use_sudo="sudo"
        fi
    else
        # Fall back to user directory
        completion_dir="${HOME}/.local/share/bash-completion/completions"
        mkdir -p "${completion_dir}" 2>/dev/null || true
    fi

    if [[ -n "${completion_dir}" ]] && [[ -d "${completion_dir}" || -n "${use_sudo}" ]]; then
        if ${use_sudo} "${ctxloom_cmd}" completion bash > "/tmp/ctxloom.bash" 2>/dev/null; then
            ${use_sudo} mv "/tmp/ctxloom.bash" "${completion_dir}/ctxloom"
            log_success "Bash completion installed"
        fi
    fi
}

setup_zsh_completion() {
    local ctxloom_cmd="$1"
    local completion_dir=""

    # Zsh completion directories (a journey through fpath)
    if [[ -n "${fpath[1]:-}" ]] && [[ -d "${fpath[1]}" ]] && [[ -w "${fpath[1]}" ]]; then
        completion_dir="${fpath[1]}"
    elif [[ -d "${HOME}/.zsh/completions" ]]; then
        completion_dir="${HOME}/.zsh/completions"
    else
        # Create user completion directory
        completion_dir="${HOME}/.zsh/completions"
        mkdir -p "${completion_dir}" 2>/dev/null || true

        # Remind user to add to fpath if we created a new directory
        if [[ ! ":${FPATH:-}:" == *":${completion_dir}:"* ]]; then
            log_info "Add to ~/.zshrc: fpath=(${completion_dir} \$fpath)"
        fi
    fi

    if [[ -n "${completion_dir}" ]] && [[ -d "${completion_dir}" ]]; then
        if "${ctxloom_cmd}" completion zsh > "${completion_dir}/_ctxloom" 2>/dev/null; then
            log_success "Zsh completion installed"
        fi
    fi
}

setup_fish_completion() {
    local ctxloom_cmd="$1"
    local completion_dir="${HOME}/.config/fish/completions"

    # Fish has a sensible default (thank you, fish)
    mkdir -p "${completion_dir}" 2>/dev/null || true

    if [[ -d "${completion_dir}" ]]; then
        if "${ctxloom_cmd}" completion fish > "${completion_dir}/ctxloom.fish" 2>/dev/null; then
            log_success "Fish completion installed"
        fi
    fi
}

# ╔═══════════════════════════════════════════════════════════════════════════╗
# ║ Main function - where the magic happens (or at least tries to)            ║
# ╚═══════════════════════════════════════════════════════════════════════════╝

main() {
    echo ""
    echo -e "${CYAN}ctxloom installer${NC}"
    echo ""

    # Detect the environment (interrogation time)
    local os arch version
    os=$(detect_os)
    arch=$(detect_arch)
    log_success "Detected ${os}/${arch}"

    # Get latest version (asking GitHub nicely)
    version=$(get_latest_version)
    log_success "Latest version: v${version}"

    # Download and install (the main event)
    download_and_install "${version}" "${os}" "${arch}"

    # Verify (because trust issues are valid)
    verify_installation

    # Set up shell completion (the cherry on top)
    setup_completion

    echo ""
    echo "Get started:"
    echo "  ctxloom init"
    echo "  ctxloom --help"
    echo ""
    echo "Docs: https://ctxloom.dev"
    echo ""
}

# Run the thing (here we go!)
main "$@"
