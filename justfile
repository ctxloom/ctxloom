# Recipes below that take PKG/PATTERN/*ARGS and forward them to `go test`
# rely on this: it hands recipe parameters to shebang scripts as real shell
# positional args ($1, "$@") instead of splicing them into the recipe body as
# text. Without it, a value like `-run 'TestA|TestB'` gets re-parsed by the
# script's shell and the `|` acts as an actual pipe (`sh: 1: TestB: not
# found`) instead of reaching go test as one -run argument.
set positional-arguments := true

# Gates that MUST be identical here and in justfile.container:
# test-docker-integration (and its package list), test-arch, plus the
# generated-protobuf precondition. Imported, not duplicated — the
# docker-integration recipe used to exist in both files under the same name with different package lists,
# which is how the whole internal/agentcoord/coord docker suite went unrun in
# CI. See build/gates.justfile.
import "build/gates.justfile"

# Every CI step, as a recipe. .github/workflows/ invokes these by name instead
# of carrying its own `run:` shell, so a CI step is something you can run
# locally and there is one definition rather than a workflow copy that drifts.
# Imported by justfile.container too. See build/ci.justfile.
import "build/ci.justfile"

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

# Build + compress all four binaries in the devcontainer (ctxloom + ltk +
# taskloom + harp, each UPX-compressed). Delegates to the container `build-compressed`.
build-compressed: dev-image
    just _run build-compressed

# Build all four binaries UNCOMPRESSED in the devcontainer (fast-starting
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

# Build the standalone harp ID-generator binary in the devcontainer into
# bin/harp, via the harp module. harp is independently distributable (plan
# WS-7: extracted from the removed `ctxloom harp` subcommand) — it ships as
# its own release artifact (.goreleaser.yml), not as one of the three
# binaries `install` puts on the host PATH.
build-harp: dev-image
    just _run harp::build

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

# Regenerate cmd/ltk/sample.ltk.yaml (the shipped default rule set, embedded in
# the ltk binary) from the ```yaml blocks in docs/ltk/DEFAULTS.md, the source
# of truth. The tool also has a -check form that fails on drift, but nothing
# invokes it — no lefthook hook, no CI step — so this is a run-by-hand recipe,
# not a gate. See docs/architecture/companions/ltk.md, "Invariants".
defaults:
    go run ./internal/ltk/tools/extract-defaults

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

# THE developer/agent entry point: runs BOTH test groups and fails if EITHER
# fails.
#
# `test` is an aggregate over two targets that can also be run alone:
#
#   test-default  the untagged suite (`go test ./...`, plain and -race)
#   test-arch     the architectural class gates, behind `-tags arch`
#
# CI does NOT call this recipe — .github/workflows/ci.yml invokes test-default
# and test-arch as two separate steps, so a red step names the class of thing
# that broke. This aggregate exists because humans and agents type `just test`,
# and every remediation batch in this programme is briefed to gate on its exit
# code. A wrapper that ran both halves and returned only the LAST exit code
# would report green while an architectural invariant was violated — a
# false-green generator wired into the workflow that depends on it. Hence the
# explicit per-group exit-code capture below rather than a bare dependency
# list: both groups always run, both verdicts are printed, and ANY failure
# exits non-zero naming the group.
test:
    #!/usr/bin/env bash
    # No `set -e`: each group's exit code is captured and checked explicitly, so
    # a failing first group cannot abort before the second runs and cannot be
    # masked by the second succeeding.
    set -uo pipefail
    failed=()
    echo "===== [1/2] default suite (untagged) — just test-default"
    if ! just test-default; then failed+=("test-default"); fi
    echo ""
    echo "===== [2/2] architectural invariants (-tags arch) — just test-arch"
    if ! just test-arch; then failed+=("test-arch"); fi
    echo ""
    if [ "${#failed[@]}" -ne 0 ]; then
        echo "FAILED GROUP(S): ${failed[*]}" >&2
        echo "  test-default = the ordinary untagged suite (go test ./..., plain + -race)" >&2
        echo "  test-arch    = the architectural class gates (-tags arch, -run TestArch_)" >&2
        echo "Re-run just that group to iterate on it." >&2
        exit 1
    fi
    echo "both groups passed: test-default + test-arch"

