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

# The test suites build a ~30MB taskloom binary of their own
# (testenv.TaskloomBinary for acceptance, tasksBinary for tests/taskloom) and
# both site it under GOTMPDIR — because on a tmpfs /tmp the linker's mmap of
# that output ENOSPCs under the suite's parallel builds. GOTMPDIR was UNSET
# here, so MkdirTemp fell back to os.TempDir() = the exact tmpfs those helpers
# were trying to avoid: the intent was written into the code and never wired
# up. Default it to disk so a future leak (or just a big parallel run) cannot
# kill the tmpfs. `go build` honors it for its own intermediates too. Same
# shape and same reason as `mutation_tmp` in build/ci.justfile.
#
# Recipes that use it depend on _ensure-gotmpdir: Go does NOT create GOTMPDIR
# on demand — with the directory missing, every go invocation dies with
# "creating work dir: ... no such file or directory" (see clean-caches, which
# learned that the hard way).
go_tmp := env_var_or_default("CTXLOOM_GOTMPDIR", "/var/tmp/ctxloom-gotmp")

# Create the GOTMPDIR above. Cheap, idempotent, and a dependency rather than a
# global export so a missing directory can never break a recipe that never
# asked for it.
_ensure-gotmpdir:
    @mkdir -p "{{go_tmp}}"

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
    go build -v -ldflags "-X github.com/ctxloom/ctxloom/internal/version.Version={{version}}" -o ctxloom ./cmd/ctxloom

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
test-default: build _ensure-covdata vet-integration _ensure-gotmpdir
    #!/usr/bin/env bash
    set -e
    export GOTMPDIR="{{go_tmp}}"
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
test-verbose: _ensure-gotmpdir
    GOTMPDIR="{{go_tmp}}" go test -v ./...

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

# Validate ONE vendor-transcript reader in isolation (its own package,
# already part of `go test ./...`, but named here so a release-monitoring job
# can point at exactly this engine's parser against a fresh vendor transcript
# without pulling in the rest of the suite). Add a sibling target per engine
# as internal/transcript/vendorreader/<engine> lands (kiro/claude).
#
# It ALSO carries internal/codex's hook-trust vendor pin, which is not a
# transcript reader but has the identical exposure and belongs in the identical
# lane: ctxloom seeds `[hooks.state] trusted_hash` into config.toml, an
# undocumented codex surface whose key format upstream itself calls provisional,
# and a codex release that moves it puts every ctxloom hook back to being
# SILENTLY skipped (see internal/codex/hooktrust.go). The pin asks the installed
# codex for its own verdict over `codex app-server` — free, no credentials, no
# model turn. It SKIPS when no codex is on PATH, so CTXLOOM_VENDOR_PIN=require
# is set here: in this lane an absent codex means the pin graded nothing, which
# must be a failure and not a quiet pass.
#
# The behavioural half (does a seeded hook actually FIRE) buys a model turn and
# stays opt-in behind CTXLOOM_VENDOR_PIN_LIVE=1 — run it when qualifying a new
# codex release, where one turn is cheap next to shipping dead hooks.
test-vendor-codex:
    go test -race ./internal/transcript/vendorreader/codex/...
    CTXLOOM_VENDOR_PIN=require go test -race -run 'TestVendorPin_' ./internal/codex/...

# Validate the kiro vendor-transcript reader in isolation. Its own fixture
# is a sqlite db built at test time (see
# internal/transcript/vendorreader/kiro/testdata/MANIFEST.json) via
# modernc.org/sqlite, the pure-Go (CGO_ENABLED=0-safe) driver this package
# isolates to itself — -race here also exercises that driver under the race
# detector, not just this package's own goroutine-free logic.
test-vendor-kiro:
    go test -race ./internal/transcript/vendorreader/kiro/...

test-vendor-claude:
    go test -race ./internal/transcript/vendorreader/claude/...

# Compile-check the `-tags integration` build fence — a cheap rot gate for
# tag-gated tests (tests/integration/*_test.go). No container needed: vet
# doesn't touch CGO/treesitter, just the generated proto stubs (`just build`
# once in a fresh worktree first). Nothing else on the default path ever
# type-checks this tag: golangci-lint's build-tags list carries only
# `mutation` (see .golangci.yml for why this one is not on it), and
# `test`/coverage exclude build-tagged files by construction. A test file
# that only compiles under the tag can therefore bit-rot silently — exactly
# what happened to acp_agent_test.go (stale agent.ChatRequest.AutoApprove
# field) and acp_live_test.go (claude.NewClaudeCode's old one-arg signature),
# both invisible until something finally ran this. Wired into both `test`
# below and `lint` (justfile.container), so it gates the default local AND CI
# paths. vet, not test/run — stays cheap.
# The SECOND vet line covers the `acceptance` tag, which gates ~23k lines the
# integration tag does not reach. Both matrices are vetted because a package
# that compiles under one tag set and not the other is invisible to `go build
# ./...` either way, and golangci-lint does not build tag-gated files at all.
vet-integration: _require-generated
    go vet -tags integration ./tests/...
    go vet -tags "acceptance integration" ./tests/...

# Run integration tests (requires ctxloom binary)
test-integration: build _ensure-gotmpdir
    GOTMPDIR="{{go_tmp}}" go test -v -tags integration ./tests/integration/...

