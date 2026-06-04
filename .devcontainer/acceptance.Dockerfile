# Acceptance test image: the devcontainer toolchain (Go, git, just) plus Node and
# the Claude Code CLI, run as a NON-ROOT `ctxloom` user. The @live acceptance
# scenarios launch `claude` against a real model, so the agent must be present.
#
# The ctxloom binary is deliberately NOT baked in — it is built at runtime from
# the mounted workspace (see `just test-acceptance-live-container`), so the image
# stays generic and always tests the working tree, not a stale copy.
ARG BASE_IMAGE=ctxloom-devcontainer:latest
FROM ${BASE_IMAGE}

# Node + the Claude Code CLI. Installed as root at build time.
RUN curl -fsSL https://deb.nodesource.com/setup_20.x | bash - \
    && apt-get install -y --no-install-recommends nodejs \
    && npm install -g @anthropic-ai/claude-code \
    && npm cache clean --force \
    && rm -rf /var/lib/apt/lists/*

# The suite runs as this unprivileged user. Its home is writable for Go/npm
# caches and the runtime-built binary; credentials are mounted read-only at run
# time. Under rootless docker this user maps to a subuid, which is why the run
# target stages a world-readable copy of ~/.claude rather than mounting it raw.
RUN useradd --create-home --shell /bin/bash ctxloom
USER ctxloom
WORKDIR /workspace