# The untagged suite — everything that is NOT an architectural class gate.
# Builds ctxloom first for acceptance tests.
# Coverage is filtered through .coverignore so generated files
# (protobuf, gRPC) don't drag the reported number down.
# vet-integration is the tag-gated-test compile rot gate (see its comment).
#
# Run `just test` (which adds test-arch) unless you specifically want this half
# alone. This target carries no `-tags arch`, so the 38 TestArch_ gates do NOT
# run here.
#
# SCOPE (soft-elm, measured 2026-07-21): exit 0 from `just test-default` means
# unit/race suites green + `tests/integration/...` (the `-tags integration`
# build fence) COMPILES — it does NOT mean the integration tests ran.
# Neither `go test` invocation below carries `-tags integration`, so those
# tests are silently skipped by this recipe (vet-integration only
# type-checks them). Measured cost of folding `test-integration` in here
# was real but not prohibitive (~47s added to a ~260s baseline, +18%); kept
# separate anyway because changing what `just test` covers changes what CI
# gates on, which needs a deliberate decision, not a side effect of a
# doc pass. CI itself is NOT blind to this — .github/workflows/ci.yml runs
# `test-integration` and `test-acceptance` as their own required jobs — but
# a local `just test` (or an agent verifying "green" by that alone) is. Run
# `just test-integration` (CLI/bundle/path changes) and `just test-acceptance`
# (cross-surface journeys) explicitly for real local verification — see
# README's Development section.
test-default: build _ensure-covdata vet-integration
    #!/usr/bin/env bash
    set -e
    # Gate on BOTH configurations, not just -race. gremlins and `just cover`
    # both run WITHOUT -race, and that no-race path caught a real flake in
    # internal/lm/grpc that -race's slower goroutine scheduling was masking —
    # -race made the bug pass, it didn't report it (a genuine data race would
    # have shown up red under -race, not green). Before this, that config ran
    # constantly but gated nothing, which is exactly how the flake stayed
    # invisible to `just test` while breaking mutation testing outright. `set
    # -e` means either run failing aborts here with a nonzero exit, before the
    # leak check / coverage filtering below ever runs.
    go test ./...
    # Unique per-invocation raw profile (not a fixed repo-root name): two
    # concurrent `just test`/`just cover` runs used to share coverage.raw.out,
    # so one run's `rm -f` could delete the file the other was still reading,
    # and the loser died with "missing coverage.raw.out". The EXIT trap covers
    # both the happy path and `set -e` aborting mid-recipe.
    raw="$(mktemp coverage.raw.XXXXXX.out)"
    trap 'rm -f "$raw"' EXIT
    go test -race -coverprofile="$raw" ./...
    just _check-no-ctxloom-leak
    just _filter_coverage "$raw" coverage.out

# Fail (and clean up) if any test wrote a nested internal/**/.ctxloom into the
# source tree instead of isolating through t.TempDir(). internal/operations'
# TestMain catches this for itself; other packages had no such guard, so a
# regression there was caught by nothing but a .gitignore rule for
# internal/**/.ctxloom — which hides the symptom (git status stays clean) but
# the directory still physically exists, which is what confuses worktree-safe
# WIP detection and blocks worktree reaping. This runs after every `just
# test`, so the leak is a build failure instead of invisible disk residue.
_check-no-ctxloom-leak:
    #!/usr/bin/env bash
    set -e
    leaked="$(find internal -mindepth 2 -type d -name .ctxloom 2>/dev/null)"
    if [ -n "$leaked" ]; then
        echo "$leaked" | xargs -I{} rm -rf {}
        echo "TEST ISOLATION FAILURE: a test wrote a nested .ctxloom into the source tree (should use t.TempDir()):" >&2
        echo "$leaked" >&2
        exit 1
    fi

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
    raw="$(mktemp coverage.raw.XXXXXX.out)"
    trap 'rm -f "$raw"' EXIT
    go test -coverprofile="$raw" ./... > /dev/null 2>&1
    just _filter_coverage "$raw" coverage.out
    echo "Coverage (excluding patterns from .coverignore):"
    go tool cover -func=coverage.out | tail -1

# Show per-function coverage (excludes patterns in .coverignore)
cover-func:
    #!/usr/bin/env bash
    set -e
    raw="$(mktemp coverage.raw.XXXXXX.out)"
    trap 'rm -f "$raw"' EXIT
    go test -coverprofile="$raw" ./... > /dev/null 2>&1
    just _filter_coverage "$raw" coverage.out
    echo "Coverage by function (excluding patterns from .coverignore):"
    go tool cover -func=coverage.out

# Generate HTML coverage report (excludes patterns in .coverignore)
cover-html:
    #!/usr/bin/env bash
    set -e
    raw="$(mktemp coverage.raw.XXXXXX.out)"
    trap 'rm -f "$raw"' EXIT
    go test -coverprofile="$raw" ./... > /dev/null 2>&1
    just _filter_coverage "$raw" coverage.out
    go tool cover -html=coverage.out -o coverage.html
    echo "Coverage report generated: coverage.html"

# Run tests with coverage (legacy alias)
test-coverage: cover

# Run the cross-agent equity conformance suite (claude/gemini/codex through the
# shared agent.SettingsWriter contract). Tag-gated so it's excluded from the
# default `go test ./...`; run it explicitly here.
test-conformance:
    go test -race -tags conformance ./internal/lm/conformance/...

# Validate ONE vendor-transcript importer in isolation (its own package,
# already part of `go test ./...`, but named here so a release-monitoring job
# can point at exactly this engine's parser against a fresh vendor transcript
# without pulling in the rest of the suite). Add a sibling target per engine
# as internal/transcript/importer/<engine> lands (kiro/claude/antigravity).
test-vendor-codex:
    go test -race ./internal/transcript/importer/codex/...

