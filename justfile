# Default recipe
default: build

# Get version from versionator (with fallback for CI without versionator)
# Format: v0.0.1-abc1234.20240115103045 (uncommitted) or v0.0.1-abc1234 (clean)
# Requires versionator >= v0.2.0 (DateTimeDirty + `output version` subcommand).
version := `versionator output version -t "{{Prefix}}{{MajorMinorPatch}}{{PreReleaseWithDash}}" --prefix --prerelease="{{ShortHash}}{{DateTimeDirty}}" 2>/dev/null || echo "dev"`

# ===== Version management (versionator) =====

# Show current version
show-version:
    @versionator output version

# Bump patch version (0.0.X)
bump-patch:
    versionator bump patch inc

# Bump minor version (0.X.0)
bump-minor:
    versionator bump minor inc

# Bump major version (X.0.0)
bump-major:
    versionator bump major inc

# Auto-bump based on conventional commits (feat: -> minor, fix: -> patch)
bump:
    versionator bump

# Create release (git tag + push)
release:
    versionator release push

# Build the main binary with all features (delegates to devcontainer)
build: dev-image
    just _run build

# Compress binary with UPX (delegates to devcontainer)
compress: dev-image
    just _run compress

# Build and compress (delegates to devcontainer)
build-compressed: build compress

# Validate fragment YAML files (delegates to devcontainer)
validate: dev-image
    just _run validate

# Generate protobuf code (delegates to devcontainer)
proto: dev-image
    just _run proto

# List available plugins
plugin-list:
    ./ctxloom plugin list

# Build with verbose output (local, for debugging)
build-verbose:
    go build -v -ldflags "-X github.com/ctxloom/ctxloom/cmd.Version={{version}}" -o ctxloom .

# Regenerate the published JSON Schemas for ctxloom's JSON output into the
# gitignored resources/schema/gen/ by reflecting their producing Go structs.
# A generated build artifact (like protobuf), not checked in. Build-tagged
# `schemagen` so the reflection dependency never enters the production binary.
gen-schemas:
    go run -tags schemagen ./cmd/gen-schemas

# Ensure the covdata tool is present for multi-package coverage merges.
# Go 1.25 dropped covdata (and other secondary tools) from the prebuilt
# distribution — they're built on demand from src/cmd. But `go test
# -coverprofile ./...` merges coverage for test-less packages by invoking
# covdata out of GOTOOLDIR, and an auto-downloaded toolchain's GOTOOLDIR is
# read-only with no covdata, so the merge fails with `no such tool "covdata"`.
# Build the version-matched covdata into GOTOOLDIR once (idempotent).
_ensure-covdata:
    #!/usr/bin/env bash
    set -euo pipefail
    tooldir="$(go env GOTOOLDIR)"
    if [ -x "$tooldir/covdata" ]; then exit 0; fi
    chmod u+w "$tooldir" 2>/dev/null || true
    go build -o "$tooldir/covdata" cmd/covdata
    echo "ctxloom: built version-matched covdata into $tooldir/"

# Run all tests (builds ctxloom first for acceptance tests).
# Coverage is filtered through .coverignore so generated files
# (protobuf, gRPC) don't drag the reported number down.
test: build _ensure-covdata
    #!/usr/bin/env bash
    set -e
    go test -race -coverprofile=coverage.raw.out ./...
    just _filter_coverage coverage.raw.out coverage.out
    rm -f coverage.raw.out

# Run tests with verbose output
test-verbose:
    go test -v ./...

