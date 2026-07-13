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

# Install Go
ARG GO_VERSION=1.25.10
RUN curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${TARGETARCH}.tar.gz" | tar -C /usr/local -xzf -
ENV PATH="/usr/local/go/bin:$PATH"
ENV GOPATH="/go"
ENV PATH="${GOPATH}/bin:${PATH}"

# Install UPX from GitHub releases
ARG UPX_VERSION=4.2.4
RUN wget -qO- "https://github.com/upx/upx/releases/download/v${UPX_VERSION}/upx-${UPX_VERSION}-${TARGETARCH}_linux.tar.xz" \
    | tar -xJf - --strip-components=1 -C /usr/local/bin "upx-${UPX_VERSION}-${TARGETARCH}_linux/upx"

# Copy osxcross toolchain from first stage
COPY --from=osxcross /osxcross /osxcross
ENV PATH="/osxcross/bin:$PATH"
ENV LD_LIBRARY_PATH="/osxcross/lib"

# Note: tokenizers library removed - using pure Go sugarme/tokenizer instead

# Install ONNX Runtime (for embedding into binary)
ARG ONNX_VERSION=1.20.1
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
    cp /opt/onnxruntime/lib/libonnxruntime.so.1.20.1 /opt/onnxruntime-embed/libonnxruntime.so

# Install just (command runner)
RUN curl --proto '=https' --tlsv1.2 -sSf https://just.systems/install.sh | bash -s -- --to /usr/local/bin

# Install lizard (cross-language per-function complexity analyzer). Ubuntu 24.04
# marks its Python env externally-managed (PEP 668), so --break-system-packages
# is required; the console script lands in /usr/local/bin (already on PATH).
RUN pip3 install --break-system-packages --no-cache-dir lizard==1.22.2

# Install golangci-lint via `go install` so the build does not depend on
# golangci/golangci-lint's install.sh, whose embedded sha256 has been seen to
# disagree with the tarball bytes served from GitHub releases (v2.12.2 at the
# time of writing). `go install` uses the Go module checksum database, which
# is the more reliable verification path. Binary lands in $GOPATH/bin (/go/bin),
# already on PATH from the Go install step above.
RUN go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.1

# Install buf for protobuf
RUN BIN="/usr/local/bin" && \
    VERSION="1.47.2" && \
    curl -sSL "https://github.com/bufbuild/buf/releases/download/v${VERSION}/buf-$(uname -s)-$(uname -m)" -o "${BIN}/buf" && \
    chmod +x "${BIN}/buf"

# Install gremlins for mutation testing
RUN go install github.com/go-gremlins/gremlins/cmd/gremlins@v0.6.0

# Install versionator for version management
RUN go install github.com/benjaminabbitt/versionator@v0.2.1

# GoReleaser: `just release-check` / `just release-snapshot` run here, never on
# the host. The host has no upx and no cross toolchain, so a host snapshot
# silently emits "-upx" artifacts that were never compressed. CI releases from
# goreleaser-cross (see .github/workflows/release-completer.yml), which bundles
# its own goreleaser — this pin only governs local validation.
RUN go install github.com/goreleaser/goreleaser/v2@v2.16.0

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