# Validate the antigravity vendor-transcript importer in isolation — see
# test-vendor-codex's comment above, same rationale, one target per engine.
test-vendor-antigravity:
    go test -race ./internal/transcript/importer/antigravity/...

# Validate the kiro vendor-transcript importer in isolation. Its own fixture
# is a sqlite db built at test time (see
# internal/transcript/importer/kiro/testdata/MANIFEST.json) via
# modernc.org/sqlite, the pure-Go (CGO_ENABLED=0-safe) driver this package
# isolates to itself — -race here also exercises that driver under the race
# detector, not just this package's own goroutine-free logic.
test-vendor-kiro:
    go test -race ./internal/transcript/importer/kiro/...

test-vendor-claude:
    go test -race ./internal/transcript/importer/claude/...

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
vet-integration: _require-generated
    go vet -tags integration ./tests/...

# Run integration tests (requires ctxloom binary)
test-integration: build
    go test -v -tags integration ./tests/integration/...

# Run integration tests matching a -run PATTERN (requires ctxloom binary).
# Same false-green hazard as test-pkg: a PATTERN that matches nothing still
# exits 0 from `go test` (`[no tests to run]`). Detect and fail on that.
test-integration-run PATTERN: build
    #!/usr/bin/env bash
    set -euo pipefail
    set +e
    output=$(go test -v -tags integration -run "$1" ./tests/integration/... 2>&1)
    status=$?
    set -e
    printf '%s\n' "$output"
    if [ "$status" -ne 0 ]; then
        exit "$status"
    fi
    if grep -q '\[no tests to run\]' <<<"$output"; then
        echo "error: -run matched no tests (typo'd or renamed test name?)" >&2
        exit 1
    fi

# Run the full-stack acceptance suite (godog): asserts each change across files,
# CLI, and mock-agent MCP traffic. Hermetic by default (@live scenarios skipped).
# Build runs in the devcontainer; the suite runs on the host like integration.
# -v: without it, `go test` on a passing package buffers ALL of the test
# binary's stdout (including the live-engine availability report printed once
# up front, AND godog's own "pretty" per-scenario output) and only shows it on
# failure — which had silently made every green CI run of this suite mute.
# The report exists specifically so a run tells you what it covered even when
# it passes; that only works if it is actually visible.
test-acceptance: build
    go test -v -tags "acceptance integration" -count=1 ./tests/acceptance/...

# Run the @container acceptance rows — the ones that actually launch an engine
# inside a container (j15_container.feature's differential host-vs-container
# row). Excluded from `test-acceptance` for one measured reason: the first
# containerized run BUILDS the agent image, which took 75s for a minimal
# fixture and over six minutes in this repo, against a suite that already
# brushes go test's 10m default. The image tag is a hash of the ctxloom binary
# plus companions plus base config, so any binary change mints a new tag and
# pays that cost again — there is no warm cache to rely on in CI.
#
# Self-skips loudly, naming what is missing, when no docker/podman daemon is
# reachable: this gate asserts what happens when a runtime IS present, and a
# machine without one has nothing to say about it. The longer timeout is the
# image build, not a slow test.
test-acceptance-container: build
    ACCEPTANCE_PATHS=features/j15_container.feature \
    ACCEPTANCE_TAGS="@container" \
    go test -v -timeout 30m -tags "acceptance integration" -count=1 ./tests/acceptance/...

# test-docker-integration lives in build/gates.justfile, imported at the top
# of this file and by justfile.container, so the host recipe and the one CI
# runs are the SAME recipe over the SAME package list. What it covers:
#   internal/lm/isolation      — the gRPC container transport + the
#                                force-removal-on-Kill boundary end to end,
#                                including a real git worktree mounted in;
#   internal/agentcoord/coord  — the docker-direct delegated spawn
#                                (TestCoordContainerDirect_NoPluginNoPort),
#                                the owner-owned top-level container runs
#                                (TestCoordOwnerRun_*) and the container
#                                progress/liveness trio
#                                (TestCoordContainerProgress_*);
#   internal/vpio/dockerexec   — the interactive docker-exec turn;
#   internal/acp               — containerTransport against a real container;
#   internal/testsupport/containercell
#                              — the hermetic container cell's three-runtime
#                                matrix (docker rootful, docker rootless,
#                                podman), asserting delivered bytes, POSIX mode
#                                AND OWNERSHIP on the host side of a bind mount.
#                                Ownership is the axis nothing else covers: a
#                                rootful daemon writes byte-identical,
#                                mode-identical, ROOT-OWNED files.
# Reachability is mandatory under CTXLOOM_REQUIRE_DOCKER=1 (set in CI only);
# locally the tests still self-skip so a machine without docker isn't blocked.

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
        -e CTXLOOM_LIVE_REQUIRE=claude \
        -e GOCACHE=/home/ctxloom/.cache/go-build \
        -e GOMODCACHE=/home/ctxloom/go/pkg/mod \
        -e GOPATH=/home/ctxloom/go \
        -e GOFLAGS=-mod=readonly \
        -e GOWORK=off \
        -w /workspace \
        {{registry}}/ctxloom-acceptance:latest \
        bash -c 'set -e; \
            go build -o /home/ctxloom/ctxloom . && \
            CTXLOOM_BINARY=/home/ctxloom/ctxloom go test -v -tags "acceptance integration" -count=1 ./tests/acceptance/...'