# Run the offline suite under a hostile environment to prove test isolation: a
# junk HOME (no real ~/.ctxloom) plus poison session env. A green run means no
# test depends on real-home content or ambient session env, and none pollutes the
# user's ~/.ctxloom. The Go toolchain + caches are pinned to their real locations
# first (the version-manager shim and module cache live under the real home), and
# the real go binary is invoked directly so junking HOME doesn't break the build.
test-dirty:
    #!/usr/bin/env bash
    set -e
    GO="$(go env GOROOT)/bin/go"
    export GOCACHE="$(go env GOCACHE)" GOMODCACHE="$(go env GOMODCACHE)" GOPATH="$(go env GOPATH)"
    export HOME="$(mktemp -d)"
    export CTXLOOM_PROJECT_ID=poison-project CTXLOOM_SESSION_HARP=poison-harp
    export CTXLOOM_RESUMED_FROM=poison CTXLOOM_RESUMED_PARTS=poison CTXLOOM_DEBUG_HTTP=1
    export GITHUB_TOKEN=poison-token GH_TOKEN=poison-token CODEX_HOME=/poison/codex
    export CTXLOOM_ROOT=/poison/root
    "$GO" test ./internal/... ./cmd/...

# Filter coverage output using patterns from .coverignore
# Usage: _filter_coverage <input> <output>
_filter_coverage INPUT OUTPUT:
    #!/usr/bin/env bash
    set -e
    if [ -f .coverignore ]; then
        # Build grep pattern from .coverignore (skip comments and empty lines)
        patterns=$(grep -v '^#' .coverignore | grep -v '^$' | paste -sd '|' -)
        if [ -n "$patterns" ]; then
            grep -Ev "$patterns" "{{INPUT}}" > "{{OUTPUT}}" || cp "{{INPUT}}" "{{OUTPUT}}"
            exit 0
        fi
    fi
    cp "{{INPUT}}" "{{OUTPUT}}"

# Run tests with coverage (excludes patterns in .coverignore)
cover:
    #!/usr/bin/env bash
    set -e
    echo "Running tests with coverage..."
    go test -coverprofile=coverage.raw.out ./... > /dev/null 2>&1
    just _filter_coverage coverage.raw.out coverage.out
    rm -f coverage.raw.out
    echo "Coverage (excluding patterns from .coverignore):"
    go tool cover -func=coverage.out | tail -1

# Show per-function coverage (excludes patterns in .coverignore)
cover-func:
    #!/usr/bin/env bash
    set -e
    go test -coverprofile=coverage.raw.out ./... > /dev/null 2>&1
    just _filter_coverage coverage.raw.out coverage.out
    rm -f coverage.raw.out
    echo "Coverage by function (excluding patterns from .coverignore):"
    go tool cover -func=coverage.out

# Generate HTML coverage report (excludes patterns in .coverignore)
cover-html:
    #!/usr/bin/env bash
    set -e
    go test -coverprofile=coverage.raw.out ./... > /dev/null 2>&1
    just _filter_coverage coverage.raw.out coverage.out
    rm -f coverage.raw.out
    go tool cover -html=coverage.out -o coverage.html
    echo "Coverage report generated: coverage.html"

# Run tests with coverage (legacy alias)
test-coverage: cover

# Run the cross-agent equity conformance suite (claude/gemini/codex through the
# shared agent.SettingsWriter contract). Tag-gated so it's excluded from the
# default `go test ./...`; run it explicitly here.
test-conformance:
    go test -race -tags conformance ./internal/lm/conformance/...

# Run integration tests (requires ctxloom binary)
test-integration: build
    go test -v -tags integration ./tests/integration/...

# Run integration tests matching a -run PATTERN (requires ctxloom binary)
test-integration-run PATTERN: build
    go test -v -tags integration -run '{{PATTERN}}' ./tests/integration/...

# Run the full-stack acceptance suite (godog): asserts each change across files,
# CLI, and mock-agent MCP traffic. Hermetic by default (@live scenarios skipped).
# Build runs in the devcontainer; the suite runs on the host like integration.
test-acceptance: build
    go test -tags "acceptance integration" -count=1 ./tests/acceptance/...