# Run integration tests matching a -run PATTERN (requires ctxloom binary).
# Same false-green hazard as test-pkg: a PATTERN that matches nothing still
# exits 0 from `go test` (`[no tests to run]`). Detect and fail on that.
#
# Unlike test-pkg this walks a MULTI-package tree, so test-pkg's "any
# `[no tests to run]` line is a miss" rule does not transfer: `.../testenv`
# has test files of its own and reports that line for every PATTERN aimed at
# the sibling package, which made the guard fail runs whose target test had
# just PASSED. Scope the verdict to the whole tree instead — a miss is when
# NO package ran a matching test, i.e. every `ok` line carries the marker.
test-integration-run PATTERN: build _ensure-gotmpdir
    #!/usr/bin/env bash
    set -euo pipefail
    export GOTMPDIR="{{go_tmp}}"
    set +e
    output=$(go test -v -tags integration -run "$1" ./tests/integration/... 2>&1)
    status=$?
    set -e
    printf '%s\n' "$output"
    if [ "$status" -ne 0 ]; then
        exit "$status"
    fi
    if ! grep -E '^ok[[:space:]]' <<<"$output" | grep -qv '\[no tests to run\]'; then
        echo "error: -run matched no tests in any package (typo'd or renamed test name?)" >&2
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
# -timeout 30m is HEADROOM FOR MACHINE LOAD, not evidence the suite is slow.
# Measured on one box, same 515 scenarios, same commit:
#     IDLE            179s   (0.35 s/scenario)
#     load avg 12-16  1200s  (2.33 s/scenario)   — 6.7x penalty
# So the suite fits inside go test's 600s DEFAULT with room to spare when the
# machine is quiet, and blows through it when the machine is busy. That made a
# green/red verdict a function of what else the desktop happened to be doing —
# worse than a slow gate, because it produces confident FALSE REDS that cost
# hours to chase. It is not hanging when the alarm fires; if it ever does hang,
# 30m still bounds it.
#
# 179s IS THE BASELINE TO WATCH. A raised budget hides a real slowdown, and
# this repo has been burned by that exact move before (taskloom stark-dose:
# five "budgets too tight" diagnoses, two of which were real defects the budget
# was concealing). Re-measure on a QUIET box and compare against 179s; do not
# infer anything from a run taken under load.
test-acceptance: build _ensure-gotmpdir
    #!/usr/bin/env bash
    # No `set -e`: the exit code is captured so a red run can skip the sweep and
    # still propagate its own status.
    set -uo pipefail
    marker="{{go_tmp}}/.cache-sweep.$$"
    : > "$marker"
    GOTMPDIR="{{go_tmp}}" go test -v -timeout 30m -tags "acceptance integration" -count=1 ./tests/acceptance/...
    status=$?
    if [ "$status" -eq 0 ]; then just _sweep-cache "$marker"; else rm -f "$marker"; fi
    exit "$status"

# Run a NARROW slice of the acceptance suite: one or more feature files, and
# optionally a tag expression. This is the iteration loop; `just test-acceptance`
# is the MERGE gate.
#
# The full suite is ~450s, which is too slow to sit inside an edit/verify cycle
# and fast enough to run once before a merge. The Go seam for narrowing already
# existed (ACCEPTANCE_PATHS / ACCEPTANCE_TAGS, read by acceptance_test.go); it
# just had no recipe, and a raw `go test` is blocked, so the seam was reachable
# only from the recipes that hard-code their own paths.
#
#   just test-acceptance-focus features/j001400_bundle_distribution.feature
#   just test-acceptance-focus features/j000200_setup.feature,features/j000700_team.feature
#   just test-acceptance-focus features/j002200_isolation.feature "@container"
#
# PATHS is comma-separated and relative to tests/acceptance/.
#
# A run that matches ZERO scenarios FAILS. `go test` exits 0 when godog matches
# nothing, so without that "I looked and found nothing wrong" and "I never
# looked" are the same green tick — and the second is the easy one to get,
# because every scenario in a file can be excluded by the default tag filter
# while the file itself is perfectly real. Same invariant as the
# `[no tests to run]` guard on test-integration-run and the no-score guard on
# test-mutation-cucumber. The two causes get different messages because they
# need different fixes: a path that names no file is a typo, a filter that
# excluded everything needs TAGS.
#
# Leaving TAGS empty keeps the DEFAULT hermetic tag filter (~@live ~@network
# ~@future ~@wip ~@container) and therefore the hermetic lane, where a runtime
# skip is fatal. Passing TAGS replaces that filter outright — it does not
# intersect with it — so a tag expression can opt INTO @live/@wip, and doing so
# leaves the hermetic lane and makes skips non-fatal. Narrowing PATHS alone
# never leaves it.
#
# Narrowing hides regressions in the features you did not name. That is the
# trade this recipe exists to make; the merge gate is what closes it.
test-acceptance-focus PATHS TAGS="": build _ensure-gotmpdir
    #!/usr/bin/env bash
    # No `set -e`: the run's exit code is captured so the zero-scenario verdict
    # below can speak before it propagates.
    set -uo pipefail

    # CAUSE 1: a PATHS entry that names no feature file. godog does refuse this
    # ("feature path ... is not available"), but only from inside the -v
    # firehose, and it names the path without saying what PATHS is relative to.
    missing=()
    IFS=',' read -r -a wanted <<<"{{PATHS}}"
    for p in "${wanted[@]}"; do
        [ -e "tests/acceptance/$p" ] || missing+=("$p")
    done
    if [ "${#missing[@]}" -ne 0 ]; then
        echo "error: PATHS names no feature file: ${missing[*]}" >&2
        echo "       PATHS is comma-separated and relative to tests/acceptance/ — a" >&2
        echo "       renamed or typo'd path would run zero scenarios." >&2
        exit 1
    fi

    log="{{go_tmp}}/.acceptance-focus.$$.log"
    trap 'rm -f "$log"' EXIT
    GOTMPDIR="{{go_tmp}}" \
    ACCEPTANCE_PATHS={{PATHS}} \
    ACCEPTANCE_TAGS="{{TAGS}}" \
    go test -v -timeout 30m -tags "acceptance integration" -count=1 ./tests/acceptance/... 2>&1 | tee "$log"
    status="${PIPESTATUS[0]}"
    if [ "$status" -ne 0 ]; then
        exit "$status"
    fi

    # CAUSE 2: the paths matched, godog ran, and the TAG FILTER left nothing to
    # run. godog says so in one line it also exits 0 on. The count is anchored
    # at line start, ahead of any colouring godog puts on the passed/failed
    # tallies that follow it.
    summary=$(grep -aE '^(No scenarios|[0-9]+ scenarios)' "$log" | head -1)
    if [ -z "$summary" ]; then
        echo "error: the run never said how many scenarios it covered." >&2
        echo "       godog prints that count on every run; without it there is nothing" >&2
        echo "       here that distinguishes a covered run from an empty one. Do not" >&2
        echo "       read this as a pass." >&2
        exit 1
    fi
    if [ "${summary}" = "No scenarios" ]; then
        filter="{{TAGS}}"
        if [ -z "$filter" ]; then
            filter="~@live && ~@network && ~@future && ~@wip && ~@container (the default, because TAGS was empty)"
        fi
        echo "error: the focused run matched ZERO scenarios — it verified NOTHING." >&2
        echo "       Every PATHS entry names a real feature file, so the TAG FILTER" >&2
        echo "       excluded every scenario in them:" >&2
        echo "           $filter" >&2
        echo "       Passing TAGS REPLACES that filter rather than intersecting with it," >&2
        echo "       so opt in with the tag the scenarios actually carry, e.g." >&2
        echo "           just test-acceptance-focus {{PATHS}} \"@container\"" >&2
        echo "       Do not read this as a pass." >&2
        exit 1
    fi