# Run the standalone isolation probe (tests/acceptance/features/
# isolation_probe.feature) for exactly ONE engine x axis cell — the
# per-engine-release regression check, not the whole live suite. ENGINE is
# one of claude-code|codex|kiro|opencode|antigravity; AXIS is worktree or
# container (or "bypass" for the engine's env-API-key-forced worktree row,
# or "kiro-leak" for the dedicated --degraded credential-store-leak proof —
# that one ignores ENGINE/AXIS). Makes AT MOST one real, paid engine call.
# Requires real credentials for ENGINE (a host credential file, or its
# API-key env var) — self-skips loudly, naming exactly what is missing, when
# absent. See website/src/content/docs/security/isolation.md's "The
# executable probe" section.
isolation-probe ENGINE AXIS: build
    ACCEPTANCE_PATHS=features/isolation_probe.feature \
    ACCEPTANCE_TAGS="@live && @{{ENGINE}} && @{{AXIS}}" \
    CTXLOOM_ACCEPTANCE_LIVE=1 \
    go test -v -tags "acceptance integration" -count=1 ./tests/acceptance/...

# Run a single package's tests under -race (fast local iteration).
# A `-run` pattern that matches nothing still exits 0 from `go test` (`ok
# ... [no tests to run]`) — silently passing a typo'd or renamed test name.
# Detect that and fail. A package with genuinely no test files reports a
# different message (`? ... [no test files]`) and stays green, as does a
# `-run` miss the pipefail chain never triggers because a package that
# doesn't build/vet fails before printing either message.
#
# ⚠ THIS RECIPE IS VACUOUS FOR ./tests/acceptance/... — taskloom
# unvaried-onlooker. It applies no `acceptance` build tag, so every file in
# that package is excluded from the build, nothing runs, and it exits 0 in
# about a second REGARDLESS. The `[no tests to run]` guard below does not
# catch it: a package built to zero test files reports `[no test files]`,
# which is treated as green. `just test-acceptance` is the only gate that
# runs those scenarios — do not cite this one for them.
test-pkg PKG *ARGS: _require-generated
    #!/usr/bin/env bash
    set -euo pipefail
    pkg="$1"; shift
    set +e
    output=$(go test -race "$@" "$pkg" 2>&1)
    status=$?
    set -e
    printf '%s\n' "$output"
    if [ "$status" -ne 0 ]; then
        exit "$status"
    fi
    if grep -q '\[no tests to run\]' <<<"$output"; then
        echo "error: -run matched no tests in $pkg (typo'd or renamed test name?)" >&2
        exit 1
    fi

# ===== Mutation testing =====

# `mutation_tmp` and the whole-tree `test-mutation` come from build/ci.justfile
# (imported at the top of this file AND by justfile.container), alongside the
# diff-only `test-mutation-diff` that gates every push. They live there because
# CI runs them: mutation-weekly.yml used to call a bare `gremlins unleash`,
# which is the same operation WITHOUT the TMPDIR pinning below — the recipe
# existed here and CI ran past it. The recipes below are host-only and keep
# using the shared `mutation_tmp`.

# Run mutation tests on specific package
# gremlins appends /... to the target itself; passing it here yields
# ./pkg/.../... which matches nothing and fails with "no packages to test".
# "$@" (not {{ARGS}}), same reasoning as test-mutation above.
test-mutation-pkg PKG *ARGS:
    #!/usr/bin/env bash
    set -euo pipefail
    mkdir -p "{{mutation_tmp}}"
    trap 'rm -rf "{{mutation_tmp}}"/gremlins-*' EXIT
    pkg="$1"; shift
    TMPDIR="{{mutation_tmp}}" gremlins unleash "./$pkg" "$@"

