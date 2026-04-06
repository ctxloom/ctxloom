# Stage 1: Get osxcross toolchain
FROM crazymax/osxcross:latest-ubuntu AS osxcross

# Stage 2: Main build image
# Use Ubuntu 24.04 for glibc compatibility with osxcross
FROM ubuntu:24.04

# Install build dependencies
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
    && rm -rf /var/lib/apt/lists/*

# Install Go
ARG GO_VERSION=1.24.4
RUN curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz" | tar -C /usr/local -xzf -
ENV PATH="/usr/local/go/bin:$PATH"
ENV GOPATH="/go"
ENV PATH="${GOPATH}/bin:${PATH}"

# Install UPX from GitHub releases
ARG UPX_VERSION=4.2.4
RUN wget -qO- "https://github.com/upx/upx/releases/download/v${UPX_VERSION}/upx-${UPX_VERSION}-amd64_linux.tar.xz" \
    | tar -xJf - --strip-components=1 -C /usr/local/bin "upx-${UPX_VERSION}-amd64_linux/upx"

# Copy osxcross toolchain from first stage
COPY --from=osxcross /osxcross /osxcross
ENV PATH="/osxcross/bin:$PATH"
ENV LD_LIBRARY_PATH="/osxcross/lib"

# Note: tokenizers library removed - using pure Go sugarme/tokenizer instead

# Install ONNX Runtime (for embedding into binary)
ARG ONNX_VERSION=1.20.1
RUN ARCH=$(dpkg --print-architecture) && \
    case "$ARCH" in \
        amd64) ONNX_ARCH="x64" ;; \
        arm64) ONNX_ARCH="aarch64" ;; \
        *) echo "Unsupported architecture: $ARCH" && exit 1 ;; \
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

# Install golangci-lint
RUN curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b /usr/local/bin

# Install buf for protobuf
RUN BIN="/usr/local/bin" && \
    VERSION="1.47.2" && \
    curl -sSL "https://github.com/bufbuild/buf/releases/download/v${VERSION}/buf-$(uname -s)-$(uname -m)" -o "${BIN}/buf" && \
    chmod +x "${BIN}/buf"

# Install gremlins for mutation testing
RUN go install github.com/go-gremlins/gremlins/cmd/gremlins@v0.6.0

# Install versionator for version management
RUN go install github.com/benjaminabbitt/versionator@v0.1.1

# Runtime module/build cache in workspace (writable by any user, persists across runs)
ENV GOMODCACHE="/workspace/.cache/go/mod"
ENV GOCACHE="/workspace/.cache/go/build"
ENV GOTOOLCHAIN="local"

# Set working directory
WORKDIR /workspace

# Mark as devcontainer (for detection in justfile)
ENV DEVCONTAINER=1

# Default command
CMD ["bash"]