# Build a coverage-instrumented ctxloom.
build-cover: dev-image
    just _run build-cover

# Run the acceptance suite against a COVERAGE-INSTRUMENTED ctxloom and report
# what it actually executed.
#
# WHY THIS EXISTS, and what it is NOT for. completeness_test.go answers
# "was this leaf REACHED?" from testenv.RecordedInvocations() — the argv the
# suite actually started, resolved to a leaf by cobra's own root.Find(). That
# gate is correct and stays: it keeps flag-level credit (`--engine antigravity`
# is a separate row), works in both lanes, and cannot be fooled by a mention.
#
# What it cannot answer is "how MUCH of that leaf ran". A leaf invoked once
# with no flags is fully credited. This lane answers that second question, and
# only that one — it is a DEPTH signal, never the reach gate.
#
# Coverage is measurable here at all only because the suite drives ctxloom as a
# SUBPROCESS and `go test -coverprofile` cannot follow an exec.
#
# DO NOT repoint the reach gate at this data. Measured: a SIGKILLed process
# never flushes its counters, and the harness hard-kills servers — `mcp serve`
# reads 0.0% while running in every @mcp scenario (taskloom unsure-cadet).
#
# `go build -cover` + GOCOVERDIR (Go 1.20+) can: the instrumented binary
# writes counters at exit, once per exec, and covdata merges them. That is the
# standard mechanism for exactly this problem, and this repo already built
# half of it — `_ensure-covdata` installs a version-matched covdata into
# GOTOOLDIR. Only the instrumentation was missing.
#
# GOCOVERDIR reaches the binary because testenv's isolatedEnv() starts from
# os.Environ() and scrubSessionEnv only strips CTXLOOM session keys. The dir
# must EXIST before the first exec or the runtime has nowhere to write.
#
# This is deliberately NOT the commit gate: an instrumented binary is slower
# and writes a file per exec, and the suite execs ctxloom thousands of times.
# Run it to get a number, not on every push.
test-acceptance-cover: build-cover _ensure-gotmpdir _ensure-covdata
    #!/usr/bin/env bash
    set -euo pipefail
    covdir="{{go_tmp}}/acceptance-cover"
    coverbin="$(pwd)/.coverbin"
    rm -rf "$covdir"; mkdir -p "$covdir"
    # -count=1 so nothing is served from the test cache: a cached PASS runs no
    # binary and would produce an empty, silently wrong coverage set.
    set +e
    # The instrumented binary is NAMED ctxloom and its directory leads PATH, so
    # the product resolves itself exactly as in a normal run. Point CTXLOOM_BINARY
    # at a differently-named twin instead and ctxloom writes its self-referencing
    # hooks as absolute paths rather than the bare name — different bytes, so a
    # different program measured. See cmd/ctxloom/justfile's build-cover.
    # -timeout 30m for the same reason test-acceptance carries it, only more so:
    # this lane runs the SAME 515 scenarios through a coverage-instrumented
    # binary, so it is strictly slower than the 1200s the plain suite measured.
    # Under go test's 600s default this lane died mid-suite and still emitted a
    # profile — a TRUNCATED one, which is worse than none: every leaf and flag
    # the run never reached reads as "not exercised", so a gate seeded from it
    # bakes in exemptions for code that is in fact covered.
    PATH="$coverbin:$PATH" GOTMPDIR="{{go_tmp}}" GOCOVERDIR="$covdir" \
        CTXLOOM_BINARY="$coverbin/ctxloom" \
        go test -timeout 30m -tags "acceptance integration" -count=1 ./tests/acceptance/...
    status=$?
    set -e
    files=$(find "$covdir" -name 'covcounters.*' | wc -l)
    if [ "$files" -eq 0 ]; then
        echo "error: the suite produced NO coverage counters — the instrumented" >&2
        echo "       binary never ran. A coverage report of nothing must not read" >&2
        echo "       as a clean result." >&2
        exit 1
    fi
    echo
    echo "=== coverage from $files instrumented runs ==="
    go tool covdata percent -i="$covdir"
    echo
    echo "(per-function: go tool covdata func -i=$covdir)"

    # The completeness gate, over data rather than over the suite's own account
    # of itself. It runs as a SECOND `go test` invocation because coverage is
    # complete only once every exec has flushed — an in-suite check would be
    # reading a half-written profile. It stays a Go test rather than a shell
    # comparison so it is discoverable where the other gates are.
    profile="$covdir/profile.txt"
    go tool covdata textfmt -i="$covdir" -o="$profile"
    echo
    echo "=== every CLI leaf's RunE ran, and every Changed() flag was passed? ==="
    CTXLOOM_COVERPROFILE="$profile" GOTMPDIR="{{go_tmp}}" \
        go test -v -tags "acceptance integration coveragegate" -count=1 \
        -run 'TestCLICoverage_' ./tests/acceptance/... || status=1
    exit "$status"