# Build the acceptance image (devcontainer toolchain + Node + the Claude Code
# agent), run as a non-root `ctxloom` user. The ctxloom binary is NOT baked in;
# it is built at runtime from the mounted workspace by test-acceptance-live-container.
container-build-acceptance: dev-image
    {{container_cmd}} build -t {{registry}}/ctxloom-acceptance:latest \
        --build-arg BASE_IMAGE={{devcontainer_image}}:latest \
        -f .devcontainer/acceptance.Dockerfile .

# Run the full acceptance suite (incl. @live real-agent scenarios) inside the
# acceptance container as the non-root `ctxloom` user, on THIS machine. Builds
# the image first. Your ~/.claude and ~/.gemini are copied to a world-readable
# staging dir and mounted read-only — under rootless docker the non-root user
# maps to a subuid that cannot read your 0600 credentials directly. Set
# {ANTHROPIC,GEMINI,GOOGLE}_API_KEY to use the unattended API-key path instead.
# ctxloom is built at runtime from the read-only workspace mount; all writes go
# to the container HOME / tmp. Each agent's @live rows self-skip without creds.
test-acceptance-live-container: container-build-acceptance
    #!/usr/bin/env bash
    set -euo pipefail
    staging="$(mktemp -d)"
    trap 'chmod -R u+w "$staging" 2>/dev/null; rm -rf "$staging"' EXIT

    # Credentials: copy only the files the live steps read (copyClaudeCredentials /
    # copyGeminiCredentials), not the whole credential trees — they hold large logs,
    # caches, and dangling symlinks.
    mkdir -p "$staging/.claude"
    for f in .credentials.json settings.json config.json; do
        if [ -f "$HOME/.claude/$f" ]; then cp "$HOME/.claude/$f" "$staging/.claude/$f"; fi
    done
    if [ -f "$HOME/.claude.json" ]; then cp "$HOME/.claude.json" "$staging/.claude.json"; fi

    mkdir -p "$staging/.gemini"
    for f in oauth_creds.json google_accounts.json settings.json installation_id user_id; do
        if [ -f "$HOME/.gemini/$f" ]; then cp "$HOME/.gemini/$f" "$staging/.gemini/$f"; fi
    done

    # Source: stage a copy of the working tree (minus .git) so the non-root user
    # can read it. Rootless docker maps that user to a subuid, and the working
    # tree has mixed perms (some 0600 files) it could not otherwise read; staging
    # + a+rX avoids mutating your tree.
    # Stage the org-mirror layout (go.work + ctxloom + shared + claude + gemini +
    # codex) so the runtime build resolves the sibling modules from local source
    # via go.work.
    mkdir -p "$staging/src/ctxloom/main" "$staging/src/shared/main" "$staging/src/claude/main" "$staging/src/gemini/main" "$staging/src/codex/main"
    tar -cf - --exclude=./.git --exclude='*.test' --exclude=./website/node_modules . | tar -xf - -C "$staging/src/ctxloom/main"
    ( cd ../../shared/main && tar -cf - --exclude=./.git --exclude='*.test' . ) | tar -xf - -C "$staging/src/shared/main"
    ( cd ../../claude/main && tar -cf - --exclude=./.git --exclude='*.test' . ) | tar -xf - -C "$staging/src/claude/main"
    ( cd ../../gemini/main && tar -cf - --exclude=./.git --exclude='*.test' . ) | tar -xf - -C "$staging/src/gemini/main"
    ( cd ../../codex/main && tar -cf - --exclude=./.git --exclude='*.test' . ) | tar -xf - -C "$staging/src/codex/main"
    cp go.work.container "$staging/src/go.work"
    # Materialize a COMPLETE go.work.sum for the staged workspace. The in-container
    # build runs offline against a read-only /workspace + read-only module cache,
    # so it can neither write go.work.sum nor reach the network sumdb. The host's
    # go.work.sum carries only go.mod hashes for workspace-upgraded deps (the host
    # build fills the full module hashes from the proxy/sumdb at build time) — so
    # seed from it, then `go work sync` here (host has proxy + cache) to record the
    # missing full hashes. Without this the container dies on either
    # "go.work.sum: read-only file system" or a sumdb lookup.
    if [ -f ../../go.work.sum ]; then cp ../../go.work.sum "$staging/src/go.work.sum"; fi
    ( cd "$staging/src" && go work sync )
    chmod -R a+rX "$staging"

    mounts=(-v "$staging/.claude:/home/ctxloom/.claude:ro" -v "$staging/.gemini:/home/ctxloom/.gemini:ro" -v "$staging/src:/workspace:ro")
    if [ -f "$staging/.claude.json" ]; then mounts+=(-v "$staging/.claude.json:/home/ctxloom/.claude.json:ro"); fi
    # Reuse the host module cache read-only so the runtime build doesn't re-download.
    if [ -d "$HOME/go/pkg/mod" ]; then mounts+=(-v "$HOME/go/pkg/mod:/home/ctxloom/go/pkg/mod:ro"); fi

    # Forward API keys when present so the unattended API-key path runs without
    # copied subscription creds. Absent keys fall back to the mounted cred dirs.
    keys=()
    for k in ANTHROPIC_API_KEY GEMINI_API_KEY GOOGLE_API_KEY; do
        if [ -n "${!k:-}" ]; then keys+=(-e "$k"); fi
    done

    {{container_cmd}} run --rm \
        "${mounts[@]}" \
        ${keys[@]+"${keys[@]}"} \
        -e HOME=/home/ctxloom \
        -e CTXLOOM_ACCEPTANCE_LIVE=1 \
        -e ACCEPTANCE_TAGS="~@network" \
        -e GOCACHE=/home/ctxloom/.cache/go-build \
        -e GOMODCACHE=/home/ctxloom/go/pkg/mod \
        -e GOPATH=/home/ctxloom/go \
        -e GOFLAGS=-mod=readonly \
        -w /workspace/ctxloom/main \
        {{registry}}/ctxloom-acceptance:latest \
        bash -c 'set -e; \
            go build -o /home/ctxloom/ctxloom . && \
            CTXLOOM_BINARY=/home/ctxloom/ctxloom go test -tags "acceptance integration" -count=1 ./tests/acceptance/...'

