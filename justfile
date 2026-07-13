# Default recipe
default: build

# pre1 is a single consolidated module: always run go in module mode so the
# host go.work (which still points at the pre-consolidation `main` worktree and
# the now-absorbed sibling modules) can't shadow this tree. Mirrors
# justfile.container; the per-recipe GOWORK=off below become redundant but stay
# explicit for the docker `-e` passthroughs.
export GOWORK := "off"

# Get version from versionator (with fallback for CI without versionator).
# Standardized stamp format across the ctxloom family:
#   v<major.minor.patch>-<short-sha>-<YYYYMMDDTHHMMSS commit datetime, utc>
# versionator emits the compact datetime (no separator); sed inserts the 'T'.
version := `if v=$(versionator output version -t "{{Prefix}}{{MajorMinorPatch}}-{{ShortHash}}-{{BuildDateTimeCompact}}" --prefix 2>/dev/null); then v=$(echo "$v" | sed -E 's/([0-9]{8})([0-9]{6})$/\1T\2/'); d=$(versionator output version -t "{{Dirty}}" 2>/dev/null); if [ -n "$d" ]; then echo "$v-dirty"; else echo "$v"; fi; else echo dev; fi`

# ===== Version management (versionator) =====

# Show current version
show-version:
    @versionator output version

# Set the release version (the supported way to bump for a release). Releases
# are merge-triggered: bump here, commit VERSION, merge — CI tags it immutably
# (v-prefixed). Example: just set-version 0.7.0
set-version version:
    versionator set {{version}}

# Bump patch version (0.0.X)
bump-patch:
    versionator bump patch inc

# Bump minor version (0.X.0)
bump-minor:
    versionator bump minor inc

# Bump major version (X.0.0)
bump-major:
    versionator bump major inc

# Auto-bump based on conventional commits (feat: -> minor, fix: -> patch)
bump:
    versionator bump

# Create release (git tag + push)
release:
    versionator release push

# Validate .goreleaser.yml (delegates to devcontainer)
release-check: dev-image
    just _run release-check

# Snapshot-build release artifacts for this platform into dist/ (delegates to
# devcontainer). goreleaser NEVER runs on the host: the host lacks upx, so a
# host snapshot emits "-upx" artifacts that are byte-identical to the
# uncompressed ones — a silent lie about what a release contains.
release-snapshot: dev-image
    just _run release-snapshot

# Build the main binary with all features (delegates to devcontainer)
build: dev-image
    just _run build

# Compress binary with UPX (delegates to devcontainer)
compress: dev-image
    just _run compress

# Build + compress all three binaries in the devcontainer (ctxloom + ltk +
# taskloom, each UPX-compressed). Delegates to the container `build-compressed`.
build-compressed: dev-image
    just _run build-compressed

# Build all three binaries UNCOMPRESSED in the devcontainer (fast-starting
# local install; UPX is release-only — see install/.goreleaser.yml).
build-all-bins: dev-image
    just _run build-all-bins

# Build the ltk companion binary in the devcontainer into bin/ltk, via the ltk
# module. ltk ships from the unified ctxloom release; main.Version matches
# `ltk version`.
build-ltk: dev-image
    just _run ltk::build

# Build the taskloom companion binary in the devcontainer into bin/taskloom, via
# the taskloom module. taskloom stamps the lowercase main.version
# (`taskloom version`).
build-taskloom: dev-image
    just _run taskloom::build

# Regenerate the committed publish-signature siblings for the in-repo
# companion loadouts (cmd/ltk/loadout.yaml, cmd/taskloom/loadout.yaml) using
# the ctxloom release key, so `<bin> loadout --format json` verifies as a
# trusted publisher (internal/config/embedded_signers.allowed_signers)
# instead of landing in ctxloom's review-pending path. Runs on the HOST (not
# delegated to the devcontainer): it needs the private key from ~/.ssh, which
# the devcontainer never mounts. Unlike `just build` — which only ever reads
# the committed .sig bytes via go:embed — this needs the PRIVATE key; run it
# once, commit the resulting .sig files, and `just build` never touches the
# key again. A signature that no longer matches its loadout.yaml (edited
# without a re-sign) is caught by `just test`
# (cmd/ltk/loadout_test.go, cmd/taskloom/loadout_test.go verify the committed
# .sig against the committed .yaml through the real embedded trust root), not
# by this recipe.
#
# Tries the on-disk private key directly first; if that key is passphrase-
# protected and ssh-agent already holds the matching identity (`ssh-add
# /path/to/key` in your own terminal, entered interactively — this recipe
# never touches the passphrase), falls back to `-U` + the public key, which
# routes the actual signing operation through the agent.
sign-loadouts key="":
    #!/usr/bin/env bash
    set -euo pipefail
    key="{{key}}"
    if [ -z "$key" ]; then
        key="$HOME/.ssh/ctxloom_ssh_key"
    fi
    if [ ! -f "$key" ]; then
        echo "sign-loadouts: signing key not found: $key" >&2
        echo "  pass one explicitly: just sign-loadouts /path/to/key" >&2
        exit 1
    fi
    for f in cmd/ltk/loadout.yaml cmd/taskloom/loadout.yaml; do
        rm -f "$f.sig"
        if ! ssh-keygen -Y sign -f "$key" -n publish.v1.ctxloom.dev "$f" 2>/tmp/sign-loadouts-err; then
            if [ -f "$key.pub" ]; then
                echo "sign-loadouts: $key needs a passphrase this recipe doesn't have; trying ssh-agent via $key.pub" >&2
                ssh-keygen -Y sign -U -f "$key.pub" -n publish.v1.ctxloom.dev "$f"
            else
                cat /tmp/sign-loadouts-err >&2
                exit 1
            fi
        fi
    done
    echo "signed (namespace publish.v1.ctxloom.dev):"
    echo "  cmd/ltk/loadout.yaml.sig"
    echo "  cmd/taskloom/loadout.yaml.sig"
    echo "commit both .sig files alongside the .yaml they cover."

