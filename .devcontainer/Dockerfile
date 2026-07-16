# Stage 1: Get osxcross toolchain
FROM crazymax/osxcross:latest-ubuntu AS osxcross

# Stage 2: Main build image
# Use Ubuntu 24.04 for glibc compatibility with osxcross
FROM ubuntu:24.04

# Install build dependencies. docker-ce-cli (client only, no daemon) lets
# `just test-docker-integration` reach the host/CI runner's Docker daemon —
# GitHub-hosted runners bind-mount /var/run/docker.sock into job containers,
# so the CLI is the only thing missing to actually exercise
# internal/lm/isolation's docker-gated container_integration_test.go /
# container_worktree_integration_test.go (see justfile test-docker-integration).
# Those tests self-skip when docker is unreachable, so this is a "make it
# possible to pass" addition, not a hard requirement.
RUN apt-get update && apt-get install -y \
    build-essential \
    git \
    curl \
    wget \
    ca-certificates \
    musl-tools \
    musl-dev \
    unzip \
    gcc-mingw-w64-x86-64 \
    gcc-aarch64-linux-gnu \
    clang \
    lld \
    libc6-dev \
    xz-utils \
    python3 \
    python3-pip \
    && install -m 0755 -d /etc/apt/keyrings \
    && curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc \
    && chmod a+r /etc/apt/keyrings/docker.asc \
    && echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu noble stable" \
        > /etc/apt/sources.list.d/docker.list \
    && apt-get update && apt-get install -y docker-ce-cli \
    && rm -rf /var/lib/apt/lists/*

# TARGETARCH is auto-populated by Docker BuildKit (amd64, arm64, etc.) so the
# same Dockerfile builds on x86 and ARM hosts. Go and UPX both publish their
# linux releases with arch names that match TARGETARCH 1:1.
ARG TARGETARCH

# --- Tool versions ----------------------------------------------------------
# .devcontainer/tool-versions.env is the ONE place these versions are
# declared. These ARGs deliberately have NO default value: a default here
# would recreate a second source of truth that could silently drift from
# that file. `just dev-image` (see justfile) reads the file and passes every
# entry as `--build-arg`; so does ci.yml's build-container job. A bare
# `docker build .` with no --build-arg gets caught by the guard RUN just
# below instead of silently installing an empty version or "latest" —
# ctxloom's characteristic bug is exit 0 with wrong/missing bytes, not a
# loud failure, and this guard exists specifically to not repeat that here.
# internal/buildpins' drift-gate test asserts these ARG names and the
# workflow consumers all agree with tool-versions.env.
ARG GO_VERSION
ARG UPX_VERSION
ARG ONNX_VERSION
ARG GOLANGCI_LINT_VERSION
ARG BUF_VERSION
ARG PROTOC_GEN_GO_VERSION
ARG PROTOC_GEN_GO_GRPC_VERSION
ARG GREMLINS_VERSION
ARG VERSIONATOR_VERSION
ARG GORELEASER_VERSION
ARG LIZARD_VERSION

RUN : "${GO_VERSION:?missing --build-arg GO_VERSION — build via 'just dev-image', which reads .devcontainer/tool-versions.env}" \
    && : "${UPX_VERSION:?missing --build-arg UPX_VERSION — see .devcontainer/tool-versions.env}" \
    && : "${ONNX_VERSION:?missing --build-arg ONNX_VERSION — see .devcontainer/tool-versions.env}" \
    && : "${GOLANGCI_LINT_VERSION:?missing --build-arg GOLANGCI_LINT_VERSION — see .devcontainer/tool-versions.env}" \
    && : "${BUF_VERSION:?missing --build-arg BUF_VERSION — see .devcontainer/tool-versions.env}" \
    && : "${PROTOC_GEN_GO_VERSION:?missing --build-arg PROTOC_GEN_GO_VERSION — see .devcontainer/tool-versions.env}" \
    && : "${PROTOC_GEN_GO_GRPC_VERSION:?missing --build-arg PROTOC_GEN_GO_GRPC_VERSION — see .devcontainer/tool-versions.env}" \
    && : "${GREMLINS_VERSION:?missing --build-arg GREMLINS_VERSION — see .devcontainer/tool-versions.env}" \
    && : "${VERSIONATOR_VERSION:?missing --build-arg VERSIONATOR_VERSION — see .devcontainer/tool-versions.env}" \
    && : "${GORELEASER_VERSION:?missing --build-arg GORELEASER_VERSION — see .devcontainer/tool-versions.env}" \
    && : "${LIZARD_VERSION:?missing --build-arg LIZARD_VERSION — see .devcontainer/tool-versions.env}"

# Install Go
RUN curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${TARGETARCH}.tar.gz" | tar -C /usr/local -xzf -
ENV PATH="/usr/local/go/bin:$PATH"
ENV GOPATH="/go"
ENV PATH="${GOPATH}/bin:${PATH}"

# Install UPX from GitHub releases
RUN wget -qO- "https://github.com/upx/upx/releases/download/v${UPX_VERSION}/upx-${UPX_VERSION}-${TARGETARCH}_linux.tar.xz" \
    | tar -xJf - --strip-components=1 -C /usr/local/bin "upx-${UPX_VERSION}-${TARGETARCH}_linux/upx"