# Re-run the CLI coverage gates ALONE, against a profile that already exists.
#
# Both gates (`TestCLICoverage_*`: every leaf's RunE ran, every Changed() flag
# was passed) are pure functions of the profile `test-acceptance-cover` writes,
# but that recipe always does the whole lane first — instrumented build, full
# acceptance suite, covdata conversion, ~4 minutes — before the gates run. The
# commonest reason to touch one is editing coverageExemptLeaves or
# flagCoverageExempt, which changes no program behaviour at all, so paying for a
# full re-run to watch one assertion flip is pure waste. Running the underlying
# `go test` by hand is correctly refused by ltk, so without this there is no
# sanctioned fast path.
#
# It FAILS rather than skips when no profile exists: a gate that quietly passes
# over absent data is this project's characteristic bug, and the gate itself
# already fatals on an unset CTXLOOM_COVERPROFILE and on a profile that parses to
# zero blocks — this keeps the recipe honest to the same standard.
#
# Run `just test-acceptance-cover` first (or after any change to what the suite
# EXECUTES); use this only when you changed the gate, not the program.
test-coverage-gate PROFILE="":
    #!/usr/bin/env bash
    set -euo pipefail
    profile="{{ PROFILE }}"
    if [ -z "$profile" ]; then
        profile="{{ go_tmp }}/acceptance-cover/profile.txt"
    fi
    if [ ! -s "$profile" ]; then
        echo "error: no coverage profile at $profile" >&2
        echo "       This gate reads a profile; it cannot produce one. Run" >&2
        echo "       \`just test-acceptance-cover\` first, or pass a path." >&2
        exit 1
    fi
    echo "=== CLI coverage gates (profile: $profile) ==="
    CTXLOOM_COVERPROFILE="$profile" GOTMPDIR="{{ go_tmp }}" \
        go test -v -tags "acceptance integration coveragegate" -count=1 \
        -run 'TestCLICoverage_' ./tests/acceptance/...