# Validate fragment YAML files (delegates to devcontainer)
validate: dev-image
    just _run validate

# Generate protobuf code (delegates to devcontainer)
proto: dev-image
    just _run proto

# List available plugins
plugin-list:
    ./ctxloom plugin list

# Build with verbose output (local, for debugging)
build-verbose:
    go build -v -ldflags "-X github.com/ctxloom/ctxloom/internal/cli.Version={{version}}" -o ctxloom ./cmd/ctxloom

# Regenerate the published JSON Schemas for ctxloom's JSON output into the
# gitignored resources/schema/gen/ by reflecting their producing Go structs.
# A generated build artifact (like protobuf), not checked in. Build-tagged
# `schemagen` so the reflection dependency never enters the production binary.
gen-schemas:
    go run -tags schemagen ./cmd/gen-schemas

# Ensure the covdata tool is present for multi-package coverage merges.
# Go 1.25 dropped covdata (and other secondary tools) from the prebuilt
# distribution — they're built on demand from src/cmd. But `go test
# -coverprofile ./...` merges coverage for test-less packages by invoking
# covdata out of GOTOOLDIR, and an auto-downloaded toolchain's GOTOOLDIR is
# read-only with no covdata, so the merge fails with `no such tool "covdata"`.
# Build the version-matched covdata into GOTOOLDIR once (idempotent).
_ensure-covdata:
    #!/usr/bin/env bash
    set -euo pipefail
    tooldir="$(go env GOTOOLDIR)"
    if [ -x "$tooldir/covdata" ]; then exit 0; fi
    chmod u+w "$tooldir" 2>/dev/null || true
    go build -o "$tooldir/covdata" cmd/covdata
    echo "ctxloom: built version-matched covdata into $tooldir/"

# Run all tests (builds ctxloom first for acceptance tests).
# Coverage is filtered through .coverignore so generated files
# (protobuf, gRPC) don't drag the reported number down.
# vet-integration is the tag-gated-test compile rot gate (see its comment).
test: build _ensure-covdata vet-integration
    #!/usr/bin/env bash
    set -e
    go test -race -coverprofile=coverage.raw.out ./...
    just _filter_coverage coverage.raw.out coverage.out
    rm -f coverage.raw.out

# Run tests with verbose output
test-verbose:
    go test -v ./...

# Run the offline suite under a hostile environment to prove test isolation: a
# junk HOME (no real ~/.ctxloom) plus poison session env. A green run means no
# test depends on real-home content or ambient session env, and none pollutes the
# user's ~/.ctxloom. The Go toolchain + caches are pinned to their real locations
# first (the version-manager shim and module cache live under the real home), and
# the real go binary is invoked directly so junking HOME doesn't break the build.
test-dirty:
    #!/usr/bin/env bash
    set -e
    GO="$(go env GOROOT)/bin/go"
    export GOCACHE="$(go env GOCACHE)" GOMODCACHE="$(go env GOMODCACHE)" GOPATH="$(go env GOPATH)"
    export HOME="$(mktemp -d)"
    export CTXLOOM_PROJECT_ID=poison-project CTXLOOM_SESSION_HARP=poison-harp
    export CTXLOOM_RESUMED_FROM=poison CTXLOOM_RESUMED_PARTS=poison CTXLOOM_DEBUG_HTTP=1
    export GITHUB_TOKEN=poison-token GH_TOKEN=poison-token CODEX_HOME=/poison/codex
    export CTXLOOM_ROOT=/poison/root
    "$GO" test ./internal/... ./cmd/...