# Copy osxcross toolchain from first stage
COPY --from=osxcross /osxcross /osxcross
ENV PATH="/osxcross/bin:$PATH"
ENV LD_LIBRARY_PATH="/osxcross/lib"

# Note: tokenizers library removed - using pure Go sugarme/tokenizer instead

# Install ONNX Runtime (for embedding into binary)
RUN case "${TARGETARCH}" in \
        amd64) ONNX_ARCH="x64" ;; \
        arm64) ONNX_ARCH="aarch64" ;; \
        *) echo "Unsupported architecture: ${TARGETARCH}" && exit 1 ;; \
    esac && \
    wget -q "https://github.com/microsoft/onnxruntime/releases/download/v${ONNX_VERSION}/onnxruntime-linux-${ONNX_ARCH}-${ONNX_VERSION}.tgz" -O /tmp/onnxruntime.tgz && \
    tar -xzf /tmp/onnxruntime.tgz -C /opt && \
    ln -s /opt/onnxruntime-linux-${ONNX_ARCH}-${ONNX_VERSION} /opt/onnxruntime && \
    rm /tmp/onnxruntime.tgz

# CGO flags for ONNX runtime (loaded via dlopen at runtime)
# Note: tokenizers is now pure Go (sugarme/tokenizer) - no CGO needed
ENV CGO_LDFLAGS="-ldl -lm -pthread"

# Copy ONNX runtime .so for embedding into binary
RUN mkdir -p /opt/onnxruntime-embed && \
    cp /opt/onnxruntime/lib/libonnxruntime.so.${ONNX_VERSION} /opt/onnxruntime-embed/libonnxruntime.so

# Install just (command runner)
RUN curl --proto '=https' --tlsv1.2 -sSf https://just.systems/install.sh | bash -s -- --to /usr/local/bin

# Install lizard (cross-language per-function complexity analyzer). Ubuntu 24.04
# marks its Python env externally-managed (PEP 668), so --break-system-packages
# is required; the console script lands in /usr/local/bin (already on PATH).
RUN pip3 install --break-system-packages --no-cache-dir lizard==${LIZARD_VERSION}

# Install golangci-lint via `go install` so the build does not depend on
# golangci/golangci-lint's install.sh, whose embedded sha256 has been seen to
# disagree with the tarball bytes served from GitHub releases (v2.12.2 at the
# time of writing). `go install` uses the Go module checksum database, which
# is the more reliable verification path. Binary lands in $GOPATH/bin (/go/bin),
# already on PATH from the Go install step above.
RUN go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v${GOLANGCI_LINT_VERSION}

# Install buf for protobuf
RUN curl -sSL "https://github.com/bufbuild/buf/releases/download/v${BUF_VERSION}/buf-$(uname -s)-$(uname -m)" -o /usr/local/bin/buf && \
    chmod +x /usr/local/bin/buf

# Codegen plugins installed LOCALLY so `buf generate` never executes remote
# plugins on the BSR — remote plugin execution is rate-limited and made the
# build non-deterministic (a throttled `buf generate` failed the whole
# pipeline). Versions pinned to match the output these plugins were producing
# remotely so generated code stays byte-identical. buf.gen.yaml references
# these as `local:` plugins.
RUN go install google.golang.org/protobuf/cmd/protoc-gen-go@v${PROTOC_GEN_GO_VERSION} && \
    go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v${PROTOC_GEN_GO_GRPC_VERSION}

# Install gremlins for mutation testing
RUN go install github.com/go-gremlins/gremlins/cmd/gremlins@v${GREMLINS_VERSION}

# Install versionator for version management
RUN go install github.com/benjaminabbitt/versionator@v${VERSIONATOR_VERSION}

# GoReleaser: `just release-check` / `just release-snapshot` run here, never on
# the host. The host has no upx and no cross toolchain, so a host snapshot
# silently emits "-upx" artifacts that were never compressed. CI releases from
# goreleaser-cross (see .github/workflows/release-completer.yml), which bundles
# its own goreleaser — this pin only governs local validation.
RUN go install github.com/goreleaser/goreleaser/v2@v${GORELEASER_VERSION}

# Go caches live under GOPATH, NEVER under /workspace. /workspace is the
# bind-mounted source tree: caches written there land inside the Go module
# directory itself, where any tool that copies the module (gremlins mutation
# testing copies it per worker) drags in gigabytes of read-only module cache and
# fails. Persistence across rebuilds comes from the named volumes mounted on
# these paths (see .devcontainer/devcontainer.json), not from the source mount.
# Mode 0777 keeps them writable whichever uid the container is run as.
ENV GOMODCACHE="/go/pkg/mod"
ENV GOCACHE="/go/cache/build"
ENV GOTOOLCHAIN="local"
RUN mkdir -p "${GOMODCACHE}" "${GOCACHE}" && chmod -R 0777 /go

# Set working directory
WORKDIR /workspace

# Mark as devcontainer (for detection in justfile)
ENV DEVCONTAINER=1

# Default command
CMD ["bash"]