# Run the @container acceptance rows — the ones that actually launch an engine
# inside a container (j002400_container.feature's differential host-vs-container
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
test-acceptance-container: build _ensure-gotmpdir
    ACCEPTANCE_PATHS=features/journeys/j002400_container.feature,features/j001400_bundle_distribution.feature,features/j002200_isolation.feature \
    ACCEPTANCE_TAGS="@container" \
    GOTMPDIR="{{go_tmp}}" \
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
# acceptance container as the invoking host user, on THIS machine. Builds the
# image first.
#
# CREDENTIALS ARE MAPPED, NEVER COPIED (tasks erased-collar / jovial-employee).
# Each engine's real credential DIRECTORY is bind-mounted READ-WRITE at the path
# the container HOME resolves it from, so the engine reads and WRITES the real
# file and a provider-side token rotation lands on the host. The copy this
# replaces was strictly one-way: codex refreshed inside the copy, the provider
# consumed the old refresh token SERVER-SIDE, the rotated value died with the
# container, and the host was left with a token that returned
# `401 refresh_token_reused` until a manual re-login.
#
# THE DIRECTORY IS MOUNTED, NEVER THE INDIVIDUAL FILE: credential files are
# written with an atomic temp-file-plus-rename, which a bind-mounted file does
# not survive — the rename would fail or land only inside the container,
# silently recreating the bug. That is why ~/.claude.json (a FILE at HOME level)
# is no longer carried at all: CLAUDE_CONFIG_DIR points at the mounted ~/.claude
# instead, and claude auto-creates its own .claude.json inside that directory.
#
# UID, and why this no longer runs as the image's `ctxloom` user: rootless
# docker/podman map that non-root user onto a subuid that cannot read the host's
# 0600 credential files — which is exactly why this recipe used to stage
# world-readable copies. Mounting the real directories instead means the
# container must run as the host uid that OWNS them. Under a rootless daemon
# that is container uid 0, which maps to the invoking user (so this is NOT root
# on the host). Under a rootful daemon it is the invoking uid itself — running
# as root there would leave root-owned files in your real credential
# directories.
#
# Set {ANTHROPIC,GEMINI,GOOGLE,OPENAI,CODEX}_API_KEY to use the unattended
# API-key path instead: that path mounts nothing, copies nothing, and rotates
# nothing. ctxloom is built at runtime from the read-only workspace mount; all
# other writes go to the container HOME / tmp. Each agent's @live rows self-skip
# without creds.
test-acceptance-live-container: container-build-acceptance
    #!/usr/bin/env bash
    set -euo pipefail
    staging="$(mktemp -d)"
    trap 'chmod -R u+w "$staging" 2>/dev/null; rm -rf "$staging"' EXIT

    # Credentials: MAP the real directories read-write. Nothing is copied, so
    # there is no rotated value to lose. An absent directory is named OUT LOUD
    # rather than silently skipped — an unmounted credential directory is how a
    # run comes back mysteriously unauthenticated.
    creds=()
    map_cred() { # $1 = host dir, $2 = container path, $3 = engine
        if [ -d "$1" ]; then
            creds+=(-v "$1:$2")
            echo "live-container: MAPPED $3 -> $1 (read-write: a token rotation lands on the host)"
        else
            echo "live-container: NOT MAPPED $3 — $1 does not exist; its @live rows will self-skip" >&2
        fi
    }
    map_cred "$HOME/.claude" /home/ctxloom/.claude claude
    map_cred "$HOME/.gemini" /home/ctxloom/.gemini antigravity
    map_cred "$HOME/.codex" /home/ctxloom/.codex codex
    # claude is the one engine CTXLOOM_LIVE_REQUIRE names below, so it is the
    # one whose absence must fail the run rather than quietly shrink coverage.
    if [ ! -d "$HOME/.claude" ] && [ -z "${ANTHROPIC_API_KEY:-}" ]; then
        echo "live-container: FATAL — CTXLOOM_LIVE_REQUIRE names claude, but neither $HOME/.claude nor ANTHROPIC_API_KEY is present" >&2
        exit 1
    fi

    # See the UID note above: pick the user whose host identity actually owns
    # the mounted credential directories.
    if {{container_cmd}} info 2>/dev/null | grep -qi 'rootless'; then
        run_as=(--user 0:0)
    else
        run_as=(--user "$(id -u):$(id -g)")
    fi

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

    mounts=(${creds[@]+"${creds[@]}"} -v "$staging/src:/workspace:ro")
    # Reuse the host module cache read-only so the runtime build doesn't re-download.
    if [ -d "$HOME/go/pkg/mod" ]; then mounts+=(-v "$HOME/go/pkg/mod:/home/ctxloom/go/pkg/mod:ro"); fi

    # Forward API keys when present so the unattended API-key path runs without
    # touching a subscription credential at all. Absent keys fall back to the
    # mapped cred dirs above.
    keys=()
    for k in ANTHROPIC_API_KEY GEMINI_API_KEY GOOGLE_API_KEY OPENAI_API_KEY CODEX_API_KEY; do
        if [ -n "${!k:-}" ]; then keys+=(-e "$k"); fi
    done

    {{container_cmd}} run --rm \
        "${run_as[@]}" \
        "${mounts[@]}" \
        ${keys[@]+"${keys[@]}"} \
        -e HOME=/home/ctxloom \
        -e CLAUDE_CONFIG_DIR=/home/ctxloom/.claude \
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
# one of claude-code|codex|kiro|opencode|antigravity; AXIS is worktree,
# container-rootless, or container-rootful (or "bypass" for the engine's
# env-API-key-forced worktree row, or "kiro-leak" for the dedicated
# --degraded credential-store-leak proof — that one ignores ENGINE/AXIS).
# container-rootful is wired but has never gone green on any box this suite
# has run on (no reachable rootful daemon) — it self-skips loudly. Makes AT
# MOST one real, paid engine call.
# Requires real credentials for ENGINE (a host credential file, or its
# API-key env var) — self-skips loudly, naming exactly what is missing, when
# absent. See website/src/content/docs/security/isolation.md's "The
# executable probe" section.
isolation-probe ENGINE AXIS: build
    ACCEPTANCE_PATHS=features/isolation_probe.feature \
    ACCEPTANCE_TAGS="@live && @{{ENGINE}} && @{{AXIS}}" \
    CTXLOOM_ACCEPTANCE_LIVE=1 \
    go test -v -tags "acceptance integration" -count=1 ./tests/acceptance/...

# Run the LIVE delegation round trip (j002300_cross_engine_delegation.feature's
# per-engine floor) for exactly ONE engine. ENGINE is one of
# claude-code|codex|kiro|opencode. It spawns a real delegated child on that
# engine and asserts the marker phrase that exists ONLY in the child's own
# composed context comes back to the coordinator's mailbox over the
# agent_send/agent_recv bus — the round trip agent_run's own success value
# famously does not prove (see that feature's header). Makes real, paid engine
# calls; self-skips loudly, naming the engine and the reason, when that engine
# is missing or unauthenticated. -timeout 20m because a live turn on a slow
# engine can outlast `go test`'s 10m default, and a killed run reads as a
# harness fault rather than the engine being slow.
live-delegation ENGINE: build _ensure-gotmpdir
    GOTMPDIR="{{go_tmp}}" \
    ACCEPTANCE_PATHS=features/j002300_cross_engine_delegation.feature \
    ACCEPTANCE_TAGS="@live && @delegation && @{{ENGINE}}" \
    CTXLOOM_ACCEPTANCE_LIVE=1 \
    go test -v -timeout 20m -tags "acceptance integration" -count=1 ./tests/acceptance/...