# Filter coverage output using patterns from .coverignore
# Usage: _filter_coverage <input> <output>
_filter_coverage INPUT OUTPUT:
    #!/usr/bin/env bash
    set -e
    if [ -f .coverignore ]; then
        # Build grep pattern from .coverignore (skip comments and empty lines)
        patterns=$(grep -v '^#' .coverignore | grep -v '^$' | paste -sd '|' -)
        if [ -n "$patterns" ]; then
            grep -Ev "$patterns" "{{INPUT}}" > "{{OUTPUT}}" || cp "{{INPUT}}" "{{OUTPUT}}"
            exit 0
        fi
    fi
    cp "{{INPUT}}" "{{OUTPUT}}"

# Run tests with coverage (excludes patterns in .coverignore)
cover:
    #!/usr/bin/env bash
    set -e
    echo "Running tests with coverage..."
    go test -coverprofile=coverage.raw.out ./... > /dev/null 2>&1
    just _filter_coverage coverage.raw.out coverage.out
    rm -f coverage.raw.out
    echo "Coverage (excluding patterns from .coverignore):"
    go tool cover -func=coverage.out | tail -1

# Show per-function coverage (excludes patterns in .coverignore)
cover-func:
    #!/usr/bin/env bash
    set -e
    go test -coverprofile=coverage.raw.out ./... > /dev/null 2>&1
    just _filter_coverage coverage.raw.out coverage.out
    rm -f coverage.raw.out
    echo "Coverage by function (excluding patterns from .coverignore):"
    go tool cover -func=coverage.out

# Generate HTML coverage report (excludes patterns in .coverignore)
cover-html:
    #!/usr/bin/env bash
    set -e
    go test -coverprofile=coverage.raw.out ./... > /dev/null 2>&1
    just _filter_coverage coverage.raw.out coverage.out
    rm -f coverage.raw.out
    go tool cover -html=coverage.out -o coverage.html
    echo "Coverage report generated: coverage.html"

# Run tests with coverage (legacy alias)
test-coverage: cover

# Run the cross-agent equity conformance suite (claude/gemini/codex through the
# shared agent.SettingsWriter contract). Tag-gated so it's excluded from the
# default `go test ./...`; run it explicitly here.
test-conformance:
    go test -race -tags conformance ./internal/lm/conformance/...

# Compile-check the `-tags integration` build fence — a cheap rot gate for
# tag-gated tests (tests/integration/*_test.go). No container needed: vet
# doesn't touch CGO/treesitter, just the generated proto stubs (`just build`
# once in a fresh worktree first). Nothing else on the default path ever
# type-checks this tag: golangci-lint runs the default build only, and
# `test`/coverage exclude build-tagged files by construction. A test file
# that only compiles under the tag can therefore bit-rot silently — exactly
# what happened to acp_agent_test.go (stale agent.ChatRequest.AutoApprove
# field) and acp_live_test.go (claude.NewClaudeCode's old one-arg signature),
# both invisible until something finally ran this. Wired into both `test`
# below and `lint` (justfile.container), so it gates the default local AND CI
# paths. vet, not test/run — stays cheap.
vet-integration:
    go vet -tags integration ./tests/...

# Run integration tests (requires ctxloom binary)
test-integration: build
    go test -v -tags integration ./tests/integration/...

# Run integration tests matching a -run PATTERN (requires ctxloom binary)
test-integration-run PATTERN: build
    go test -v -tags integration -run '{{PATTERN}}' ./tests/integration/...

# Run the full-stack acceptance suite (godog): asserts each change across files,
# CLI, and mock-agent MCP traffic. Hermetic by default (@live scenarios skipped).
# Build runs in the devcontainer; the suite runs on the host like integration.
test-acceptance: build
    go test -tags "acceptance integration" -count=1 ./tests/acceptance/...

# Run the docker-gated container transport integration tests
# (internal/lm/isolation/*_integration_test.go): builds a minimal image,
# spawns the mock plugin INSIDE a real container, and proves the gRPC
# transport + force-removal-on-Kill boundary end to end, including a real
# git worktree mounted into a container. Requires a local container runtime;
# each test self-skips (not fails) when docker is unreachable or non-rootless,
# so this is safe to run anywhere — it just proves nothing when it skips.
# Previously gated behind a build tag that appeared in neither the justfile
# nor any CI workflow, so it had never actually run.
test-docker-integration:
    go test -v -tags docker_integration ./internal/lm/isolation/...

