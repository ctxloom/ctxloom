# Default recipe
default: build

# Get version from versionator (with v prefix)
version := `versionator version -t "{{Prefix}}{{MajorMinorPatch}}"`

# Build all binaries (main app + generators + server)
build: build-mlcm build-generators server-build

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

# Clean build artifacts
clean:
    rm -f mlcm
    rm -rf bin/
    rm -rf server/proto/fragmentspb/
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
build-static:
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

# ===== Server targets =====

# Build the server binary
server-build:
    CGO_ENABLED=0 go build -ldflags="-s -w" -trimpath -o bin/scm-server ./server/cmd/server

# Generate protobuf code (requires buf)
server-proto:
    cd server && buf generate

# Build server Docker image
server-docker tag="scm-server:latest":
    docker build -t {{tag}} -f server/Dockerfile .

# Run server locally (requires storage backend env vars)
server-run:
    go run ./server/cmd/server

# Run server with local MongoDB
server-run-mongo:
    STORAGE_TYPE=mongodb MONGODB_URI=mongodb://localhost:27017 go run ./server/cmd/server

# Run server with local DynamoDB
server-run-dynamo:
    STORAGE_TYPE=dynamodb DYNAMODB_ENDPOINT=http://localhost:8000 AWS_REGION=us-east-1 go run ./server/cmd/server

# Deploy to Cloud Run (requires gcloud auth)
server-deploy-cloudrun project region="us-central1":
    gcloud run deploy scm-server \
        --image gcr.io/{{project}}/scm-server \
        --region {{region}} \
        --platform managed \
        --allow-unauthenticated

# Push server image to GCR
server-push project:
    docker tag scm-server:latest gcr.io/{{project}}/scm-server
    docker push gcr.io/{{project}}/scm-server

# Full server build and push
server-release project: server-proto server-docker
    just server-push {{project}}

# Test server storage implementations
server-test:
    go test ./server/...

# Build Lambda deployment package
lambda-build:
    GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags="-s -w" -o bin/bootstrap ./server/cmd/lambda
    cd bin && zip -j lambda.zip bootstrap

# Build Lambda for ARM64 (Graviton)
lambda-build-arm:
    GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -ldflags="-s -w" -o bin/bootstrap ./server/cmd/lambda
    cd bin && zip -j lambda.zip bootstrap

# Deploy Lambda function (requires AWS CLI configured)
lambda-deploy function_name:
    aws lambda update-function-code --function-name {{function_name}} --zip-file fileb://bin/lambda.zip

# Create new Lambda function with DynamoDB
lambda-create function_name role_arn:
    aws lambda create-function \
        --function-name {{function_name}} \
        --runtime provided.al2023 \
        --handler bootstrap \
        --zip-file fileb://bin/lambda.zip \
        --role {{role_arn}} \
        --environment Variables="{STORAGE_TYPE=dynamodb}"

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

# Build and push Docker image to Artifact Registry
tf-docker-push project region="us-central1":
    gcloud auth configure-docker {{region}}-docker.pkg.dev
    docker build -t {{region}}-docker.pkg.dev/{{project}}/mlcm/mlcm-fragment-server:latest -f server/Dockerfile .
    docker push {{region}}-docker.pkg.dev/{{project}}/mlcm/mlcm-fragment-server:latest

# Full GCP deployment: init, build, push, apply
tf-deploy project region="us-central1":
    just tf-init
    just tf-docker-push {{project}} {{region}}
    just tf-apply