# Run a single package's tests under -race (fast local iteration)
test-pkg PKG *ARGS:
    go test -race {{ARGS}} {{PKG}}

# Run all tests in container (matches CI environment)
test-container:
    #!/usr/bin/env bash
    # See _run for why --user is skipped under rootless docker.
    user_flag=(--user "$(id -u):$(id -g)")
    if docker info 2>/dev/null | grep -q "rootless"; then
        user_flag=()
    fi
    docker run --rm "${user_flag[@]}" -v "$(pwd):/app" -w /app golang:1.26 sh -c '\
        go mod download && \
        go test -race ./... && \
        CGO_ENABLED=0 go build -ldflags "-X github.com/ctxloom/ctxloom/cmd.Version={{version}}" -o ctxloom . && \
        go test -v -tags integration ./tests/integration/...'

# ===== Mutation testing =====

# Run mutation tests with gremlins (requires gremlins installed)
test-mutation *ARGS:
    gremlins unleash {{ARGS}}

# Run mutation tests on specific package
test-mutation-pkg PKG *ARGS:
    gremlins unleash ./{{PKG}}/... {{ARGS}}

# Install gremlins
test-mutation-install:
    go install github.com/go-gremlins/gremlins/cmd/gremlins@v0.6.0

# Run mutation tests in container
test-mutation-container:
    #!/usr/bin/env bash
    # See _run for why --user is skipped under rootless docker.
    user_flag=(--user "$(id -u):$(id -g)")
    if docker info 2>/dev/null | grep -q "rootless"; then
        user_flag=()
    fi
    docker run --rm "${user_flag[@]}" -v "$(pwd):/app" -w /app gogremlins/gremlins gremlins unleash

# Clean build artifacts
clean:
    rm -f ctxloom
    rm -rf bin/ man/
    go clean

# Install dependencies
deps:
    go mod download