# Build the acceptance image (devcontainer toolchain + Node + the Claude Code
# agent), run as a non-root `ctxloom` user. The ctxloom binary is NOT baked in;
# it is built at runtime from the mounted workspace by test-acceptance-live-container.
container-build-acceptance: dev-image
    {{container_cmd}} build -t {{registry}}/ctxloom-acceptance:latest \
        --build-arg BASE_IMAGE={{devcontainer_image}}:latest \
        -f .devcontainer/acceptance.Dockerfile .

# Run the full acceptance suite (incl. @live real-agent scenarios) inside the
# acceptance container as the non-root `ctxloom` user, on THIS machine. Builds
# the image first. Your ~/.claude and ~/.gemini are copied to a world-readable
# staging dir and mounted read-only — under rootless docker the non-root user
# maps to a subuid that cannot read your 0600 credentials directly. Set
# {ANTHROPIC,GEMINI,GOOGLE}_API_KEY to use the unattended API-key path instead.
# ctxloom is built at runtime from the read-only workspace mount; all writes go
# to the container HOME / tmp. Each agent's @live rows self-skip without creds.
test-acceptance-live-container: container-build-acceptance
    #!/usr/bin/env bash
    set -euo pipefail
    staging="$(mktemp -d)"
    trap 'chmod -R u+w "$staging" 2>/dev/null; rm -rf "$staging"' EXIT

    # Credentials: copy only the files the live steps read (copyClaudeCredentials /
    # copyGeminiCredentials), not the whole credential trees — they hold large logs,
    # caches, and dangling symlinks.
    mkdir -p "$staging/.claude"
    for f in .credentials.json settings.json config.json; do
        if [ -f "$HOME/.claude/$f" ]; then cp "$HOME/.claude/$f" "$staging/.claude/$f"; fi
    done
    if [ -f "$HOME/.claude.json" ]; then cp "$HOME/.claude.json" "$staging/.claude.json"; fi

    mkdir -p "$staging/.gemini"
    for f in oauth_creds.json google_accounts.json settings.json installation_id user_id; do
        if [ -f "$HOME/.gemini/$f" ]; then cp "$HOME/.gemini/$f" "$staging/.gemini/$f"; fi
    done

    # Source: stage a copy of the working tree (minus .git) so the non-root user
    # can read it. Rootless docker maps that user to a subuid, and the working
    # tree has mixed perms (some 0600 files) it could not otherwise read; staging
    # + a+rX avoids mutating your tree.
    # Module mode (GOWORK=off below): github.com/ctxloom/* resolve from the
    # go.mod pins via the mounted host module cache (or the proxy), never from
    # sibling checkouts — the container build sees exactly what a release build
    # sees. Pushing sibling changes and bumping the pin is the way to pick up
    # cross-module work here. Warm the host cache first so the read-only mount
    # has every pinned module and the build runs offline.
    GOWORK=off go mod download
    mkdir -p "$staging/src"
    tar -cf - --exclude=./.git --exclude='*.test' --exclude=./website/node_modules . | tar -xf - -C "$staging/src"
    chmod -R a+rX "$staging"

    mounts=(-v "$staging/.claude:/home/ctxloom/.claude:ro" -v "$staging/.gemini:/home/ctxloom/.gemini:ro" -v "$staging/src:/workspace:ro")
    if [ -f "$staging/.claude.json" ]; then mounts+=(-v "$staging/.claude.json:/home/ctxloom/.claude.json:ro"); fi
    # Reuse the host module cache read-only so the runtime build doesn't re-download.
    if [ -d "$HOME/go/pkg/mod" ]; then mounts+=(-v "$HOME/go/pkg/mod:/home/ctxloom/go/pkg/mod:ro"); fi

    # Forward API keys when present so the unattended API-key path runs without
    # copied subscription creds. Absent keys fall back to the mounted cred dirs.
    keys=()
    for k in ANTHROPIC_API_KEY GEMINI_API_KEY GOOGLE_API_KEY; do
        if [ -n "${!k:-}" ]; then keys+=(-e "$k"); fi
    done

    {{container_cmd}} run --rm \
        "${mounts[@]}" \
        ${keys[@]+"${keys[@]}"} \
        -e HOME=/home/ctxloom \
        -e CTXLOOM_ACCEPTANCE_LIVE=1 \
        -e ACCEPTANCE_TAGS="~@network" \
        -e GOCACHE=/home/ctxloom/.cache/go-build \
        -e GOMODCACHE=/home/ctxloom/go/pkg/mod \
        -e GOPATH=/home/ctxloom/go \
        -e GOFLAGS=-mod=readonly \
        -e GOWORK=off \
        -w /workspace \
        {{registry}}/ctxloom-acceptance:latest \
        bash -c 'set -e; \
            go build -o /home/ctxloom/ctxloom . && \
            CTXLOOM_BINARY=/home/ctxloom/ctxloom go test -tags "acceptance integration" -count=1 ./tests/acceptance/...'