# Run mutation tests against the ACCEPTANCE/journey suite
# (.gremlins.acceptance.yaml), not the unit suite .gremlins.yaml normally
# measures. See that config's header comment for why this profile exists and
# its one load-bearing subtlety: GOFLAGS restricts every `go test` gremlins
# runs here (both the coverage gather and every per-mutant retest) to the
# acceptance package's entrypoint (TestAcceptance) only, so a KILLED verdict
# can only have come from the journeys, never from internal/operations' own
# (extensive) unit tests riding along on the `./...` scan that
# unleash.integration:true forces.
#
# Builds the ctxloom binary once, up front, at an ABSOLUTE path outside any of
# gremlins' per-worker scratch copies — the acceptance suite always execs a
# pre-built binary (tests/integration/testenv's exec.Command), so CTXLOOM_BINARY
# must resolve regardless of which copy's cwd is active when a worker runs.
#
# KNOWN LIMITATION (see the config file's own comment, confirmed empirically
# before this recipe was written): mutating source under internal/operations
# can never change what that already-built, frozen binary does when the suite
# execs it, so a mutant whose only effect is on a subprocess-only code path
# will report NOT COVERED here even when the journeys genuinely exercise it —
# Go's coverage instrumentation cannot see across the exec boundary. That is a
# floor on what this tool can prove, not proof the journeys don't cover it.
#
# Pass --dry-run to only count candidate mutants without executing anything
# (use this first — see the config's scope-narrowing comment on cost).
test-mutation-acceptance *ARGS: build
    #!/usr/bin/env bash
    set -euo pipefail
    mkdir -p "{{mutation_tmp}}"
    trap 'rm -rf "{{mutation_tmp}}"/gremlins-*' EXIT
    export CTXLOOM_BINARY="$(pwd)/ctxloom"
    export GOFLAGS="-run=TestAcceptance"
    set +e
    output=$(TMPDIR="{{mutation_tmp}}" gremlins --config .gremlins.acceptance.yaml unleash ./internal/operations {{ARGS}} 2>&1)
    status=$?
    set -e
    printf '%s\n' "$output"
    if [ "$status" -ne 0 ]; then
        exit "$status"
    fi
    # A run with zero RUNNABLE mutants has measured nothing, and must not report
    # success: the configured threshold is efficacy (killed/(killed+lived)), a
    # ratio over an empty set, so it is satisfied vacuously and the gate goes
    # green while proving exactly nothing about the suite.
    if grep -qE 'Runnable: 0([^0-9]|$)' <<<"$output"; then
        echo "error: 0 runnable mutants — this gate measured NOTHING (efficacy over an empty mutant set passes vacuously)." >&2
        echo "       Coverage-gated mutation testing cannot observe this suite: it execs a PRE-BUILT ctxloom binary, so a" >&2
        echo "       mutant is never present in the process under test and Go coverage cannot cross the exec boundary." >&2
        echo "       \`just test-mutation-cucumber\` is the harness that actually works — it rebuilds the binary from each mutant." >&2
        exit 1
    fi

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

# Mutate internal/operations/trust.go and drive the CUCUMBER acceptance suite
# against a binary rebuilt from each mutant (github.com/gtramontina/ooze), not
# `go test` against source like test-mutation-acceptance above. That is the
# whole point: test-mutation-acceptance mutates source and runs `go test`,
# but the acceptance suite execs a PRE-BUILT ctxloom binary
# (tests/integration/testenv/environment.go) — a gremlins mutant never
# reaches that already-compiled process (measured: 92 mutants on this same
# file, 0 runnable, 92 NOT COVERED). ooze's laboratory instead symlinks the
# repo into a tmpdir, overwrites ONLY the mutated file with real bytes at
# that path (never the source tree), and runs
# tests/mutation/run_scoped_suite.sh with that tmpdir as cwd — which
# rebuilds ctxloom FROM the mutant and then runs the cucumber suite scoped
# (ACCEPTANCE_PATHS) to trust_surface.feature + j3_corporate_signed.feature +
# j7_incident.feature, the three that claim to cover the EffectiveTrust
# cascade exhaustively. A survivor here is a mechanism one of those features
# CLAIMS to cover but does not actually verify.
#
# Cost: every mutant is a full build + a ~15-20s scoped suite run (measured
# ~25-30s/mutant on this machine). Nightly/scoped, never a per-PR gate.
# Scope is fixed to internal/operations/trust.go inside the test file itself
# (tests/mutation/trust_cascade_mutation_test.go), built programmatically by
# walking the repo and ignoring every other .go file — see that file's doc
# comment for why (RE2 has no lookahead; ooze's own file discovery is as
# scope-blind as gremlins').
test-mutation-cucumber *ARGS:
    #!/usr/bin/env bash
    set -euo pipefail
    set +e
    output=$(go test -tags mutation -count=1 -timeout 120m ./tests/mutation/... "$@" 2>&1)
    status=$?
    set -e
    printf '%s\n' "$output"
    if [ "$status" -ne 0 ]; then
        exit "$status"
    fi
    if grep -q '\[no tests to run\]' <<<"$output"; then
        echo "error: -run matched no tests (typo'd or renamed test name?)" >&2
        exit 1
    fi

# Clean build artifacts
clean:
    rm -f ctxloom
    rm -rf bin/ man/
    go clean