# Tidy dependencies
tidy:
    go mod tidy

# Format code
fmt:
    go fmt ./...

# Lint code (delegates to devcontainer for the pinned golangci-lint)
lint: dev-image
    just _run lint

# ===== Code complexity (lizard, in devcontainer) =====
# lizard is a cross-platform, multi-language per-function complexity analyzer,
# installed in the devcontainer image. These targets delegate into it, so no
# host install is needed (implementations live in justfile.container).

# Per-function cyclomatic complexity as a human-readable table + warnings.
# Defaults to the repo root; pass paths/flags to scope, e.g.
#   just complexity internal/remote
#   just complexity -C 15 .           (warn on functions over CCN 15)
complexity *ARGS: dev-image
    just _run complexity {{ARGS}}

# Same per-function analysis as CSV (header prepended) for LLM/tooling ingest.
# Columns: nloc,ccn,tokens,params,length,location,file,function,long_name,start,end
complexity-csv *ARGS: dev-image
    just _run complexity-csv {{ARGS}}

# Enforcing gate: fail if any function exceeds CCN 10 (used by the CI lint job).
complexity-check *ARGS: dev-image
    just _run complexity-check {{ARGS}}

# Run the CLI locally without installing — builds ./ctxloom (host, no
# treesitter/CGO) and execs it attached to this terminal, so interactive
# sessions get a real tty (cleaner than `go run` for pty/raw-mode smoke tests).
# Never touches your PATH/installed ctxloom. E.g. `just run run`, `just run memory list`.
run *ARGS:
    #!/usr/bin/env bash
    set -euo pipefail
    go build -ldflags "-X github.com/ctxloom/ctxloom/cmd.Version={{version}}" -o ctxloom .
    exec ./ctxloom {{ARGS}}

# Build, compress, and install to ~/go/bin (standard Go location)
# Atomic rename instead of pkill+cp: replacing the directory entry leaves the
# busy inode mapped for any running ctxloom (avoids ETXTBSY and never dumps a
# live ctxloom-managed session); new launches pick up the new binary.
install: build-compressed
    mkdir -p ~/go/bin
    cp ctxloom ~/go/bin/ctxloom.new
    mv -f ~/go/bin/ctxloom.new ~/go/bin/ctxloom

# Uninstall from ~/go/bin
uninstall:
    rm -f ~/go/bin/ctxloom

# Generate man pages
man:
    go run ./scripts/genman

