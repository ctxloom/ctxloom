# Every image this file builds from or copies out of is pinned by digest,
# `name:tag@sha256:<digest>`: the tag says which release, the digest which
# bytes, and a tag the registry re-points cannot change the build. The digest
# is the multi-arch INDEX's, so each TARGETARCH still resolves its own image.
# internal/shared/buildpins requires one on every FROM and `COPY --from=`.
#
# To bump an image (a new tag) or refresh one (same tag, re-pushed):
#   docker buildx imagetools inspect <name>:<tag>
# and copy the top-level `Digest:` (MediaType ...image.index...) beside the tag.
# scripts/lint-pins refuses a tag change that keeps the old digest; a digest
# refresh under the same tag is allowed, since that is the routine update.

# Stage 1: Get osxcross toolchain. `latest-ubuntu` floats upstream; the digest
# is the index the build was already using when this was pinned.
FROM crazymax/osxcross:latest-ubuntu@sha256:832e027d26eefd7189b9f742d363f629716bc5592e5b1427b11953f903422a64 AS osxcross

# reprise, the duplicate-detection gate lefthook's pre-commit hook runs
# (`reprise check`, lefthook.yml). INVARIANT: this repo's container agents are
# built on this image, and their commits run that hook INSIDE the container — a
# missing reprise fails every commit with exit 127. So the image must carry it.
#
# reprise has published no release, so it is BUILT here, in its own stage, from
# the full commit SHA tool-versions.env pins as REPRISE_REVISION (a commit on a
# pushed ref of github.com/ctxloom/reprise); only the binary reaches the final
# image. Fetched by SHA, the source cannot move under the pin, and --locked
# builds Cargo.lock's dependency versions, which crates.io's checksums verify.
# The checkout is by hand, not `cargo install --git`, so reprise's benchmark
# corpus submodules are never fetched. Once reprise publishes v0.1.0 this stage
# goes: the release asset is downloaded and checksum-verified like `just` below.
# Built on bookworm (glibc 2.36), so the binary runs on ubuntu:24.04's newer
# glibc.
FROM rust:1.96.1-bookworm@sha256:a339861ae23e9abb272cea45dfafde21760d2ce6577a70f8a926153677902663 AS reprise
ARG REPRISE_REVISION
RUN : "${REPRISE_REVISION:?missing --build-arg REPRISE_REVISION — see .devcontainer/tool-versions.env}" \
    && git init -q /src/reprise && cd /src/reprise \
    && git fetch -q --depth 1 https://github.com/ctxloom/reprise "${REPRISE_REVISION}" \
    && git checkout -q FETCH_HEAD \
    && test "$(git rev-parse HEAD)" = "${REPRISE_REVISION}" \
    && cargo build --release --locked --bin reprise \
    && install -m 0755 target/release/reprise /usr/local/bin/reprise \
    && cd / && rm -rf /src/reprise "${CARGO_HOME:-/usr/local/cargo}/registry" \
    && reprise --version

# Stage 2: Main build image
# Use Ubuntu 24.04 for glibc compatibility with osxcross
FROM ubuntu:24.04@sha256:534baea6a22c03a63003dbc8dbe78fe34bc0d7e595d9a9dc9834884ff530eb55