# Run ONE cell of the engine x isolation floor
# (features/engine_isolation_matrix.feature): the simplest live round trip —
# "emit exactly this JSON object, nothing else" — for one engine under one
# isolation scheme. ENGINE is claude-code|codex|kiro|opencode, RUNTIME is
# host|container-rootless|container-rootful, WORKSPACE is none|worktree.
# container-rootless and container-rootful are ownership modes of ONE
# containerization axis, not a fourth engine — a host has at most one of them
# reachable at a time (containercell.probeDocker), so which one this runner
# can actually pass is an ENVIRONMENT fact, not a choice.
#
# ONE CELL AT A TIME, deliberately, and there is no all-cells recipe on
# purpose: these are real, paid engine calls, a container cell can run for
# minutes (it may build an agent image first), and fanning the matrix out on a
# loaded box is how a machine gets OOM-killed in the middle of a measurement.
# Self-skips loudly, naming the engine and BOTH axes, when the engine is
# absent/unauthenticated, when that axis cannot authenticate it, or when no
# container runtime is reachable. -timeout 30m so a slow container cell fails
# by saying so rather than by being killed.
engine-matrix ENGINE RUNTIME WORKSPACE: build _ensure-gotmpdir
    GOTMPDIR="{{go_tmp}}" \
    ACCEPTANCE_PATHS=features/engine_isolation_matrix.feature \
    ACCEPTANCE_TAGS="@live && @{{ENGINE}} && @{{RUNTIME}} && @ws-{{WORKSPACE}}" \
    CTXLOOM_ACCEPTANCE_LIVE=1 \
    go test -v -timeout 30m -tags "acceptance integration" -count=1 ./tests/acceptance/...

# Run ONE cell of the capability-probe ladder (tests/acceptance's probe
# registry): PROBE is a registry probe name without the @probe- prefix
# ("p3-hook-firing"), FEATURE is that probe's own feature file, ENGINE is
# claude-code|codex|kiro|opencode, RUNTIME is host|container, WORKSPACE is
# none|worktree. The five tags it composes are exactly the tag line every
# probe's Examples block carries (probeCell.Tags), so this recipe and the
# registry cannot drift about how a cell is addressed.
#
# WHY FEATURE IS A PARAMETER rather than looked up from the registry: this is
# a justfile, and reaching into a Go table from one would mean building and
# running something first — which is the thing the recipe exists to start. The
# registry's Feature field is the source of truth and the completeness test
# already refuses a probe naming a feature that does not exist, so a wrong
# value here fails loudly on the very next hermetic run.
# ONE CELL AT A TIME, for the same reasons engine-matrix above says so and
# there is deliberately no all-cells recipe here either: every cell is a real
# paid engine turn on somebody's subscription, and fanning them out on a loaded
# box is how a machine gets OOM-killed mid-measurement (the design's own §5.5
# counter). The sweep runner that sequences cells, pre-flights the process
# table and renders a report is slice S10's job, not this recipe's.
capability-probe PROBE FEATURE ENGINE RUNTIME WORKSPACE: build _ensure-gotmpdir
    GOTMPDIR="{{go_tmp}}" \
    ACCEPTANCE_PATHS=features/{{FEATURE}} \
    ACCEPTANCE_TAGS="@live && @probe-{{PROBE}} && @{{ENGINE}} && @{{RUNTIME}} && @ws-{{WORKSPACE}}" \
    CTXLOOM_ACCEPTANCE_LIVE=1 \
    go test -v -timeout 30m -tags "acceptance integration" -count=1 ./tests/acceptance/...

# Run ONE cell of the plan-sentinel probe (P4 of the capability ladder,
# features/capability_plan_sentinel.feature): does `permissions: plan` actually
# stop a write. POSTURE is control|plan, or "pair" to run BOTH — and pair is
# what you almost always want.
# WHY PAIR IS THE DEFAULT ANSWER. The plan cell's claim is negative: a file that
# did not change. On its own that is equally consistent with a posture that
# refused the write and with a run that never attempted one. The bypass control
# is the discriminator — same engine, same fixture, one line of config.yaml
# different — and the plan verdict consults its outcome, reddening when the
# control is dead and printing a PROVISIONAL note when the control did not run
# in the same process. So a lone `plan` invocation gives you a caveat, not a
# result.
# Two real, paid turns for a pair. Host axis only at this rung. Self-skips
# loudly, naming the engine and the posture, when that engine is missing or
# unauthenticated. The sweep runner, the version-stamp trigger and the generic
# `capability-probe`/`capability-sweep` recipes are a later slice's job (S10 of
# the capability-probe design); this is the single-cell unit those will call.
plan-sentinel ENGINE POSTURE="pair": build _ensure-gotmpdir
    ACCEPTANCE_PATHS=features/capability_plan_sentinel.feature \
    ACCEPTANCE_TAGS="@live && @probe-p4-plan-sentinel && @{{ENGINE}} && @host && @ws-none{{ if POSTURE == 'pair' { '' } else { ' && @var-' + POSTURE } }}" \
    CTXLOOM_ACCEPTANCE_LIVE=1 \
    go test -v -timeout 30m -tags "acceptance integration" -count=1 ./tests/acceptance/...

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
test-pkg PKG *ARGS: _require-generated _ensure-gotmpdir
    #!/usr/bin/env bash
    set -euo pipefail
    export GOTMPDIR="{{go_tmp}}"
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
    trap 'rm -rf "{{mutation_tmp}}"/gremlins-* "{{mutation_tmp}}"/.cache-sweep.*' EXIT
    marker="{{mutation_tmp}}/.cache-sweep.$$"
    : > "$marker"
    pkg="$1"; shift
    TMPDIR="{{mutation_tmp}}" gremlins unleash "./$pkg" "$@"
    # `set -e` above means this line is reached only on a green run.
    just _sweep-cache "$marker"

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
    trap 'rm -rf "{{mutation_tmp}}"/gremlins-* "{{mutation_tmp}}"/.cache-sweep.*' EXIT
    marker="{{mutation_tmp}}/.cache-sweep.$$"
    : > "$marker"
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
    # Past every "this measured nothing" guard, so the run is genuinely green.
    just _sweep-cache "$marker"

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