# Reclaim regenerable Go caches (build cache + leftover temp/aux caches)
clean-caches:
    #!/usr/bin/env bash
    # The build cache (~/.cache/go-build) has NO native size cap and grows large
    # under heavy multi-agent build days (98G in one day here); Go's own 5-day
    # trim is healthy but does not bound total size. Does NOT touch the module
    # cache (~/go/pkg/mod) — expensive to refetch and not the problem. The aux
    # dirs hold Go module-cache copies at mode 0444, so chmod -R u+w before rm.
    set -uo pipefail
    echo "before: $(df -h "$HOME" | awk 'NR==2{print $4" free, "$5" used"}')"
    go clean -cache
    # RECREATE after removing. ~/.cache/gotmp is this machine's configured
    # GOTMPDIR (`go env GOTMPDIR`, persisted in ~/.config/go/env), and Go does
    # NOT create it on demand: with the directory gone, every subsequent go
    # invocation dies with "creating work dir: stat .../gotmp: no such file or
    # directory". Deleting it therefore broke `just test`, `just lint` and
    # `just test-acceptance` outright — measured, after this recipe reclaimed
    # 145 GB and left the tree unbuildable. Emptying reclaims the same space;
    # only the directory itself has to survive.
    for d in "$HOME/.cache/gotmp" "$HOME/.cache/ctxloom-agent-tmp" "$HOME/.cache/goimports"; do
        [ -d "$d" ] || continue
        chmod -R u+w "$d" 2>/dev/null || true
        rm -rf "$d" 2>/dev/null || echo "  (some of $d was container-owned and left in place)"
        mkdir -p "$d" 2>/dev/null || true
    done
    echo "after:  $(df -h "$HOME" | awk 'NR==2{print $4" free, "$5" used"}')"

# Report Go cache sizes and, above LIMIT_GB, evict the oldest build-cache
# entries until the build cache is back under the limit. The SIZE bound that
# `clean-caches` (all-or-nothing) and Go itself do not provide.
#
# WHY: ~/.cache/go-build is one unbounded directory shared by the host, gopls,
# every worktree, and every `just _run` container (see the GOCACHE mount in
# _run — that sharing is deliberate and stays). Go's own trim only evicts
# entries unused for ~5 days, so the cache is bounded at ~5 days of churn;
# a heavy multi-agent day here produces ~100 GB, so 5 days is ~500 GB. That is
# how /home reached 99% full with a 218 GB build cache while Go's trim was
# working exactly as designed. Worktrees compound it: without -trimpath the
# compiler embeds absolute source paths, so the same package built in 13
# ctxloom worktrees is 13 distinct cache entries.
#
# Eviction is age-based, the same mechanism as Go's own trim: delete
# <hash>-a/<hash>-d files older than a cutoff, tightening the cutoff until
# under the limit. A missing entry is a plain cache miss, never a wrong build,
# so partial eviction is always safe. NEVER touches ~/go/pkg/mod (expensive to
# refetch, and not the problem).
#
#   just cache-report        report, evict above the 40 GB default
#   just cache-report 0      report only, evict nothing
cache-report LIMIT_GB="40":
    #!/usr/bin/env bash
    set -uo pipefail
    gbc="$(go env GOCACHE 2>/dev/null || echo "$HOME/.cache/go-build")"
    gmc="$(go env GOMODCACHE 2>/dev/null || echo "$HOME/go/pkg/mod")"
    size_gb() { du -sk "$1" 2>/dev/null | awk '{printf "%.1f", $1/1048576}'; }
    limit="{{LIMIT_GB}}"
    echo "GOCACHE     $gbc: $(size_gb "$gbc") GB (limit ${limit} GB)"
    echo "GOMODCACHE  $gmc: $(size_gb "$gmc") GB (never trimmed here)"
    echo "disk:       $(df -h "$HOME" | awk 'NR==2{print $4" free, "$5" used"}')"
    if [ ! -d "$gbc" ] || [ "${limit%%.*}" = "0" ]; then exit 0; fi
    # 5d, 3d, 2d, 1d, 12h, 6h, 2h — stop as soon as we are under the limit.
    for mins in 7200 4320 2880 1440 720 360 120; do
        over=$(awk -v a="$(size_gb "$gbc")" -v b="$limit" 'BEGIN{print (a>b)?1:0}')
        [ "$over" = "1" ] || break
        echo "  over limit — evicting entries unused for >${mins} min"
        find "$gbc" -type f \( -name '*-a' -o -name '*-d' \) -mmin +"$mins" -delete 2>/dev/null
    done
    echo "after:      $(size_gb "$gbc") GB"

