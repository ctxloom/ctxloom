# Default recipe
default: build

# Get version from versionator
# Format: v0.0.1-abc1234.dirty (uncommitted) or v0.0.1-abc1234 (clean)
version := `versionator version -t "{{Prefix}}{{MajorMinorPatch}}{{PreReleaseWithDash}}" --prerelease="{{ShortHash}}{{DirtyWithDot}}"`

# Validate fragment YAML files against JSON schema
validate:
    go run ./cmd/validate

# Build all binaries (main app + plugins)
build: validate proto build-scm

# Build the main binary
build-scm:
    go build -ldflags "-X github.com/benjaminabbitt/scm/cmd.Version={{version}}" -o scm .

# ===== Plugin targets =====

# Generate protobuf code
proto:
    protoc --go_out=. --go_opt=paths=source_relative \
           --go-grpc_out=. --go-grpc_opt=paths=source_relative \
           internal/lm/grpc/plugin.proto

# Check protoc is installed
proto-check:
    @which protoc > /dev/null || (echo "protoc not installed. Install with: brew install protobuf" && exit 1)
    @which protoc-gen-go > /dev/null || (echo "protoc-gen-go not installed. Install with: go install google.golang.org/protobuf/cmd/protoc-gen-go@latest" && exit 1)
    @which protoc-gen-go-grpc > /dev/null || (echo "protoc-gen-go-grpc not installed. Install with: go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest" && exit 1)

# Install protobuf tools
proto-tools:
    go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
    go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest

# List available plugins
plugin-list:
    ./scm plugin list

# ===== End plugin targets =====

# Build with verbose output
build-verbose:
    go build -v -ldflags "-X github.com/benjaminabbitt/scm/cmd.Version={{version}}" -o scm .

# Run tests
test:
    go test ./...

# Run tests with verbose output
test-verbose:
    go test -v ./...

# Run tests with coverage
test-coverage:
    go test -cover ./...

# Run acceptance tests (requires scm binary)
test-acceptance: build-scm
    go test -v ./tests/acceptance/...

# Run acceptance tests with specific tags
test-acceptance-tags TAGS:
    go test -v ./tests/acceptance/... -godog.tags="{{TAGS}}"

# Run all tests in container (matches CI environment)
test-container:
    docker run --rm -v "$(pwd):/app" -w /app golang:1.24 sh -c '\
        go mod download && \
        go test -race ./... && \
        CGO_ENABLED=0 go build -o scm . && \
        go test -v ./tests/acceptance/...'

# Clean build artifacts
clean:
    rm -f scm
    rm -rf bin/
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

# Build and install to ~/go/bin (default Go binary location)
install: build
    mkdir -p ~/go/bin
    -rm -f ~/go/bin/scm 2>/dev/null
    cp scm ~/go/bin/

# Build static and install to ~/.local/bin
install-local: build-static
    mkdir -p ~/.local/bin
    -pkill -x scm && sleep 0.5
    cp scm ~/.local/bin/

# Build compressed and install to ~/.local/bin
install-compressed: build-compressed
    mkdir -p ~/.local/bin
    -pkill -x scm && sleep 0.5
    cp scm ~/.local/bin/

# Uninstall from ~/.local/bin
uninstall:
    rm -f ~/.local/bin/scm

# Build static binaries
build-static: validate proto
    CGO_ENABLED=0 go build -ldflags="-s -w -X github.com/benjaminabbitt/scm/cmd.Version={{version}}" -o scm .

# Compress binaries with UPX (requires upx installed)
compress:
    @which upx > /dev/null || (echo "upx not installed. Install with: brew install upx (macOS) or apt install upx (Linux)" && exit 1)
    upx --best --lzma scm

# Build static binaries with UPX compression
build-compressed: build-static compress

# Show help
help:
    ./scm --help

# Initialize .scm directory
init:
    ./scm init

# Dry run with test fragments
dry-run PROMPT:
    ./scm run -f test-fragment -f additional-context -n "{{PROMPT}}"

# Run with Gemini plugin
gemini *ARGS:
    ./scm -P gemini {{ARGS}}

# Run with Claude plugin (default)
claude *ARGS:
    ./scm -P claude-code {{ARGS}}

# Code review with reviewer profile
review *ARGS:
    ./scm -p reviewer -r code-review {{ARGS}}

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
    podman build -t {{registry}}/scm-agent-base:latest \
        -f container/{{variant}}/Containerfile-base container/{{variant}}/

# Build Claude Code agent container
container-build-claude: container-build-base
    podman build -t {{registry}}/scm-agent-claude:latest \
        --build-arg BASE_IMAGE={{registry}}/scm-agent-base:latest \
        -f container/{{variant}}/Containerfile-claude-code container/{{variant}}/

# Build Gemini CLI agent container
container-build-gemini: container-build-base
    podman build -t {{registry}}/scm-agent-gemini:latest \
        --build-arg BASE_IMAGE={{registry}}/scm-agent-base:latest \
        -f container/{{variant}}/Containerfile-gemini container/{{variant}}/

# Build Codex agent container
container-build-codex: container-build-base
    podman build -t {{registry}}/scm-agent-codex:latest \
        --build-arg BASE_IMAGE={{registry}}/scm-agent-base:latest \
        -f container/{{variant}}/Containerfile-codex container/{{variant}}/

