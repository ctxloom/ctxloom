# Default recipe
default: build

# Get version from versionator
# Format: v0.0.1-abc1234.dirty (uncommitted) or v0.0.1-abc1234 (clean)
version := `versionator version -t "{{Prefix}}{{MajorMinorPatch}}{{PreReleaseWithDash}}" --prerelease="{{ShortHash}}{{DirtyWithDot}}"`

# Validate fragment YAML files against JSON schema
validate:
    go run ./cmd/validate

# Distill resources before packaging
distill-resources:
    go run . distill --resources

# Build all binaries (main app + generators)
build: validate distill-resources build-scm build-generators

# Build the main binary
build-scm:
    go build -ldflags "-X github.com/benjaminabbitt/scm/cmd.Version={{version}}" -o scm .

# Build all generators
build-generators: build-simple

# Build simple wrapper generator
build-simple:
    go build -o bin/scm-gen-simple ./cmd/generators/simple

# Build with verbose output
build-verbose:
    go build -v -ldflags "-X github.com/benjaminabbitt/scm/cmd.Version={{version}}" -o scm .
    go build -v -o bin/scm-gen-simple ./cmd/generators/simple

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
        mkdir -p bin && \
        CGO_ENABLED=0 go build -o bin/scm-gen-simple ./cmd/generators/simple && \
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

# Build and install to ~/.local/bin
install: build-static
    mkdir -p ~/.local/bin
    -pkill -x scm && sleep 0.5
    cp scm ~/.local/bin/
    cp bin/scm-gen-* ~/.local/bin/

# Uninstall from ~/.local/bin
uninstall:
    rm -f ~/.local/bin/scm
    rm -f ~/.local/bin/scm-gen-*

# Build static binaries
build-static: validate distill-resources
    CGO_ENABLED=0 go build -ldflags="-s -w -X github.com/benjaminabbitt/scm/cmd.Version={{version}}" -o scm .
    CGO_ENABLED=0 go build -ldflags="-s -w" -o bin/scm-gen-simple ./cmd/generators/simple

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

# Code review with reviewer persona
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