# Run a single package's tests under -race (fast local iteration)
test-pkg PKG *ARGS:
    go test -race {{ARGS}} {{PKG}}

# ===== Mutation testing =====

# gremlins copies the whole Go module into TMPDIR once per worker. On a tmpfs
# /tmp that exhausts RAM and wedges the machine (it has emptied a 16G tmpfs
# here), so both recipes pin TMPDIR to disk and sweep the copies afterwards.
mutation_tmp := env_var_or_default("CTXLOOM_MUTATION_TMP", "/var/tmp/ctxloom-mutation")

# Run mutation tests with gremlins (requires gremlins installed)
test-mutation *ARGS:
    #!/usr/bin/env bash
    set -euo pipefail
    mkdir -p "{{mutation_tmp}}"
    trap 'rm -rf "{{mutation_tmp}}"/gremlins-*' EXIT
    TMPDIR="{{mutation_tmp}}" gremlins unleash {{ARGS}}

# Run mutation tests on specific package
# gremlins appends /... to the target itself; passing it here yields
# ./pkg/.../... which matches nothing and fails with "no packages to test".
test-mutation-pkg PKG *ARGS:
    #!/usr/bin/env bash
    set -euo pipefail
    mkdir -p "{{mutation_tmp}}"
    trap 'rm -rf "{{mutation_tmp}}"/gremlins-*' EXIT
    TMPDIR="{{mutation_tmp}}" gremlins unleash ./{{PKG}} {{ARGS}}

# Install gremlins
test-mutation-install:
    go install github.com/go-gremlins/gremlins/cmd/gremlins@v0.6.0

# Run mutation tests in container
# The container path carries the same TMPDIR hazard as the host recipes: gremlins
# copies the module per worker, so its scratch space must be a bind-mounted disk
# dir, never the container's default (which is backed by the host's /tmp). The
# image tag is pinned to the same gremlins version test-mutation-install builds,
# so a container run and a host run mutate identically.
test-mutation-container:
    #!/usr/bin/env bash
    # See _run for why --user is skipped under rootless docker.
    user_flag=(--user "$(id -u):$(id -g)")
    if docker info 2>/dev/null | grep -q "rootless"; then
        user_flag=()
    fi
    mkdir -p "{{mutation_tmp}}"
    trap 'rm -rf "{{mutation_tmp}}"/gremlins-*' EXIT
    docker run --rm "${user_flag[@]}" \
        -v "$(pwd):/app" \
        -v "{{mutation_tmp}}:/mutation-tmp" \
        -e TMPDIR=/mutation-tmp \
        -w /app gogremlins/gremlins:v0.6.0 gremlins unleash

# Clean build artifacts
clean:
    rm -f ctxloom
    rm -rf bin/ man/
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

# Lint code (delegates to devcontainer for the pinned golangci-lint)
lint: dev-image
    just _run lint

# ===== Code complexity (lizard, in devcontainer) =====
# lizard is a cross-platform, multi-language per-function complexity analyzer,
# installed in the devcontainer image. These targets delegate into it, so no
# host install is needed (implementations live in justfile.container).

# Per-function cyclomatic complexity as a human-readable table + warnings.
# Defaults to the repo root; pass paths/flags to scope, e.g.
#   just complexity internal/remote
#   just complexity -C 15 .           (warn on functions over CCN 15)
complexity *ARGS: dev-image
    just _run complexity {{ARGS}}

# Same per-function analysis as CSV (header prepended) for LLM/tooling ingest.
# Columns: nloc,ccn,tokens,params,length,location,file,function,long_name,start,end
complexity-csv *ARGS: dev-image
    just _run complexity-csv {{ARGS}}

# Enforcing gate: fail if any function exceeds CCN 10 (used by the CI lint job).
complexity-check *ARGS: dev-image
    just _run complexity-check {{ARGS}}

# Run the CLI locally without installing — builds ./ctxloom (host, no
# treesitter/CGO) and execs it attached to this terminal, so interactive
# sessions get a real tty (cleaner than `go run` for pty/raw-mode smoke tests).
# Never touches your PATH/installed ctxloom. E.g. `just run run`, `just run memory list`.
run *ARGS:
    #!/usr/bin/env bash
    set -euo pipefail
    go build -ldflags "-X github.com/ctxloom/ctxloom/internal/cli.Version={{version}}" -o ctxloom ./cmd/ctxloom
    exec ./ctxloom {{ARGS}}