# Prune ephemeral docker images this repo's tooling produces — per-agent-run
# images (ctxloom-agent:<hash>), integration-test images (ctxloom-*-itest,
# ctxloom-iso*, ctxloom-coord*), and VS Code devcontainer builds (vsc-*) — plus
# dangling layers, once they are older than HOURS (default 6). These accumulate
# fast under agent/isolation work and are the bulk of docker disk here.
#
# PRESERVED (never pruned): named registry/base images (ghcr.io/*, postgres,
# alpine, etc.), and the warm :latest reuse caches of the agent/base families
# (ctxloom-agent{,-base}:latest, ctxloom-devcontainer:latest, ...) which are
# slow to rebuild. Images backing a live container are skipped by `docker rmi`.
docker-prune HOURS="6":
    #!/usr/bin/env bash
    set -euo pipefail
    cutoff=$(date -d '{{HOURS}} hours ago' +%s)
    # Repos where EVERY tag (incl. :latest) is per-run throwaway.
    always='^(ctxloom-[^:]*-itest|ctxloom-iso[^:]*|ctxloom-coord[^:]*|vsc-)'
    # Repos where only the content-hash tags are throwaway; :latest is the cache.
    hash_only='^(ctxloom-agent|ctxloom-agent-base)$'
    removed=0
    while IFS='|' read -r ref created repo tag; do
        [ -z "$ref" ] && continue
        if echo "$repo" | grep -qE "$always"; then
            :
        elif echo "$repo" | grep -qE "$hash_only" && [ "$tag" != "latest" ]; then
            :
        else
            continue
        fi
        # docker CreatedAt: "2026-07-16 20:00:00 -0700 PDT" — keep date/time/offset,
        # drop the trailing tz NAME which `date -d` can't parse.
        cts=$(date -d "$(echo "$created" | awk '{print $1, $2, $3}')" +%s 2>/dev/null || echo 0)
        { [ "$cts" -eq 0 ] || [ "$cts" -ge "$cutoff" ]; } && continue
        if docker rmi "$ref" >/dev/null 2>&1; then
            echo "removed $ref"
            removed=$((removed + 1))
        fi
    done < <(docker images --format '{{ "{{.Repository}}:{{.Tag}}|{{.CreatedAt}}|{{.Repository}}|{{.Tag}}" }}')
    echo "Pruned $removed ephemeral image(s) older than {{HOURS}}h."
    docker image prune -f --filter "until={{HOURS}}h" >/dev/null
    echo "Pruned dangling layers older than {{HOURS}}h."
    # Build cache pins the layers the image prune above frees, so physical disk
    # is not reclaimed until this runs too. Same age floor keeps in-flight work.
    docker builder prune -f --filter "until={{HOURS}}h" >/dev/null
    echo "Pruned build cache older than {{HOURS}}h."

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

# Enforcing gate: fail on any NEW or newly-worsened CCN>10 violation, ratcheted
# against .complexity-baseline.txt (used by the CI lint job). Pre-existing
# violations don't fail it; see that file's header and scripts/complexity_gate.py.
complexity-check: dev-image
    just _run complexity-check

# Deliberately regenerate .complexity-baseline.txt from the current tree.
# Never run this to make a genuinely new violation "go away" without review —
# always look at the diff first. See .complexity-baseline.txt's header.
complexity-baseline-update: dev-image
    just _run complexity-baseline-update

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
    go run ./internal/agentcoord/mcpschema/gen -descriptor "$tmp" \
        -out internal/agentcoord/mcpschema/schemas \
        -xmllike-out internal/agentcoord/xmllike_gen.go

# Generate the reference docs for all three binaries from their sources of
# truth: the CLI reference (man pages + website markdown) from each cobra
# command tree, the MCP reference from the live tool/resource registrations, and
# ctxloom's and taskloom's config references from their tracked JSON Schemas. One generator
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
        --mcp website/src/content/docs/taskloom/reference \
        --config website/src/content/docs/taskloom/reference \
        --config-schema resources/schema/input/taskloom-config-schema.json
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

# `docs-deps` (npm ci) and `docs-build` (npm run build) come from
# build/ci.justfile — .github/workflows/docs.yml runs them, so they are shared
# with justfile.container rather than defined only here.

# Preview production docs build
docs-preview:
    cd website && npm run preview

# Generate the "living docs" journey pages: run the acceptance suite with
# per-scenario evidence capture enabled, then render website/src/content/docs/
# journeys/ from that run's REAL captured output (tests/acceptance/
# steps_doc_capture.go + scripts/gendocs/livingdocs). The generator refuses
# (nonzero exit, writes nothing) if any @doc scenario's capture has a
# non-passed step — a broken feature cannot be documented.
#
# Generated pages are gitignored, not checked in (see .gitignore): they are
# produced fresh from this run's capture, so they can never be stale. Neither
# `docs` (dev server) nor `docs-build` depends on this — like `gen-docs`
# (the CLI/MCP/config reference generator), it is a separate, explicit step so
# a docs preview never forces a full acceptance run. CI's docs deploy workflow
# (.github/workflows/docs.yml) runs it explicitly before `npm run build`.
gen-living-docs: build
    #!/usr/bin/env bash
    set -euo pipefail
    # Absolute path: `go test ./tests/acceptance/...` runs with its cwd set to
    # the package directory (tests/acceptance/), not the repo root, so a
    # relative CTXLOOM_DOC_CAPTURE_DIR would silently land one level down and
    # every scenario would render as "not captured" — the generator, run
    # separately via `go run` from the repo root, would never find it.
    capture_dir="$(pwd)/.cache/doc-capture"
    rm -rf "$capture_dir"
    CTXLOOM_DOC_CAPTURE_DIR="$capture_dir" go test -tags "acceptance integration" -count=1 ./tests/acceptance/...
    go run ./scripts/gendocs/livingdocs --capture-dir "$capture_dir"

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