# Install man pages (Linux/macOS)
man-install: man
    @mkdir -p ~/.local/share/man/man1
    cp man/man1/*.1 ~/.local/share/man/man1/
    @echo "Man pages installed. Run 'man ctxloom' to view."

# Show help
help:
    ./ctxloom --help

# ===== Documentation targets =====

# Start docs dev server (http://localhost:4321)
docs:
    cd website && npm run dev

# Build docs for production
docs-build:
    cd website && npm run build

# Preview production docs build
docs-preview:
    cd website && npm run preview

# Initialize .ctxloom directory
init:
    ./ctxloom init

# Dry run with test fragments
dry-run PROMPT:
    ./ctxloom run -f test-fragment -f additional-context -n "{{PROMPT}}"

# Run with Gemini plugin
gemini *ARGS:
    ./ctxloom -P gemini {{ARGS}}

# Run with Claude plugin (default)
claude *ARGS:
    ./ctxloom -P claude-code {{ARGS}}

# Code review with reviewer profile
review *ARGS:
    ./ctxloom -p reviewer -r code-review {{ARGS}}

# ===== Terraform targets =====

# Initialize Terraform
tf-init:
    cd terraform && terraform init

# Plan Terraform deployment
tf-plan:
    cd terraform && terraform plan

# Apply Terraform deployment
tf-apply:
    cd terraform && terraform apply

# Destroy Terraform deployment
tf-destroy:
    cd terraform && terraform destroy

# Show Terraform outputs
tf-output:
    cd terraform && terraform output

# Format Terraform files
tf-fmt:
    cd terraform && terraform fmt

# Validate Terraform configuration
tf-validate:
    cd terraform && terraform validate

# ===== Container targets =====

# Container registry (override with: just registry=ghcr.io/user container-build-all)
registry := "localhost"

# Container variant: wolfi (glibc, secure) or alpine (musl, smaller)
variant := "wolfi"

# Build base agent container
container-build-base:
    podman build -t {{registry}}/ctxloom-agent-base:latest \
        -f container/{{variant}}/Containerfile-base container/{{variant}}/

# Build Claude Code agent container
container-build-claude: container-build-base
    podman build -t {{registry}}/ctxloom-agent-claude:latest \
        --build-arg BASE_IMAGE={{registry}}/ctxloom-agent-base:latest \
        -f container/{{variant}}/Containerfile-claude-code container/{{variant}}/

# Build Gemini CLI agent container
container-build-gemini: container-build-base
    podman build -t {{registry}}/ctxloom-agent-gemini:latest \
        --build-arg BASE_IMAGE={{registry}}/ctxloom-agent-base:latest \
        -f container/{{variant}}/Containerfile-gemini container/{{variant}}/

# Build Codex agent container
container-build-codex: container-build-base
    podman build -t {{registry}}/ctxloom-agent-codex:latest \
        --build-arg BASE_IMAGE={{registry}}/ctxloom-agent-base:latest \
        -f container/{{variant}}/Containerfile-codex container/{{variant}}/

# Build Cline agent container
container-build-cline: container-build-base
    podman build -t {{registry}}/ctxloom-agent-cline:latest \
        --build-arg BASE_IMAGE={{registry}}/ctxloom-agent-base:latest \
        -f container/{{variant}}/Containerfile-cline container/{{variant}}/

# Build Aider agent container (standalone - Python)
container-build-aider:
    podman build -t {{registry}}/ctxloom-agent-aider:latest \
        -f container/{{variant}}/Containerfile-aider container/{{variant}}/

# Build Goose agent container (standalone - Block)
container-build-goose:
    podman build -t {{registry}}/ctxloom-agent-goose:latest \
        -f container/{{variant}}/Containerfile-goose container/{{variant}}/

# Build Q Developer agent container (standalone - Amazon)
container-build-qdeveloper:
    podman build -t {{registry}}/ctxloom-agent-qdeveloper:latest \
        -f container/{{variant}}/Containerfile-qdeveloper container/{{variant}}/

# Build all agent containers
container-build-agents: container-build-claude container-build-gemini container-build-codex container-build-cline container-build-aider container-build-goose container-build-qdeveloper

# ===== Language LSP containers =====

# Build Go LSP container (gopls + tools)
container-build-lang-go: container-build-base
    podman build -t {{registry}}/ctxloom-lsp-go:latest \
        --build-arg BASE_IMAGE={{registry}}/ctxloom-agent-base:latest \
        -f container/{{variant}}/lang/Containerfile-go container/{{variant}}/

# Build Python LSP container (pyright + tools)
container-build-lang-python: container-build-base
    podman build -t {{registry}}/ctxloom-lsp-python:latest \
        --build-arg BASE_IMAGE={{registry}}/ctxloom-agent-base:latest \
        -f container/{{variant}}/lang/Containerfile-python container/{{variant}}/

# Build Rust LSP container (rust-analyzer + tools)
container-build-lang-rust: container-build-base
    podman build -t {{registry}}/ctxloom-lsp-rust:latest \
        --build-arg BASE_IMAGE={{registry}}/ctxloom-agent-base:latest \
        -f container/{{variant}}/lang/Containerfile-rust container/{{variant}}/

# Build TypeScript LSP container (typescript-language-server)
container-build-lang-typescript: container-build-base
    podman build -t {{registry}}/ctxloom-lsp-typescript:latest \
        --build-arg BASE_IMAGE={{registry}}/ctxloom-agent-base:latest \
        -f container/{{variant}}/lang/Containerfile-typescript container/{{variant}}/

# Build Java LSP container (jdtls + tools)
container-build-lang-java: container-build-base
    podman build -t {{registry}}/ctxloom-lsp-java:latest \
        --build-arg BASE_IMAGE={{registry}}/ctxloom-agent-base:latest \
        -f container/{{variant}}/lang/Containerfile-java container/{{variant}}/

# Build C# LSP container (omnisharp)
container-build-lang-csharp: container-build-base
    podman build -t {{registry}}/ctxloom-lsp-csharp:latest \
        --build-arg BASE_IMAGE={{registry}}/ctxloom-agent-base:latest \
        -f container/{{variant}}/lang/Containerfile-csharp container/{{variant}}/

# Build all language LSP containers
container-build-langs: container-build-lang-go container-build-lang-python container-build-lang-rust container-build-lang-typescript container-build-lang-java container-build-lang-csharp

# Build all containers (base + langs + agents)
container-build-all: container-build-langs container-build-agents

# Push all agent containers to registry
container-push-agents:
    podman push {{registry}}/ctxloom-agent-base:latest
    podman push {{registry}}/ctxloom-agent-claude:latest
    podman push {{registry}}/ctxloom-agent-gemini:latest
    podman push {{registry}}/ctxloom-agent-codex:latest
    podman push {{registry}}/ctxloom-agent-cline:latest
    podman push {{registry}}/ctxloom-agent-aider:latest
    podman push {{registry}}/ctxloom-agent-goose:latest
    podman push {{registry}}/ctxloom-agent-qdeveloper:latest

# Push all language LSP containers to registry
container-push-langs:
    podman push {{registry}}/ctxloom-lsp-go:latest
    podman push {{registry}}/ctxloom-lsp-python:latest
    podman push {{registry}}/ctxloom-lsp-rust:latest
    podman push {{registry}}/ctxloom-lsp-typescript:latest
    podman push {{registry}}/ctxloom-lsp-java:latest
    podman push {{registry}}/ctxloom-lsp-csharp:latest

# Push all containers to registry
container-push-all: container-push-langs container-push-agents

# Clean agent container images
container-clean-agents:
    -podman rmi {{registry}}/ctxloom-agent-claude:latest
    -podman rmi {{registry}}/ctxloom-agent-gemini:latest
    -podman rmi {{registry}}/ctxloom-agent-codex:latest
    -podman rmi {{registry}}/ctxloom-agent-cline:latest
    -podman rmi {{registry}}/ctxloom-agent-aider:latest
    -podman rmi {{registry}}/ctxloom-agent-goose:latest
    -podman rmi {{registry}}/ctxloom-agent-qdeveloper:latest

# Clean language LSP container images
container-clean-langs:
    -podman rmi {{registry}}/ctxloom-lsp-go:latest
    -podman rmi {{registry}}/ctxloom-lsp-python:latest
    -podman rmi {{registry}}/ctxloom-lsp-rust:latest
    -podman rmi {{registry}}/ctxloom-lsp-typescript:latest
    -podman rmi {{registry}}/ctxloom-lsp-java:latest
    -podman rmi {{registry}}/ctxloom-lsp-csharp:latest

# Clean all container images
container-clean: container-clean-agents container-clean-langs
    -podman rmi {{registry}}/ctxloom-agent-base:latest

# List all ctxloom container images
container-list:
    @podman images | grep -E "ctxloom-(agent|lsp)" | sort

# ===== Devcontainer overlay pattern =====
# Runs targets inside devcontainer with CGO dependencies (libtokenizers, ONNX runtime)
# Uses justfile.container which is mounted over justfile inside container

# Container runtime (docker or podman)
container_cmd := env_var_or_default("CONTAINER_CMD", "docker")

# Devcontainer image name
devcontainer_image := "ctxloom-devcontainer"

# Build devcontainer image
dev-image:
    {{container_cmd}} build -t {{devcontainer_image}}:latest -f .devcontainer/Dockerfile .

# Internal helper: run just target inside devcontainer
# Mounts justfile.container as /workspace/justfile (overlay pattern)
_run +ARGS:
    #!/usr/bin/env bash
    if [ -n "$DEVCONTAINER" ] || [ -n "$CI" ] || [ -n "$GITHUB_ACTIONS" ]; then
        # Already inside container (devcontainer or CI), use container justfile directly
        just -f justfile.container {{ARGS}}
    else
        # Run in container with justfile overlay and uid/gid mapping.
        #
        # Skip --user under rootless docker: the daemon already maps container
        # root (uid 0) to the invoking host user, so an explicit --user 1000:1000
        # lands on an unrelated subordinate uid that can neither write workspace
        # files nor read mode-0700 dirs. Rootful daemons need --user to avoid
        # leaving root-owned files in the host workspace.
        user_flag=(--user "$(id -u):$(id -g)")
        if {{container_cmd}} info 2>/dev/null | grep -q "rootless"; then
            user_flag=()
        fi
        # Mount the org-mirror layout so go.work resolves sibling modules
        # (shared, claude, gemini, codex) from local source. ctxloom is the writable
        # build target; the siblings are read-only. go.work.container is overlaid as
        # the root go.work, found by walking up from the ctxloom module.
        shared_dir="$(cd ../../shared/main && pwd)"
        claude_dir="$(cd ../../claude/main && pwd)"
        gemini_dir="$(cd ../../gemini/main && pwd)"
        codex_dir="$(cd ../../codex/main && pwd)"
        # Reuse the host module cache (read-only) and keep Go's writable caches
        # in the container tmpdir. Without this, HOME/GOCACHE resolve under the
        # cwd and the build spills a .cache/ into the source tree (and re-downloads
        # every run).
        cache_mount=()
        if [ -d "$HOME/go/pkg/mod" ]; then cache_mount=(-v "$HOME/go/pkg/mod:/tmp/gomodcache:ro"); fi
        {{container_cmd}} run --rm \
            "${user_flag[@]}" \
            "${cache_mount[@]}" \
            -e HOME=/tmp \
            -e GOMODCACHE=/tmp/gomodcache \
            -e GOCACHE=/tmp/.gocache \
            -v "$(pwd):/workspace/ctxloom/main" \
            -v "$shared_dir:/workspace/shared/main:ro" \
            -v "$claude_dir:/workspace/claude/main:ro" \
            -v "$gemini_dir:/workspace/gemini/main:ro" \
            -v "$codex_dir:/workspace/codex/main:ro" \
            -v "$(pwd)/go.work.container:/workspace/go.work:ro" \
            -v "$(pwd)/justfile.container:/workspace/ctxloom/main/justfile:ro" \
            -w /workspace/ctxloom/main \
            {{devcontainer_image}}:latest \
            just {{ARGS}}
    fi

# Build with all CGO features (static, inside devcontainer)
dev-build: dev-image
    just _run build

# Build with ONNX support (static, inside devcontainer)
dev-build-onnx: dev-image
    just _run build-onnx

# Build with tree-sitter (static, inside devcontainer)
dev-build-treesitter: dev-image
    just _run build-treesitter

# Build with all features (static, inside devcontainer)
dev-build-full: dev-image
    just _run build-full

# Run treesitter (CGO) tests inside devcontainer
dev-test-treesitter: dev-image
    just _run test-treesitter

# Run any target inside devcontainer
dev +ARGS: dev-image
    just _run {{ARGS}}

# Shell into devcontainer for debugging
dev-shell: dev-image
    {{container_cmd}} run --rm -it \
        --user "$(id -u):$(id -g)" \
        -v "$(pwd):/workspace" \
        -v "$(pwd)/justfile.container:/workspace/justfile:ro" \
        -w /workspace \
        {{devcontainer_image}}:latest \
        bash