# Build, compress, and install all three binaries to ~/go/bin (standard Go
# location): ctxloom plus its companions ltk and taskloom, which now ship from
# this same repo. Atomic rename instead of pkill+cp: replacing the directory
# entry leaves the busy inode mapped for any running binary (avoids ETXTBSY and
# never dumps a live ctxloom-managed session); new launches pick up the new one.
install: build-all-bins
    mkdir -p ~/go/bin
    cp ctxloom ~/go/bin/ctxloom.new
    mv -f ~/go/bin/ctxloom.new ~/go/bin/ctxloom
    cp bin/ltk ~/go/bin/ltk.new
    mv -f ~/go/bin/ltk.new ~/go/bin/ltk
    cp bin/taskloom ~/go/bin/taskloom.new
    mv -f ~/go/bin/taskloom.new ~/go/bin/taskloom

# Uninstall all three binaries from ~/go/bin
uninstall:
    rm -f ~/go/bin/ctxloom ~/go/bin/ltk ~/go/bin/taskloom

# Regenerate the proto-canonical MCP tool schemas (checked-in goldens under
# internal/agentcoord/mcpschema/schemas/) from a buf-built FileDescriptorSet
# WITH source info (buf includes SourceCodeInfo by default; the protoc
# fallback is --descriptor_set_out --include_source_info). CI fails on drift
# (gen-mcp-schemas-check in justfile.container).
gen-mcp-schemas:
    #!/usr/bin/env bash
    set -euo pipefail
    tmp=$(mktemp)
    trap 'rm -f "$tmp"' EXIT
    buf build -o "$tmp"
    go run ./internal/agentcoord/mcpschema/gen -descriptor "$tmp" -out internal/agentcoord/mcpschema/schemas

# Generate the reference docs for all three binaries from their sources of
# truth: the CLI reference (man pages + website markdown) from each cobra
# command tree, the MCP reference from the live tool/resource registrations, and
# ctxloom's config reference from the tracked JSON Schema. One generator
# (internal/docsgen) serves all three; taskloom and ltk keep their trees in
# `package main`, so it mounts on them as a hidden `gendocs` subcommand compiled
# only under `-tags docsgen`. CI fails on drift (gen-docs-check in
# justfile.container).
gen-docs:
    go run ./scripts/gendocs \
        --man man/man1 \
        --markdown website/src/content/docs/reference/cli \
        --mcp website/src/content/docs/reference \
        --config website/src/content/docs/reference \
        --config-schema resources/schema/input/config-schema.json
    go run -tags docsgen ./cmd/taskloom gendocs \
        --man man/man1 \
        --markdown website/src/content/docs/taskloom/reference/cli \
        --mcp website/src/content/docs/taskloom/reference
    go run -tags docsgen ./cmd/ltk gendocs \
        --man man/man1 \
        --markdown website/src/content/docs/ltk/reference/cli

# Generate man pages only, for all three binaries (the --man half of gen-docs)
man:
    go run ./scripts/gendocs --man man/man1
    go run -tags docsgen ./cmd/taskloom gendocs --man man/man1
    go run -tags docsgen ./cmd/ltk gendocs --man man/man1