# Build the shared agent-image BASE stage (ctxloom-agent-base:latest): the distro
# plus the coding-agent tool layer (git, ripgrep, curl, certs, unzip, jq). The
# composed multi-engine agent stage (isolation.composeAgentContainerfile) layers
# onto it via --build-arg BASE_IMAGE. To bring your own base instead, use
# `ctxloom container build --base-containerfile <file>` (or config
# isolation_base_containerfile) — or a project .devcontainer/devcontainer.json
# auto-detects (isolation_devcontainer_base).
container-build-base:
    #!/usr/bin/env bash
    set -euo pipefail
    ctx=$(mktemp -d)
    trap 'rm -rf "$ctx"' EXIT
    cp container/base/Containerfile "$ctx/Containerfile"
    {{container_cmd}} build -t ctxloom-agent-base:latest -f "$ctx/Containerfile" "$ctx"

# Build a locally-built ctxloom binary, then delegate to `ctxloom container
# build <backend> [--engines ...]` — the SAME composed multi-engine
# Containerfile generator (base resolution + per-engine official-installer
# fragments) the on-the-fly build uses, so this ahead-of-time path and a
# `ctxloom run` build byte-identical images for the same config. Passes
# --no-devcontainer-base: THIS recipe is a fast local smoke-build of the
# composed-engine mechanism, not "what a real run in this checkout would use"
# — ctxloom's OWN .devcontainer/ (a heavy CGO/ONNX toolchain image, and one
# that declares `features:`) would otherwise become the base here. A real
# `ctxloom run --runtime container` (or an explicit `ctxloom container build`)
# still auto-detects normally; this recipe opts out deliberately.
_container-build-via-cli backend *engines:
    #!/usr/bin/env bash
    set -euo pipefail
    bin="./ctxloom-build-tmp-$$"
    trap 'rm -f "$bin"' EXIT
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOWORK=off go build \
        -ldflags "-X github.com/ctxloom/ctxloom/internal/cli.Version={{version}}" \
        -o "$bin" ./cmd/ctxloom
    args=(container build {{backend}} --no-devcontainer-base)
    if [ -n "{{engines}}" ]; then args+=(--engines "{{engines}}"); fi
    "$bin" "${args[@]}"

# Build the claude-code agent image (the composed multi-engine image, tagged
# by its resolved content — see `ctxloom container check claude-code` for the
# resolved tag).
container-build-claude: (_container-build-via-cli "claude-code")

# Build the kiro agent image (the composed multi-engine image; see
# container-build-claude).
container-build-kiro: (_container-build-via-cli "kiro")

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

# Build devcontainer image. Tool versions (Go, buf, protoc-gen-go, ...) are
# NOT hardcoded here or in the Dockerfile — .devcontainer/tool-versions.env is
# their one source of truth, and every entry in it is passed through as a
# --build-arg so the Dockerfile's (deliberately default-less) ARGs resolve.
# .github/workflows/ci.yml's build-container job builds the same Dockerfile
# without going through `just` (via docker/build-push-action), so it loads
# the same file into that action's build-args input instead of this recipe —
# see that workflow. internal/buildpins' drift-gate test fails if either
# consumer's build-args stop matching this file.
dev-image:
    #!/usr/bin/env bash
    set -euo pipefail
    build_args=()
    while IFS= read -r line; do
        [[ -z "$line" || "$line" == \#* ]] && continue
        build_args+=(--build-arg "$line")
    done < .devcontainer/tool-versions.env
    {{container_cmd}} build "${build_args[@]}" -t {{devcontainer_image}}:latest -f .devcontainer/Dockerfile .

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
        # Persist Go's BUILD cache on the host as well, so container builds reuse
        # compiled output across `--rm` runs instead of recompiling cold every
        # time. Shared with the host's own GOCACHE (~/.cache/go-build). Cache
        # entries are keyed by toolchain version, so host and container reuse
        # each other's output only when their `go version` matches; mismatched
        # entries coexist safely as plain cache misses, never wrong builds.
        gobuild_mount=()
        gbc="$HOME/.cache/go-build"
        if mkdir -p "$gbc" 2>/dev/null; then gobuild_mount=(-v "$gbc:/tmp/.gocache"); fi
        {{container_cmd}} run --rm \
            "${user_flag[@]}" \
            "${cache_mount[@]}" \
            "${gobuild_mount[@]}" \
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
    go test -v -tags "acceptance integration" -count=1 ./tests/acceptance/...