# Install build dependencies. docker-ce-cli (client only, no daemon) lets
# `just test-docker-integration` reach the host/CI runner's Docker daemon —
# GitHub-hosted runners bind-mount /var/run/docker.sock into job containers,
# so the CLI is the only thing missing to actually exercise
# internal/adapters/isolation's docker-gated integration tests (see justfile
# test-docker-integration). Those tests self-skip when docker is unreachable,
# so this is a "make it possible to pass" addition, not a hard requirement.
#
# Docker's apt key is trusted for every package that repository serves, so the
# fetched key must be exactly Docker's before it lands in /etc/apt/keyrings,
# as the git PPA key below is fetched by full fingerprint: the file must hold
# one primary key, and its fingerprint must be DOCKER_APT_FINGERPRINT.
# That value is the fingerprint Docker documented for this key ("9DC8 5822
# 9FC7 DD38 854A E2D8 8D81 803C 0EBF CD88", docs.docker.com/engine/install/
# ubuntu/; docker/docs engine/install/ubuntu.md, until 760bb64ea3cc dropped the
# check from the page in 2021 — the key itself is unchanged since 2017). If
# Docker rotates the key, this fails loudly; take the new fingerprint from
# Docker's own announcement, never from the fetched file. `gpg` is installed
# here to read the key (the node install below uses it too).
ARG DOCKER_APT_FINGERPRINT="9DC858229FC7DD38854AE2D88D81803C0EBFCD88"
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
    gpg \
    && install -m 0755 -d /etc/apt/keyrings \
    && tmp="$(mktemp -d)" \
    && curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o "${tmp}/docker.asc" \
    && fprs="$(GNUPGHOME="${tmp}" gpg --batch --show-keys --with-colons "${tmp}/docker.asc" \
        | awk -F: '$1 == "pub" {p = 1; next} p && $1 == "fpr" {print $10; p = 0}')" \
    && if [ "${fprs}" != "${DOCKER_APT_FINGERPRINT}" ]; then \
        echo "Docker apt key: want exactly ${DOCKER_APT_FINGERPRINT}, fetched: ${fprs:-no key}" >&2; exit 1; \
    fi \
    && install -m 0644 "${tmp}/docker.asc" /etc/apt/keyrings/docker.asc \
    && rm -rf "${tmp}" \
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
# internal/shared/buildpins' drift-gate test asserts these ARG names and the
# workflow consumers all agree with tool-versions.env.
ARG GO_VERSION
ARG GO_SHA256_AMD64
ARG GO_SHA256_ARM64
ARG UPX_VERSION
ARG ONNX_VERSION
ARG GOLANGCI_LINT_VERSION
ARG BUF_VERSION
ARG PROTOC_GEN_GO_VERSION
ARG PROTOC_GEN_GO_GRPC_VERSION
ARG GOMUTANTS_VERSION
ARG VERSIONATOR_VERSION
ARG GORELEASER_VERSION
ARG LIZARD_VERSION
ARG JUST_VERSION
ARG JUST_SHA256_AMD64
ARG JUST_SHA256_ARM64
ARG GOPLS_VERSION
ARG SERENA_AGENT_VERSION
ARG GITLEAKS_VERSION
ARG SHFMT_VERSION
ARG LEFTHOOK_VERSION
ARG GOVULNCHECK_VERSION
ARG NODE_VERSION
ARG GIT_MIN_VERSION

