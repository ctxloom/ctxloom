# Default recipe
default: build

# Get version from versionator (with fallback for CI without versionator)
# Format: v0.0.1-abc1234.dirty (uncommitted) or v0.0.1-abc1234 (clean)
version := `versionator version -t "{{Prefix}}{{MajorMinorPatch}}{{PreReleaseWithDash}}" --prerelease="{{ShortHash}}{{DirtyWithDot}}" 2>/dev/null || echo "dev"`

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

# Run all tests (builds ctxloom first for acceptance tests)
test: build
    go test -race -coverprofile=coverage.out ./...

# Run tests with verbose output
test-verbose:
    go test -v ./...

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

# Run integration tests (requires ctxloom binary)
test-integration: build
    go test -v -tags integration ./tests/integration/...

# Run all tests in container (matches CI environment)
test-container:
    docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd):/app" -w /app golang:1.26 sh -c '\
        go mod download && \
        go test -race ./... && \
        CGO_ENABLED=0 go build -ldflags "-X github.com/ctxloom/ctxloom/cmd.Version={{version}}" -o ctxloom . && \
        go test -v -tags integration ./tests/integration/...'

# ===== Mutation testing =====

# Run mutation tests with gremlins (requires gremlins installed)
mutate *ARGS:
    gremlins unleash {{ARGS}}

# Run mutation tests on specific package
mutate-pkg PKG *ARGS:
    gremlins unleash ./{{PKG}}/... {{ARGS}}

# Install gremlins
mutate-install:
    go install github.com/go-gremlins/gremlins/cmd/gremlins@v0.6.0

# Run mutation tests in container
mutate-container:
    docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd):/app" -w /app gogremlins/gremlins gremlins unleash

# Clean build artifacts
clean:
    rm -f ctxloom scm
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

# Lint code (requires golangci-lint)
lint:
    golangci-lint run

# Run the CLI
run *ARGS:
    go run . {{ARGS}}

# Build, compress, and install to ~/go/bin (standard Go location)
install: build-compressed
    mkdir -p ~/go/bin
    -pkill -x ctxloom && sleep 0.5
    cp ctxloom ~/go/bin/

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
        # Run in container with justfile overlay and uid/gid mapping
        {{container_cmd}} run --rm \
            --user "$(id -u):$(id -g)" \
            -v "$(pwd):/workspace" \
            -v "$(pwd)/justfile.container:/workspace/justfile:ro" \
            -w /workspace \
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

# Run tests with ONNX (inside devcontainer)
dev-test-onnx: dev-image
    just _run test-onnx

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