# Mutate one source file per target and drive the CUCUMBER acceptance suite
# against a binary rebuilt from each mutant (github.com/gtramontina/ooze), not
# `go test` against source like test-mutation-acceptance above. That is the
# whole point: test-mutation-acceptance mutates source and runs `go test`,
# but the acceptance suite execs a PRE-BUILT ctxloom binary
# (tests/integration/testenv/environment.go) — a gremlins mutant never
# reaches that already-compiled process (measured: 92 mutants on
# internal/operations/trust.go, 0 runnable, 92 NOT COVERED). ooze's laboratory
# instead symlinks the repo into a tmpdir, overwrites ONLY the mutated file
# with real bytes at that path (never the source tree), and runs
# tests/mutation/run_scoped_suite.sh with that tmpdir as cwd — which
# rebuilds ctxloom FROM the mutant and then runs the cucumber suite scoped
# (ACCEPTANCE_PATHS) to just the features that CLAIM to cover that file. A
# survivor is a mechanism one of those features claims to cover but does not
# actually verify.
#
# Which file is paired with which features is the mutationTargets table in
# tests/mutation/trust_cascade_mutation_test.go — a data change, not a code
# change. Each entry runs as its own subtest of TestAcceptanceMutation, so a
# single one can be run alone:
#
#   just test-mutation-cucumber -run 'TestAcceptanceMutation/^trust_cascade$'
#
# Cost: every mutant is a full build + a ~15-20s scoped suite run (measured
# ~28s/mutant on this machine); trust_cascade alone is 132 mutants ≈ 63
# minutes, and the whole table is a multi-hour job that WILL exceed the 120m
# -timeout below. Nightly/scoped, never a per-PR gate — and run entries one
# at a time unless you mean to spend the night on it.
#
# Scope is enforced inside the test file, built programmatically by walking
# the repo and ignoring every other .go file — see that file's doc comment for
# why (RE2 has no lookahead; ooze's own file discovery is as scope-blind as
# gremlins'). The scoping and the table are pinned by cheap unit tests that
# need no mutation run:
#
#   just test-pkg ./tests/mutation/... -tags mutation -run TestMutationTargets
#
# THE RESULT IS RATCHETED, per target, against tests/mutation/survivor_baseline.txt:
# a target that measures MORE survivors than its recorded count fails this
# recipe. The harness itself cannot do that — it releases with
# WithMinimumThreshold(0) so a threshold is never chosen before a measurement
# exists — so without the ratchet the only thing that can red this gate is a
# run producing no score at all. Re-record after coverage work with
# CTXLOOM_MUTATION_BASELINE=update; the baseline file states what each
# provenance word licenses.
test-mutation-cucumber *ARGS:
    #!/usr/bin/env bash
    set -euo pipefail
    mkdir -p "{{mutation_tmp}}"
    marker="{{mutation_tmp}}/.cache-sweep.$$"
    : > "$marker"
    trap 'rm -f "$marker" "{{mutation_tmp}}/.run.$$.log"' EXIT
    set +e
    # -v is LOAD-BEARING, not a debugging convenience. ooze prints its per-mutant
    # diffs and its summary box to STDOUT, and `go test` swallows a PASSING test's
    # stdout. The mutation test uses WithMinimumThreshold(0), so it ALWAYS passes —
    # which means without -v this recipe runs for an hour and emits one line:
    #   ok  github.com/ctxloom/ctxloom/tests/mutation  339.740s
    # Measured 2026-08-07: a full bundle_sign entry did exactly that. The first
    # trust_cascade run only showed its 51 survivors because the guard test was
    # failing beside it and dragged the package output out; fixing that guard
    # silenced the gate entirely. See taskloom unwanted-deviate.
    #
    # 240m, not 120m: the four-entry table measured 111 minutes of mutants
    # (63 + 30 + 12 + 6) plus the guard test, so 120m was already marginal and a
    # timeout mid-table loses the whole run's results.
    output=$(go test -tags mutation -v -count=1 -timeout 240m ./tests/mutation/... "$@" 2>&1)
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
    # THE INVARIANT: a mutation run that produced no score has told you nothing,
    # and must never read as a clean bill of health. Exit 0 here would be the
    # exact failure this gate replaced (gremlins reporting success over an empty
    # mutant set).
    if ! grep -q 'Score:' <<<"$output"; then
        echo "error: the run produced no mutation score — it measured NOTHING." >&2
        echo "       ooze prints its summary to stdout; if that is missing, either no" >&2
        echo "       target was released or the output was swallowed. Do not read this" >&2
        echo "       as a pass." >&2
        exit 1
    fi
    # Repeat the summary AFTER the -v firehose, so the number is not buried
    # thousands of scenario lines up.
    echo
    echo "=== mutation summary ==="
    grep -E 'Total:|Killed:|Survived:|Score:' <<<"$output" || true
    # THE SECOND HALF OF THE SAME INVARIANT: the guard above refuses a run that
    # measured nothing; this one refuses a run that measured something WORSE
    # than what is already recorded. A score alone cannot fail this gate, so
    # regression is what fails it — per target, because one number for the whole
    # table lets an improvement in one entry mask a regression in another.
    runlog="{{mutation_tmp}}/.run.$$.log"
    printf '%s\n' "$output" > "$runlog"
    set +e
    bash tests/mutation/survivor_ratchet.sh tests/mutation/survivor_baseline.txt "$runlog"
    ratchet=$?
    set -e
    if [ "$ratchet" -ne 0 ]; then
        exit "$ratchet"
    fi
    # Past both guards, so a real score was produced and it did not regress:
    # sweep what the per-mutant recompiles just added.
    just _sweep-cache "$marker"

