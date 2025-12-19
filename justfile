# Default recipe
default: build

# Build all binaries (main app + generators)
build: build-mlcm build-generators

# Build the main binary
build-mlcm:
    go build -o mlcm .

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
    go build -v -o mlcm .
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

# Build and install to GOPATH/bin
install:
    go install .
    go install ./cmd/generators/git-context
    go install ./cmd/generators/simple

# Show help
help:
    ./mlcm --help

# Initialize local .mlcm directory
init-local:
    ./mlcm init --local

# Dry run with test fragments
dry-run PROMPT:
    ./mlcm run -f test-fragment -f additional-context -n "{{PROMPT}}"

# Test git-context generator
test-generator:
    ./bin/mlcm-gen-git-context