RUN : "${GO_VERSION:?missing --build-arg GO_VERSION — build via 'just dev-image', which reads .devcontainer/tool-versions.env}" \
    && : "${GO_SHA256_AMD64:?missing --build-arg GO_SHA256_AMD64 — see .devcontainer/tool-versions.env}" \
    && : "${GO_SHA256_ARM64:?missing --build-arg GO_SHA256_ARM64 — see .devcontainer/tool-versions.env}" \
    && : "${UPX_VERSION:?missing --build-arg UPX_VERSION — see .devcontainer/tool-versions.env}" \
    && : "${ONNX_VERSION:?missing --build-arg ONNX_VERSION — see .devcontainer/tool-versions.env}" \
    && : "${GOLANGCI_LINT_VERSION:?missing --build-arg GOLANGCI_LINT_VERSION — see .devcontainer/tool-versions.env}" \
    && : "${BUF_VERSION:?missing --build-arg BUF_VERSION — see .devcontainer/tool-versions.env}" \
    && : "${PROTOC_GEN_GO_VERSION:?missing --build-arg PROTOC_GEN_GO_VERSION — see .devcontainer/tool-versions.env}" \
    && : "${PROTOC_GEN_GO_GRPC_VERSION:?missing --build-arg PROTOC_GEN_GO_GRPC_VERSION — see .devcontainer/tool-versions.env}" \
    && : "${GOMUTANTS_VERSION:?missing --build-arg GOMUTANTS_VERSION — see .devcontainer/tool-versions.env}" \
    && : "${VERSIONATOR_VERSION:?missing --build-arg VERSIONATOR_VERSION — see .devcontainer/tool-versions.env}" \
    && : "${GORELEASER_VERSION:?missing --build-arg GORELEASER_VERSION — see .devcontainer/tool-versions.env}" \
    && : "${LIZARD_VERSION:?missing --build-arg LIZARD_VERSION — see .devcontainer/tool-versions.env}" \
    && : "${JUST_VERSION:?missing --build-arg JUST_VERSION — see .devcontainer/tool-versions.env}" \
    && : "${JUST_SHA256_AMD64:?missing --build-arg JUST_SHA256_AMD64 — see .devcontainer/tool-versions.env}" \
    && : "${JUST_SHA256_ARM64:?missing --build-arg JUST_SHA256_ARM64 — see .devcontainer/tool-versions.env}" \
    && : "${GOPLS_VERSION:?missing --build-arg GOPLS_VERSION — see .devcontainer/tool-versions.env}" \
    && : "${SERENA_AGENT_VERSION:?missing --build-arg SERENA_AGENT_VERSION — see .devcontainer/tool-versions.env}" \
    && : "${GITLEAKS_VERSION:?missing --build-arg GITLEAKS_VERSION — see .devcontainer/tool-versions.env}" \
    && : "${SHFMT_VERSION:?missing --build-arg SHFMT_VERSION — see .devcontainer/tool-versions.env}" \
    && : "${LEFTHOOK_VERSION:?missing --build-arg LEFTHOOK_VERSION — see .devcontainer/tool-versions.env}" \
    && : "${GOVULNCHECK_VERSION:?missing --build-arg GOVULNCHECK_VERSION — see .devcontainer/tool-versions.env}" \
    && : "${NODE_VERSION:?missing --build-arg NODE_VERSION — see .devcontainer/tool-versions.env}" \
    && : "${GIT_MIN_VERSION:?missing --build-arg GIT_MIN_VERSION — see .devcontainer/tool-versions.env}"