# Install man pages (Linux/macOS)
man-install: man
    @mkdir -p ~/.local/share/man/man1
    cp man/man1/*.1 ~/.local/share/man/man1/
    @echo "Man pages installed. Run 'man ctxloom' (or 'man taskloom' / 'man ltk') to view."

# Show help
help:
    ./ctxloom --help

# ===== Documentation targets =====

# Start docs dev server (http://localhost:4321)
docs:
    cd website && npm run dev

# Build docs for production
docs-build:
    cd website && npm run build

# Preview production docs build
docs-preview:
    cd website && npm run preview

# Initialize .ctxloom directory
init:
    ./ctxloom init

# Dry run with test fragments
dry-run PROMPT:
    ./ctxloom run -f test-fragment -f additional-context -n "{{PROMPT}}"

# Run with Gemini plugin
gemini *ARGS:
    ./ctxloom -P gemini {{ARGS}}

# Run with Claude plugin (default)
claude *ARGS:
    ./ctxloom -P claude-code {{ARGS}}

# Code review with reviewer profile
review *ARGS:
    ./ctxloom -p reviewer -r code-review {{ARGS}}

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

# ===== Container targets =====

# Container registry prefix for locally-built utility images (the acceptance
# image); the agent images are local-only tags.
registry := "localhost"

# Build the MINIMAL isolation image: the locally-built static linux ctxloom on a
# small base, tagged ctxloom-agent:latest (the default the container isolation
# policy looks for). Proves the plugin-in-container transport without an engine CLI
# or auth. The production agent image (real engine + auth) is a separate follow-up.
container-build-minimal:
    #!/usr/bin/env bash
    set -euo pipefail
    ctx=$(mktemp -d)
    trap 'rm -rf "$ctx"' EXIT
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOWORK=off go build \
        -ldflags "-X github.com/ctxloom/ctxloom/internal/cli.Version={{version}}" \
        -o "$ctx/ctxloom" ./cmd/ctxloom
    cp container/minimal/Containerfile "$ctx/Containerfile"
    {{container_cmd}} build -t ctxloom-agent:latest -f "$ctx/Containerfile" "$ctx"

# Build the PRODUCTION claude agent image: the locally-built static linux ctxloom
# PLUS a real claude CLI (npm @anthropic-ai/claude-code on node:22-slim), tagged
# ctxloom-agent:latest (the default image the container isolation policy looks for)
# AND ctxloom-agent-claude:latest. Unlike container-build-minimal (transport only),
# this runs a REAL engine in-container for a top-level isolated `ctxloom run`. Auth
# is NOT baked in — it crosses at run time (ANTHROPIC_* passthrough, or a read-only
# ~/.claude credential mount). Follows the self-contained container-build-minimal
# pattern (docker, static binary, no base-image dependency); the Go test gate never
# depends on this image (the build is slow/network-bound).
container-build-claude: container-build-base
    #!/usr/bin/env bash
    set -euo pipefail
    ctx=$(mktemp -d)
    trap 'rm -rf "$ctx"' EXIT
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOWORK=off go build \
        -ldflags "-X github.com/ctxloom/ctxloom/internal/cli.Version={{version}}" \
        -o "$ctx/ctxloom" ./cmd/ctxloom
    # Companions mirrored from the host (the agent stage's COPY companions/
    # requires the dir even when empty; a missing companion just warns).
    mkdir -p "$ctx/companions"
    for b in taskloom ltk reprise; do
        if p=$(command -v "$b"); then cp "$p" "$ctx/companions/$b"; \
        else echo "warning: companion $b not on PATH; image builds without it" >&2; fi
    done
    cp container/entrypoint.sh "$ctx/ctxloom-entrypoint"
    cp container/production/Containerfile-claude-code "$ctx/Containerfile"
    # Stamp the provenance digest computed by the very binary being baked, so a
    # later `ctxloom run` with that same binary sees the image as current.
    prov=$("$ctx/ctxloom" container provenance 2>/dev/null || echo "")
    {{container_cmd}} build -t ctxloom-agent:latest -t ctxloom-agent-claude:latest \
        --build-arg BASE_IMAGE=ctxloom-agent-base:latest \
        --build-arg CTXLOOM_VERSION={{version}} \
        --build-arg CTXLOOM_PROVENANCE="$prov" \
        -f "$ctx/Containerfile" "$ctx"

# Build the shared agent-image BASE stage (ctxloom-agent-base:latest): the distro
# plus the coding-agent tool layer (git, ripgrep, curl, certs, unzip, jq). The
# per-engine recipes layer their agent stage on top via --build-arg BASE_IMAGE.
# To bring your own base instead, use `ctxloom container build
# --base-containerfile <file>` (or config isolation_base_containerfile).
container-build-base:
    #!/usr/bin/env bash
    set -euo pipefail
    ctx=$(mktemp -d)
    trap 'rm -rf "$ctx"' EXIT
    cp container/base/Containerfile "$ctx/Containerfile"
    {{container_cmd}} build -t ctxloom-agent-base:latest -f "$ctx/Containerfile" "$ctx"

# Build the PRODUCTION kiro agent image: the locally-built static linux ctxloom
# PLUS a real kiro-cli (official installer on debian slim), tagged
# ctxloom-agent-kiro:latest (the image the kiro container profile looks for).
# Auth is NOT baked in — KIRO_API_KEY crosses at run time (headless mode). The
# isolation policy can also build this image ON THE FLY from the embedded
# Containerfile when the tag is absent; this recipe is the ahead-of-time path.
container-build-kiro: container-build-base
    #!/usr/bin/env bash
    set -euo pipefail
    ctx=$(mktemp -d)
    trap 'rm -rf "$ctx"' EXIT
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOWORK=off go build \
        -ldflags "-X github.com/ctxloom/ctxloom/internal/cli.Version={{version}}" \
        -o "$ctx/ctxloom" ./cmd/ctxloom
    # Companions mirrored from the host (the agent stage's COPY companions/
    # requires the dir even when empty; a missing companion just warns).
    mkdir -p "$ctx/companions"
    for b in taskloom ltk reprise; do
        if p=$(command -v "$b"); then cp "$p" "$ctx/companions/$b"; \
        else echo "warning: companion $b not on PATH; image builds without it" >&2; fi
    done
    cp container/entrypoint.sh "$ctx/ctxloom-entrypoint"
    cp container/production/Containerfile-kiro "$ctx/Containerfile"
    # Stamp the provenance digest computed by the very binary being baked, so a
    # later `ctxloom run` with that same binary sees the image as current.
    prov=$("$ctx/ctxloom" container provenance 2>/dev/null || echo "")
    {{container_cmd}} build -t ctxloom-agent-kiro:latest \
        --build-arg BASE_IMAGE=ctxloom-agent-base:latest \
        --build-arg CTXLOOM_VERSION={{version}} \
        --build-arg CTXLOOM_PROVENANCE="$prov" \
        -f "$ctx/Containerfile" "$ctx"

# List all ctxloom container images
container-list:
    @{{container_cmd}} images | grep -E "ctxloom-agent" | sort

# ===== Devcontainer overlay pattern =====
# Runs targets inside devcontainer with CGO dependencies (libtokenizers, ONNX runtime)
# Uses justfile.container which is mounted over justfile inside container

# Container runtime (docker or podman)
container_cmd := env_var_or_default("CONTAINER_CMD", "docker")

# Devcontainer image name
devcontainer_image := "ctxloom-devcontainer"

# Build devcontainer image
dev-image:
    {{container_cmd}} build -t {{devcontainer_image}}:latest -f .devcontainer/Dockerfile .

# Internal helper: run just target inside devcontainer
# Mounts justfile.container as /workspace/justfile (overlay pattern)
_run +ARGS:
    #!/usr/bin/env bash
    if [ -n "$DEVCONTAINER" ] || [ -n "$CI" ] || [ -n "$GITHUB_ACTIONS" ]; then
        # Already inside container (devcontainer or CI), use container justfile directly
        just -f justfile.container {{ARGS}}
    else
        # Run in container with justfile overlay and uid/gid mapping.
        #
        # Skip --user under rootless docker: the daemon already maps container
        # root (uid 0) to the invoking host user, so an explicit --user 1000:1000
        # lands on an unrelated subordinate uid that can neither write workspace
        # files nor read mode-0700 dirs. Rootful daemons need --user to avoid
        # leaving root-owned files in the host workspace.
        user_flag=(--user "$(id -u):$(id -g)")
        if {{container_cmd}} info 2>/dev/null | grep -q "rootless"; then
            user_flag=()
        fi
        # Module mode (GOWORK=off): github.com/ctxloom/* resolve from the go.mod
        # pins via the mounted host module cache (or the proxy), never from host
        # sibling checkouts — the container build sees exactly what a release
        # build sees. Push sibling changes and bump the pin to pick up
        # cross-module work here. Warm the host cache so the read-only mount
        # already holds every pinned module.
        GOWORK=off go mod download
        # Reuse the host module cache (read-only) and keep Go's writable caches
        # in the container tmpdir. Without this, HOME/GOCACHE resolve under the
        # cwd and the build spills a .cache/ into the source tree (and re-downloads
        # every run).
        cache_mount=()
        if [ -d "$HOME/go/pkg/mod" ]; then cache_mount=(-v "$HOME/go/pkg/mod:/tmp/gomodcache:ro"); fi
        {{container_cmd}} run --rm \
            "${user_flag[@]}" \
            "${cache_mount[@]}" \
            -e HOME=/tmp \
            -e GOMODCACHE=/tmp/gomodcache \
            -e GOCACHE=/tmp/.gocache \
            -e GOWORK=off \
            -v "$(pwd):/workspace" \
            -v "$(pwd)/justfile.container:/workspace/justfile:ro" \
            -w /workspace \
            {{devcontainer_image}}:latest \
            just {{ARGS}}
    fi

# Build with all CGO features (static, inside devcontainer)
dev-build: dev-image
    just _run build

# Build with ONNX support (static, inside devcontainer)
dev-build-onnx: dev-image
    just _run build-onnx

# Build with tree-sitter (static, inside devcontainer)
dev-build-treesitter: dev-image
    just _run build-treesitter

# Build with all features (static, inside devcontainer)
dev-build-full: dev-image
    just _run build-full

# Run treesitter (CGO) tests inside devcontainer
dev-test-treesitter: dev-image
    just _run test-treesitter

# Run any target inside devcontainer
dev +ARGS: dev-image
    just _run {{ARGS}}

# Shell into devcontainer for debugging
dev-shell: dev-image
    {{container_cmd}} run --rm -it \
        --user "$(id -u):$(id -g)" \
        -v "$(pwd):/workspace" \
        -v "$(pwd)/justfile.container:/workspace/justfile:ro" \
        -w /workspace \
        {{devcontainer_image}}:latest \
        bash

# TEMP (trust/fail-loudly validation): acceptance without the buf-invoking build
# chain — proto artifacts are already generated on disk. Remove after use.
test-acceptance-nobuild:
    go test -tags "acceptance integration" -count=1 ./tests/acceptance/...