# Run ONE entry from the mutation target table (see `just test-mutation-entries`).
# Per-entry is the recommended way to run this: the full table is ~111 minutes,
# and a single entry gives a number you can act on today.
#
#   just test-mutation-entry signer_store
test-mutation-entry NAME *ARGS:
    @just test-mutation-cucumber -run 'TestAcceptanceMutation/^{{NAME}}$' {{ARGS}}

# List the mutation target table's entry names, with the file each one mutates.
# Reads the table itself, so it cannot drift from the code the way a hand-kept
# list in a comment would.
test-mutation-entries:
    #!/usr/bin/env bash
    set -euo pipefail
    echo "mutation target table (tests/mutation/, run with: just test-mutation-entry NAME):"
    grep -A2 -E '^\s+Name:\s+"' tests/mutation/*_test.go \
      | grep -oE '"(([a-z_]+)|(internal/[^"]+\.go))"' \
      | tr -d '"' \
      | paste - - \
      | awk '{ printf "  %-16s %s\n", $1, $2 }'

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

# Evict the build-cache entries a GREEN gate just created, given a MARKER file
# stamped immediately before the run.
#
# ONLY the acceptance and mutation gates call this. They are what produce the
# tens of GB: acceptance compiles the whole tree under `-tags "acceptance
# integration"`, and the mutation lanes recompile it once per mutant. The fast
# unit lanes (test-default, test-pkg) deliberately keep their cache warm —
# sweeping those would just make iteration slower for no meaningful disk win.
#
# ONLY ON SUCCESS. A red run leaves every entry in place so the next attempt
# recompiles nothing and the failure can be re-run immediately; the disk cost
# of a failing gate is bounded by how long it stays failing.
#
# Attribution is by mtime window, the same age mechanism cache-report and Go's
# own trim use, because a cache entry carries no record of which run produced
# it. GOCACHE is shared (host, gopls, every worktree, every `just _run`
# container), so this can also evict entries a CONCURRENT run in another tree
# created. That is a cache miss for that run and nothing worse — a missing
# entry is never a wrong build.
#
# This only COMPENSATES for the duplication. -trimpath on the test path is the
# real fix and is already decided; it is blocked on 44 runtime.Caller(0) sites
# across 25 test files that derive a path from their own source location, which
# a trimmed path rewrites to a module path that does not exist on disk. Four
# tests and `just cover` fail immediately without that prerequisite. Do not read
# this sweep as closing that out — see taskloom obtuse-equinox.
_sweep-cache MARKER:
    #!/usr/bin/env bash
    set -uo pipefail
    gbc="$(go env GOCACHE 2>/dev/null || echo "$HOME/.cache/go-build")"
    { [ -d "$gbc" ] && [ -f "{{MARKER}}" ]; } || exit 0
    size_kb() { du -sk "$1" 2>/dev/null | awk '{print $1}'; }
    before="$(size_kb "$gbc")"
    find "$gbc" -type f \( -name '*-a' -o -name '*-d' \) -newer "{{MARKER}}" -delete 2>/dev/null
    after="$(size_kb "$gbc")"
    rm -f "{{MARKER}}"
    awk -v b="${before:-0}" -v a="${after:-0}" \
        'BEGIN{printf "swept %.1f GB of gate build cache (run was green)\n", (b-a)/1048576}'

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

# Build the architectural linter into bin/archlint (delegates to devcontainer).
build-archlint: dev-image
    just _run build-archlint

# Run ctxloom's architectural rules (delegates to devcontainer).
#
# A sibling of `lint` rather than part of it, so an architectural failure is
# attributable and CI's golangci-lint baseline is untouched. Wired into
# lefthook's pre-commit hook against the prebuilt bin/archlint.
lint-arch: dev-image
    just _run lint-arch

# Whole-program dead-code sweep.
#
# golangci-lint's `unused` is PACKAGE-scoped: it cannot see that an exported
# symbol has no caller anywhere in the module, which is where this codebase's
# dead code hides. deadcode does whole-program reachability instead.
#
# Pinned by the `tool` directive in go.mod, so it needs neither the dev image
# nor a host install, and every clone runs the same version.
#
# THE TAGS ARE NOT OPTIONAL. deadcode analyses ONE build configuration, and a
# symbol reached only from tag-gated code looks dead without them. MEASURED on
# this tree: 128 findings untagged against 16 with the tags below — 87% of the
# untagged report was live code, including ConfigHomeEnvKeys, ComposableEngines
# and VendorReaderEngineNames. Keep this list in step with the tags in
# .golangci.yml and the tagged test recipes.
#
# -test counts tests as entry points. Drop it (`just deadcode ""`) to find code
# that only the suite reaches — a different and also useful question.
#
# Not a gate: it reports, it does not fail. Symbols reached through a
# string-keyed registry (content.Register) are invisible to it, so a report is
# evidence to check, never a delete list.

# Whole-program dead-code sweep (pass "" to drop -test and find test-only code)
deadcode *ARGS="-test":
    go tool deadcode -tags treesitter,acceptance,integration,arch,mutation,conformance,docker_integration {{ARGS}} ./...

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
    go build -ldflags "-X github.com/ctxloom/ctxloom/internal/version.Version={{version}}" -o ctxloom ./cmd/ctxloom
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
        -ldflags "-X github.com/ctxloom/ctxloom/internal/version.Version={{version}}" \
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
        -ldflags "-X github.com/ctxloom/ctxloom/internal/version.Version={{version}}" \
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
test-acceptance-nobuild: _ensure-gotmpdir
    GOTMPDIR="{{go_tmp}}" go test -v -tags "acceptance integration" -count=1 ./tests/acceptance/...