# git from the git-core PPA, over the one Ubuntu 24.04 ships: ctxloom registers
# container-side worktrees with `git worktree add --relative-paths`
# (internal/adapters/git RelativePathsFlag), which needs git 2.48+, and the
# tests exercising it against real git can only run where real git has it.
#
# GIT_MIN_VERSION is a FLOOR, not an exact pin like the other tools here, and
# that is forced: the PPA publishes only its newest build per series and
# deletes superseded ones, so an exact `git=<version>` would stop resolving on
# the PPA's next upload. The assertion below is what keeps the floor binding —
# a build that ends up with an older git fails here instead of shipping one.
# The repository is registered by hand rather than with add-apt-repository so
# the image gains no software-properties-common; the key is fetched by its full
# fingerprint (the one Launchpad reports for ~git-core/ppa), so a different
# key cannot be substituted under the same name.
RUN curl -fsSL "https://keyserver.ubuntu.com/pks/lookup?op=get&search=0xF911AB184317630C59970973E363C90F8F1B6217" \
        -o /etc/apt/keyrings/git-core-ppa.asc \
    && echo "deb [signed-by=/etc/apt/keyrings/git-core-ppa.asc] https://ppa.launchpadcontent.net/git-core/ppa/ubuntu noble main" \
        > /etc/apt/sources.list.d/git-core-ppa.list \
    && apt-get update && apt-get install -y git \
    && rm -rf /var/lib/apt/lists/* \
    && installed="$(git --version | awk '{print $3}')" \
    && if [ "$(printf '%s\n%s\n' "${GIT_MIN_VERSION}" "${installed}" | sort -V | head -n1)" != "${GIT_MIN_VERSION}" ]; then \
        echo "git ${installed} is older than GIT_MIN_VERSION=${GIT_MIN_VERSION}" >&2; exit 1; \
    fi

# Install Go. The tarball lands in a file and is checked against the sha256
# tool-versions.env pins for this arch BEFORE it is extracted: `curl | tar`
# would unpack whatever arrived. A mismatch fails the build at sha256sum -c.
# The pins are go.dev's own published checksums, and scripts/lint-pins refuses
# a GO_VERSION bump that does not move them too.
RUN case "${TARGETARCH}" in \
        amd64) GO_SHA256="${GO_SHA256_AMD64}" ;; \
        arm64) GO_SHA256="${GO_SHA256_ARM64}" ;; \
        *) echo "Unsupported architecture: ${TARGETARCH}" >&2; exit 1 ;; \
    esac \
    && tarball="go${GO_VERSION}.linux-${TARGETARCH}.tar.gz" \
    && tmp="$(mktemp -d)" \
    && curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${TARGETARCH}.tar.gz" -o "${tmp}/${tarball}" \
    && echo "${GO_SHA256}  ${tmp}/${tarball}" | sha256sum -c - \
    && tar -C /usr/local -xzf "${tmp}/${tarball}" \
    && rm -rf "${tmp}" \
    && test "$(/usr/local/go/bin/go env GOVERSION)" = "go${GO_VERSION}"
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
#
# The release tarball is downloaded directly and checked against the sha256
# tool-versions.env pins for this arch — casey/just's own SHA256SUMS lines —
# before it is extracted, like Go above. Not just.systems/install.sh piped to
# bash: that executes whatever script the site serves at build time. The musl
# targets are the ones install.sh itself picks for Linux, so the binary is the
# same one it installed.
RUN case "${TARGETARCH}" in \
        amd64) JUST_TARGET="x86_64-unknown-linux-musl"; JUST_SHA256="${JUST_SHA256_AMD64}" ;; \
        arm64) JUST_TARGET="aarch64-unknown-linux-musl"; JUST_SHA256="${JUST_SHA256_ARM64}" ;; \
        *) echo "Unsupported architecture: ${TARGETARCH}" >&2; exit 1 ;; \
    esac \
    && tarball="just-${JUST_VERSION}-${JUST_TARGET}.tar.gz" \
    && tmp="$(mktemp -d)" \
    && curl -fsSL "https://github.com/casey/just/releases/download/${JUST_VERSION}/${tarball}" -o "${tmp}/${tarball}" \
    && echo "${JUST_SHA256}  ${tmp}/${tarball}" | sha256sum -c - \
    && tar -xzf "${tmp}/${tarball}" -C /usr/local/bin just \
    && rm -rf "${tmp}" \
    && test "$(just --version)" = "just ${JUST_VERSION}"

# Install lizard (cross-language per-function complexity analyzer). Ubuntu 24.04
# marks its Python env externally-managed (PEP 668), so --break-system-packages
# is required; the console script lands in /usr/local/bin (already on PATH).
# pip runs in hash-checking mode over lizard-requirements.txt, which pins
# lizard and its dependencies to PyPI's published sha256s: a distribution that
# matches none of them, or a dependency the file does not pin, fails the build.
COPY .devcontainer/lizard-requirements.txt /tmp/lizard-requirements.txt
RUN pip3 install --break-system-packages --no-cache-dir --require-hashes -r /tmp/lizard-requirements.txt \
    && rm /tmp/lizard-requirements.txt \
    && test "$(lizard --version)" = "${LIZARD_VERSION}"

# Install golangci-lint via `go install` so the build does not depend on
# golangci/golangci-lint's install.sh, whose embedded sha256 has been seen to
# disagree with the tarball bytes served from GitHub releases (v2.12.2 at the
# time of writing). `go install` uses the Go module checksum database, which
# is the more reliable verification path. Binary lands in $GOPATH/bin (/go/bin),
# already on PATH from the Go install step above.
RUN go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v${GOLANGCI_LINT_VERSION}

# Install buf for protobuf, verified the way buf's installation docs prescribe
# (https://buf.build/docs/cli/installation/, "Verify a release with minisign"):
# the release's sha256.txt must carry a valid minisign signature from
# BUF_RELEASE_SIGNER, the minisign public key those docs publish, and the binary must
# match its line in that file — all before it is installed. A BUF_VERSION bump
# needs no hash edit: the signed sha256.txt names the hashes, and a release not
# signed by this key fails the build. minisign (Ubuntu universe) is installed
# for the check and purged after it, so the image's toolset does not grow.
# BUF_RELEASE_SIGNER is an ARG with a value, like NODE_RELEASERS below: it is not
# a tool-versions.env pin, and as an ARG it stays out of the container's env.
# It is the key's only copy: build/ci.justfile's release-install-tools reads
# this exact line to verify the buf it installs, so keep it one line.
ARG BUF_RELEASE_SIGNER="RWQ/i9xseZwBVE7pEniCNjlNOeeyp4BQgdZDLQcAohxEAH5Uj5DEKjv6"
RUN case "${TARGETARCH}" in \
        amd64) BUF_ARCH="x86_64" ;; \
        arm64) BUF_ARCH="aarch64" ;; \
        *) echo "Unsupported architecture: ${TARGETARCH}" >&2; exit 1 ;; \
    esac \
    && apt-get update && apt-get install -y --no-install-recommends minisign && rm -rf /var/lib/apt/lists/* \
    && tmp="$(mktemp -d)" && cd "${tmp}" \
    && bin="buf-Linux-${BUF_ARCH}" \
    && curl -fsSLO "https://github.com/bufbuild/buf/releases/download/v${BUF_VERSION}/sha256.txt" \
    && curl -fsSLO "https://github.com/bufbuild/buf/releases/download/v${BUF_VERSION}/sha256.txt.minisig" \
    && curl -fsSLO "https://github.com/bufbuild/buf/releases/download/v${BUF_VERSION}/${bin}" \
    && minisign -Vm sha256.txt -P "${BUF_RELEASE_SIGNER}" \
    && grep " ${bin}\$" sha256.txt | sha256sum -c - \
    && install -m 0755 "${bin}" /usr/local/bin/buf \
    && cd / && rm -rf "${tmp}" \
    && apt-get purge -y --auto-remove minisign \
    && test "$(buf --version)" = "${BUF_VERSION}"

# Codegen plugins installed LOCALLY so `buf generate` never executes remote
# plugins on the BSR — remote plugin execution is rate-limited and made the
# build non-deterministic (a throttled `buf generate` failed the whole
# pipeline). Versions pinned to match the output these plugins were producing
# remotely so generated code stays byte-identical. buf.gen.yaml references
# these as `local:` plugins.
RUN go install google.golang.org/protobuf/cmd/protoc-gen-go@v${PROTOC_GEN_GO_VERSION} && \
    go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v${PROTOC_GEN_GO_GRPC_VERSION}

# Install gomutants for mutation testing
RUN go install github.com/gomutants/gomutants@v${GOMUTANTS_VERSION}

# Install versionator for version management
RUN go install github.com/benjaminabbitt/versionator@v${VERSIONATOR_VERSION}

# Install gitleaks, the secret scanner lefthook's pre-commit hook runs
RUN go install github.com/zricethezav/gitleaks/v8@v${GITLEAKS_VERSION}

# Install shfmt, the shell formatter `just fmt` and lefthook's pre-commit hook
# run over the shell scripts
RUN go install mvdan.cc/sh/v3/cmd/shfmt@v${SHFMT_VERSION}

# Install lefthook, the runner of the commit hooks (lefthook.yml). This
# project's agent cells are built on this image, and the hook git generates
# falls back to an `echo` that exits 0 when lefthook is absent — so without it
# every commit made in a cell skips the hooks silently.
RUN go install github.com/evilmartians/lefthook@v${LEFTHOOK_VERSION}

# govulncheck, the Go vulnerability scanner `just govulncheck` runs (and CI's
# vulncheck job). It reports a known vulnerability only when the module's code
# can reach the affected symbol, not merely when a vulnerable version is in
# go.sum, so a finding is one the binaries can actually hit.
RUN go install golang.org/x/vuln/cmd/govulncheck@v${GOVULNCHECK_VERSION}

# GoReleaser: `just release-check` / `just release-snapshot` run here, never on
# the host. The host has no upx and no cross toolchain, so a host snapshot
# silently emits "-upx" artifacts that were never compressed. CI releases from
# goreleaser-cross (see .github/workflows/release-completer.yml), which bundles
# its own goreleaser — this pin only governs local validation.
RUN go install github.com/goreleaser/goreleaser/v2@v${GORELEASER_VERSION}

# Go caches live under GOPATH, NEVER under /workspace. /workspace is the
# bind-mounted source tree: caches written there land inside the Go module
# directory itself, where any tool that copies the module drags in gigabytes of
# read-only module cache and fails. Persistence across rebuilds comes from the named volumes mounted on
# these paths (see .devcontainer/devcontainer.json), not from the source mount.
# Mode 0777 keeps them writable whichever uid the container is run as.
#
# The caches the tool installs above filled are emptied first: nothing reads
# them (the devcontainer mounts volumes over both paths, `just _run` points
# GOMODCACHE/GOCACHE at host mounts), and a recursive chmod over gigabytes of
# module cache copies every file into a new overlay layer — minutes of build
# and a doubled image.
ENV GOMODCACHE="/go/pkg/mod"
ENV GOCACHE="/go/cache/build"
ENV GOTOOLCHAIN="local"
RUN go clean -cache -modcache \
    && mkdir -p "${GOMODCACHE}" "${GOCACHE}" && chmod -R 0777 /go

# serena — the code-intelligence MCP server (find_symbol,
# find_referencing_symbols, the symbol-edit tools). It is here as well as in
# container/base/Containerfile because THIS repo never uses that base: an
# unset isolation_base adopts the detected devcontainer (resolveBase), so an
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
# installer version is pinned. It is pinned INLINE, by tag and digest (see the
# top of this file), not via a build ARG like the tools above: BuildKit rejects
# variable expansion in `COPY --from`, so an ARG here would be a pin that does
# not bind.
COPY --from=ghcr.io/astral-sh/uv:0.9.22@sha256:2320e6c239737dc73cccce393a8bb89eba2383d17018ee91a59773df802c20e6 /uv /usr/local/bin/uv

# The tool venv and its shims go under /opt + /usr/local/bin, not a user home:
# this image is run under several uids and anything under /root would be
# unreachable by the process that actually runs the engine.
# The gate is `serena --help`, which imports the package and exits 0 — a shim
# that merely EXISTS proves nothing.
#
# serena-agent is pinned to SERENA_AGENT_VERSION, and `uv tool list` must
# report exactly that version. It is pinned by version only, not by hash:
# `uv tool install` (uv 0.9.22) has no hash-checking mode — it silently
# ignores hashes in --constraints and --with-requirements files — so its
# bytes, and its dependencies' versions, are whatever PyPI serves for this
# release. container/base/Containerfile still installs it unpinned.
ENV UV_TOOL_DIR=/opt/uv/tools \
    UV_TOOL_BIN_DIR=/usr/local/bin
RUN UV_NO_CACHE=1 uv tool install --python python3 "serena-agent==${SERENA_AGENT_VERSION}" \
    && uv tool list | grep -qx "serena-agent v${SERENA_AGENT_VERSION}" \
    && serena --help >/dev/null

# Node.js. The opencode bridge plugin ctxloom generates is JavaScript, and its
# wire tests run it under node: they skip on a host without node but FAIL under
# CI, which runs them in this image.
#
# The download is verified, like the git PPA's key above, against keys named
# by FULL fingerprint: the Node.js releasers' primary keys, as nodejs/node's
# README lists them. The release's SHASUMS256.txt must carry a valid signature
# whose signing key is one of THESE fingerprints (gpgv's VALIDSIG, not merely
# "some key in the ring"), and the tarball must match its line there. Bumping
# NODE_VERSION needs no hash edit; a release signed by a releaser missing from
# NODE_RELEASERS fails the build loudly, and the fix is to add the fingerprint from
# that README. `gpg` (installed with the build dependencies at the top) is used
# only to dearmor the keys: the base image's gpgv cannot read an armored
# keyring. NODE_RELEASERS is an ARG with a value, unlike the version ARGs
# above: it is not a pin tool-versions.env carries, and as an ARG it stays out
# of the running container's environment.
ARG NODE_RELEASERS="5BE8A3F6C8A5C01D106C0AD820B1A390B168D356 DD792F5973C6DE52C432CBDAC77ABFA00DDBF2B7 CC68F5A3106FF448322E48ED27F5E38D5B0A215F 8FCCA13FEF1D0C2E91008E09770F7A9A5AE15600 890C08DB8579162FEE0DF9DB8BEAB4DFCF555EF4 C82FA3AE1CBEDC6BE46B9360C43CEC45C17AB93C 108F52B48DB57BB0CC439B2997B01419BD92F80A 655F3B5C1FB3FA8D1A0CA6BDE4A7D232B936D2FD A363A499291CBBC940DD62E41F10027AF002F8B0"
RUN case "${TARGETARCH}" in \
        amd64) NODE_ARCH="x64" ;; \
        arm64) NODE_ARCH="arm64" ;; \
        *) echo "Unsupported architecture: ${TARGETARCH}" >&2; exit 1 ;; \
    esac \
    && tmp="$(mktemp -d)" && cd "${tmp}" \
    && for fpr in ${NODE_RELEASERS}; do \
        curl -fsSL "https://keyserver.ubuntu.com/pks/lookup?op=get&options=mr&search=0x${fpr}" -o key.asc \
        && gpg --batch --dearmor < key.asc >> keys.gpg || exit 1; \
    done \
    && tarball="node-v${NODE_VERSION}-linux-${NODE_ARCH}.tar.xz" \
    && curl -fsSLO "https://nodejs.org/dist/v${NODE_VERSION}/SHASUMS256.txt" \
    && curl -fsSLO "https://nodejs.org/dist/v${NODE_VERSION}/SHASUMS256.txt.sig" \
    && curl -fsSLO "https://nodejs.org/dist/v${NODE_VERSION}/${tarball}" \
    && signer="$(gpgv --status-fd 1 --keyring ./keys.gpg SHASUMS256.txt.sig SHASUMS256.txt 2>/dev/null | awk '$2 == "VALIDSIG" {print $NF}')" \
    && case " ${NODE_RELEASERS} " in \
        *" ${signer:-none} "*) ;; \
        *) echo "node ${NODE_VERSION}: SHASUMS256.txt has no valid signature from a listed releaser (signer: ${signer:-none})" >&2; exit 1 ;; \
    esac \
    && grep " ${tarball}\$" SHASUMS256.txt | sha256sum -c - \
    && tar -xJf "${tarball}" -C /opt \
    && ln -s "/opt/node-v${NODE_VERSION}-linux-${NODE_ARCH}" /opt/node \
    && cd / && rm -rf "${tmp}" \
    && test "$(/opt/node/bin/node --version)" = "v${NODE_VERSION}"
ENV PATH="/opt/node/bin:${PATH}"

# reprise from its build stage above, which says why the image needs it. The
# check runs it HERE, in the final image, so a binary that cannot load on this
# base fails the build rather than the first commit an agent makes.
COPY --from=reprise /usr/local/bin/reprise /usr/local/bin/reprise
RUN reprise --version

# Set working directory
WORKDIR /workspace

# Mark as devcontainer (for detection in justfile)
ENV DEVCONTAINER=1

# Default command
CMD ["bash"]
