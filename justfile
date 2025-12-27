# Default recipe
default: build

# Get version from versionator (with v prefix)
version := `versionator version -t "{{Prefix}}{{MajorMinorPatch}}"`

# Validate fragment YAML files against JSON schema
validate:
    go run ./cmd/validate

# Distill resources before packaging
distill-resources:
    go run . distill --resources

# Build all binaries (main app + generators)
build: validate distill-resources build-mlcm build-generators

# Build the main binary
build-mlcm:
    go build -ldflags "-X mlcm/cmd.Version={{version}}" -o mlcm .

# Build all generators
build-generators: build-git-context build-simple

# Build git-context generator
build-git-context:
    go build -o bin/mlcm-gen-git-context ./cmd/generators/git-context

# Build simple wrapper generator
build-simple:
    go build -o bin/mlcm-gen-simple ./cmd/generators/simple

# Build with verbose output
build-verbose:
    go build -v -ldflags "-X mlcm/cmd.Version={{version}}" -o mlcm .
    go build -v -o bin/mlcm-gen-git-context ./cmd/generators/git-context
    go build -v -o bin/mlcm-gen-simple ./cmd/generators/simple

# Run tests
test:
    go test ./...

# Run tests with verbose output
test-verbose:
    go test -v ./...

# Run tests with coverage
test-coverage:
    go test -cover ./...

# Run acceptance tests (requires mlcm binary)
test-acceptance: build-mlcm
    go test -v ./tests/acceptance/...

# Run acceptance tests with specific tags
test-acceptance-tags TAGS:
    go test -v ./tests/acceptance/... -godog.tags="{{TAGS}}"

# Clean build artifacts
clean:
    rm -f mlcm
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
    -pkill -x mlcm && sleep 0.5
    cp mlcm ~/.local/bin/
    cp bin/mlcm-gen-* ~/.local/bin/

# Uninstall from ~/.local/bin
uninstall:
    rm -f ~/.local/bin/mlcm
    rm -f ~/.local/bin/mlcm-gen-*

# Build static binaries
build-static: validate distill-resources
    CGO_ENABLED=0 go build -ldflags="-s -w -X mlcm/cmd.Version={{version}}" -o mlcm .
    CGO_ENABLED=0 go build -ldflags="-s -w" -o bin/mlcm-gen-git-context ./cmd/generators/git-context
    CGO_ENABLED=0 go build -ldflags="-s -w" -o bin/mlcm-gen-simple ./cmd/generators/simple

# Show help
help:
    ./mlcm --help

# Initialize .mlcm directory
init:
    ./mlcm init

# Dry run with test fragments
dry-run PROMPT:
    ./mlcm run -f test-fragment -f additional-context -n "{{PROMPT}}"

# Test git-context generator
test-generator:
    ./bin/mlcm-gen-git-context

# Run with Gemini plugin
gemini *ARGS:
    ./mlcm -P gemini {{ARGS}}

# Run with Claude plugin (default)
claude *ARGS:
    ./mlcm -P claude-code {{ARGS}}

# Code review with reviewer persona
review *ARGS:
    ./mlcm -p reviewer -r code-review {{ARGS}}

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

