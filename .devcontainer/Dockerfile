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
# tmux is a TEST DEPENDENCY, not a convenience. internal/tmuxhost's tests drive a
# real tmux server, and without the binary they SKIP rather than fail — so the
# package reports ok, the suite goes green, and the tmux-backed behaviour is
# never exercised at all. That is how three of four rename mutations survived a
# green gate, and how an acceptance scenario ran red in this image while passing
# on a host that happened to have tmux installed. The acceptance image inherits
# from this one (acceptance.Dockerfile is FROM ${BASE_IMAGE}), so this line is
# what makes the gate mean the same thing in both places.
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
    tmux \
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
ARG JUST_VERSION
ARG GOPLS_VERSION

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
    && : "${LIZARD_VERSION:?missing --build-arg LIZARD_VERSION — see .devcontainer/tool-versions.env}" \
    && : "${JUST_VERSION:?missing --build-arg JUST_VERSION — see .devcontainer/tool-versions.env}" \
    && : "${GOPLS_VERSION:?missing --build-arg GOPLS_VERSION — see .devcontainer/tool-versions.env}"

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

# Install just (command runner). Pinned, like every other tool here: `just`
# is not incidental tooling — it IS the CI contract now that every workflow
# step is a `just` target, so the runner bootstrap
# (.github/actions/setup-just) and this image must install the same version.
RUN curl --proto '=https' --tlsv1.2 -sSf https://just.systems/install.sh \
    | bash -s -- --tag "${JUST_VERSION}" --to /usr/local/bin

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

# serena — the code-intelligence MCP server (find_symbol,
# find_referencing_symbols, the symbol-edit tools). It is here as well as in
# container/base/Containerfile because THIS repo never uses that base: an
# auto-detected devcontainer wins in resolveDevBase's candidate order, so an
# agent on `runtime: container-rootless` here builds from this file. A
# container-bound agent without serena loses those tools SILENTLY — it does not
# know they are missing and falls back to grep.
#
# gopls is the actual dependency, not an extra: serena's Go support shells
# `go version` AND `gopls version` and refuses to start its language server
# without both. Without it serena errors loudly rather than answering from a
# degraded index — safe, but useless on a Go repo. It is a plain `go install`
# here because the Go toolchain is already present above; that is precisely why
# this image was chosen over ctxloom's shared base, which has no Go.
RUN go install golang.org/x/tools/gopls@v${GOPLS_VERSION}

# uv is copied from its own published image rather than curl-piped, so the
# installer version is pinned. It is pinned INLINE, not via a build ARG like the
# tools above: BuildKit rejects variable expansion in `COPY --from`, so an ARG
# here would be a pin that does not bind. serena-agent itself is unpinned,
# matching container/base/Containerfile.
COPY --from=ghcr.io/astral-sh/uv:0.9.22 /uv /usr/local/bin/uv

# The tool venv and its shims go under /opt + /usr/local/bin, not a user home:
# this image is run under several uids and anything under /root would be
# unreachable by the process that actually runs the engine.
# The gate is `serena --help`, which imports the package and exits 0 — a shim
# that merely EXISTS proves nothing.
ENV UV_TOOL_DIR=/opt/uv/tools \
    UV_TOOL_BIN_DIR=/usr/local/bin
RUN UV_NO_CACHE=1 uv tool install --python python3 serena-agent \
    && serena --help >/dev/null

# Set working directory
WORKDIR /workspace

# Mark as devcontainer (for detection in justfile)
ENV DEVCONTAINER=1

# Default command
CMD ["bash"]