# Build Cline agent container
container-build-cline: container-build-base
    podman build -t {{registry}}/scm-agent-cline:latest \
        --build-arg BASE_IMAGE={{registry}}/scm-agent-base:latest \
        -f container/{{variant}}/Containerfile-cline container/{{variant}}/

# Build Aider agent container (standalone - Python)
container-build-aider:
    podman build -t {{registry}}/scm-agent-aider:latest \
        -f container/{{variant}}/Containerfile-aider container/{{variant}}/

# Build Goose agent container (standalone - Block)
container-build-goose:
    podman build -t {{registry}}/scm-agent-goose:latest \
        -f container/{{variant}}/Containerfile-goose container/{{variant}}/

# Build Q Developer agent container (standalone - Amazon)
container-build-qdeveloper:
    podman build -t {{registry}}/scm-agent-qdeveloper:latest \
        -f container/{{variant}}/Containerfile-qdeveloper container/{{variant}}/

# Build all agent containers
container-build-agents: container-build-claude container-build-gemini container-build-codex container-build-cline container-build-aider container-build-goose container-build-qdeveloper

# ===== Language LSP containers =====

# Build Go LSP container (gopls + tools)
container-build-lang-go: container-build-base
    podman build -t {{registry}}/scm-lsp-go:latest \
        --build-arg BASE_IMAGE={{registry}}/scm-agent-base:latest \
        -f container/{{variant}}/lang/Containerfile-go container/{{variant}}/

# Build Python LSP container (pyright + tools)
container-build-lang-python: container-build-base
    podman build -t {{registry}}/scm-lsp-python:latest \
        --build-arg BASE_IMAGE={{registry}}/scm-agent-base:latest \
        -f container/{{variant}}/lang/Containerfile-python container/{{variant}}/

# Build Rust LSP container (rust-analyzer + tools)
container-build-lang-rust: container-build-base
    podman build -t {{registry}}/scm-lsp-rust:latest \
        --build-arg BASE_IMAGE={{registry}}/scm-agent-base:latest \
        -f container/{{variant}}/lang/Containerfile-rust container/{{variant}}/

# Build TypeScript LSP container (typescript-language-server)
container-build-lang-typescript: container-build-base
    podman build -t {{registry}}/scm-lsp-typescript:latest \
        --build-arg BASE_IMAGE={{registry}}/scm-agent-base:latest \
        -f container/{{variant}}/lang/Containerfile-typescript container/{{variant}}/

# Build Java LSP container (jdtls + tools)
container-build-lang-java: container-build-base
    podman build -t {{registry}}/scm-lsp-java:latest \
        --build-arg BASE_IMAGE={{registry}}/scm-agent-base:latest \
        -f container/{{variant}}/lang/Containerfile-java container/{{variant}}/

# Build C# LSP container (omnisharp)
container-build-lang-csharp: container-build-base
    podman build -t {{registry}}/scm-lsp-csharp:latest \
        --build-arg BASE_IMAGE={{registry}}/scm-agent-base:latest \
        -f container/{{variant}}/lang/Containerfile-csharp container/{{variant}}/

# Build all language LSP containers
container-build-langs: container-build-lang-go container-build-lang-python container-build-lang-rust container-build-lang-typescript container-build-lang-java container-build-lang-csharp

# Build all containers (base + langs + agents)
container-build-all: container-build-langs container-build-agents

# Push all agent containers to registry
container-push-agents:
    podman push {{registry}}/scm-agent-base:latest
    podman push {{registry}}/scm-agent-claude:latest
    podman push {{registry}}/scm-agent-gemini:latest
    podman push {{registry}}/scm-agent-codex:latest
    podman push {{registry}}/scm-agent-cline:latest
    podman push {{registry}}/scm-agent-aider:latest
    podman push {{registry}}/scm-agent-goose:latest
    podman push {{registry}}/scm-agent-qdeveloper:latest

# Push all language LSP containers to registry
container-push-langs:
    podman push {{registry}}/scm-lsp-go:latest
    podman push {{registry}}/scm-lsp-python:latest
    podman push {{registry}}/scm-lsp-rust:latest
    podman push {{registry}}/scm-lsp-typescript:latest
    podman push {{registry}}/scm-lsp-java:latest
    podman push {{registry}}/scm-lsp-csharp:latest

# Push all containers to registry
container-push-all: container-push-langs container-push-agents

# Clean agent container images
container-clean-agents:
    -podman rmi {{registry}}/scm-agent-claude:latest
    -podman rmi {{registry}}/scm-agent-gemini:latest
    -podman rmi {{registry}}/scm-agent-codex:latest
    -podman rmi {{registry}}/scm-agent-cline:latest
    -podman rmi {{registry}}/scm-agent-aider:latest
    -podman rmi {{registry}}/scm-agent-goose:latest
    -podman rmi {{registry}}/scm-agent-qdeveloper:latest

# Clean language LSP container images
container-clean-langs:
    -podman rmi {{registry}}/scm-lsp-go:latest
    -podman rmi {{registry}}/scm-lsp-python:latest
    -podman rmi {{registry}}/scm-lsp-rust:latest
    -podman rmi {{registry}}/scm-lsp-typescript:latest
    -podman rmi {{registry}}/scm-lsp-java:latest
    -podman rmi {{registry}}/scm-lsp-csharp:latest

# Clean all container images
container-clean: container-clean-agents container-clean-langs
    -podman rmi {{registry}}/scm-agent-base:latest

# List all scm container images
container-list:
    @podman images | grep -E "scm-(agent|lsp)" | sort
