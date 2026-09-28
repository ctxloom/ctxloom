# Gates shared by BOTH root justfiles.
#
# Pulled in with just's `import` (NOT `mod`), the same mechanism
# build/common.justfile uses for the per-binary build logic: the recipes and
# variables below land directly in the importing file's namespace, so
# `justfile` and `justfile.container` get ONE definition instead of two
# copies that drift.
#
# They drifted. Before this file, `test-docker-integration` existed twice
# under the same NAME with different package lists — the host copy ran
# ./internal/adapters/isolation/..., ./internal/core/coord/... and
#; the container copy (the one
# .github/workflows/ci.yml actually invokes) ran only
# ./internal/adapters/isolation/.... Every docker-gated test under
# internal/core/coord — TestCoordContainerDirect_NoPluginNoPort, the
# whole TestCoordOwnerRun_* suite, the TestCoordContainerProgress_* trio —
# had therefore NEVER executed in CI. Nobody noticed because the two recipes
# shared a name, and a name is what you grep for.
#
# A shared definition is the fix that cannot regress; keeping two aligned
# lists would only reset the clock.

# ===== Docker-gated integration tests =====

# THE package list for `-tags docker_integration`. One definition, both
# justfiles. _check-docker-integration-pkgs below proves it still covers
# every docker_integration-tagged file in the tree, so a NEW package growing
# such a test cannot silently fall outside the gate either.
#
# Cost (measured 2026-07-24, rootless docker, warm image cache): the
# internal/core/coord suite is ~115s of which the
# TestCoordContainerProgress_* trio is ~75s. That is per-commit money, not
# nightly money: these guard a defect class that ships SILENTLY (a container
# child that never receives its prompt looks identical to a healthy one from
# every cheap signal), and a red nightly on a branch nobody is standing on is
# noise, not a gate.
docker_integration_pkgs := "./internal/adapters/attach/... ./internal/adapters/isolation/... ./internal/core/coord/... ./internal/core/spool/... ./internal/engines/mock/... ./internal/testsupport/containercell/..."

# Run the docker-gated container integration tests: they build minimal images,
# spawn real containers, and prove the transport / coordinator bus / progress
# machinery end to end against a live daemon rather than an in-process
# simulation.
#
# Reachability is a HARD requirement under CTXLOOM_REQUIRE_DOCKER=1 (CI sets
# it): without it every test here self-skips when the socket is unreachable,
# so a runner that loses docker passes this step having executed nothing —
# the same silent-no-op failure family the suite exists to catch, sitting
# inside the gate. See internal/testsupport/dockergate.
# -count=1 (no result caching) is load-bearing, not hygiene. Both gate knobs —
# CTXLOOM_REQUIRE_DOCKER and CTXLOOM_REQUIRE_RUNTIMES — are read at PACKAGE
# INIT, which is deliberate (testsupport.Isolate clears them, so a test that
# isolates before it gates must not silently demote itself back to skipping).
# But go's test-result cache only records env reads made through its testlog
# hook, which is installed by testing.M.Run — AFTER package init. An init-time
# read is therefore invisible to the cache key, so `CTXLOOM_REQUIRE_RUNTIMES=podman
# just test-docker-integration` would happily replay a green result from a run
# that declared nothing. Measured, not theorised: the podman lane reported
# "(cached) ok" until this was added. These tests are environment-dependent by
# definition; caching their verdict is wrong regardless of which variable
# changed.
test-docker-integration: _require-generated _check-docker-integration-pkgs _check-docker-skip-gate
    go test -trimpath -v -count=1 -tags docker_integration {{docker_integration_pkgs}}

# Drift gate for docker_integration_pkgs: every file carrying the
# `//go:build docker_integration` constraint must live under a package the
# list selects. Catches the "new package, new docker test, never run" hole
# that the two-copies-of-one-recipe split created in the first place.
_check-docker-integration-pkgs:
    #!/usr/bin/env bash
    set -euo pipefail
    patterns=({{docker_integration_pkgs}})
    missing=()
    while IFS= read -r f; do
        [ -n "$f" ] || continue
        dir="./$(dirname "$f")"
        covered=0
        for p in "${patterns[@]}"; do
            base="${p%/...}"
            case "$dir" in
                "$base"|"$base"/*) covered=1; break ;;
            esac
        done
        [ "$covered" -eq 1 ] || missing+=("$f")
    done < <(grep -rl '^//go:build docker_integration' --include='*.go' . 2>/dev/null | sed 's|^\./||' | sort)
    if [ "${#missing[@]}" -ne 0 ]; then
        echo "error: docker_integration-tagged files outside docker_integration_pkgs — these tests would NEVER run:" >&2
        printf '  %s\n' "${missing[@]}" >&2
        echo "fix: add the package to docker_integration_pkgs in build/gates.justfile" >&2
        exit 1
    fi

# Enforce that docker-gated tests route their skips through
# internal/testsupport/dockergate instead of calling t.Skip directly. A bare
# t.Skip is invisible reachability policy: it turns a runner with no docker
# into a green run of zero tests, and CTXLOOM_REQUIRE_DOCKER cannot see it.
_check-docker-skip-gate:
    #!/usr/bin/env bash
    set -euo pipefail
    offenders=""
    while IFS= read -r f; do
        [ -n "$f" ] || continue
        hits="$(grep -nE '\bt\.Skip(f|Now)?\(' "$f" || true)"
        if [ -n "$hits" ]; then
            offenders+="$f"$'\n'"$(sed 's/^/    /' <<<"$hits")"$'\n'
        fi
    done < <(grep -rl '^//go:build docker_integration' --include='*.go' . 2>/dev/null | sed 's|^\./||' | sort)
    if [ -n "$offenders" ]; then
        echo "error: bare t.Skip in a docker-gated test — route it through internal/testsupport/dockergate:" >&2
        printf '%s' "$offenders" >&2
        echo "  dockergate.RequireRuntime(t, available, what)  — reachability; fails under CTXLOOM_REQUIRE_DOCKER=1" >&2
        echo "  dockergate.SkipCapability(t, reason)           — environment capability CI legitimately lacks" >&2
        exit 1
    fi

# ===== Architectural invariants =====

# Run the architectural-invariant gates as a discrete, attributable set.
#
# These are the CLASS gates — the tests that fail when a new instance of a
# known-bad class appears (a proto field no converter mirrors, an enum value no
# table covers, a config key Save() drops, a package importing test-only
# machinery). They are named `TestArch_<Subject>_<Property>` for exactly one
# reason: so this recipe can select them. They already all ran; what was
# missing was ATTRIBUTION — a violated invariant surfaced as one red test
# inside a 217-package run, indistinguishable from an ordinary break.
#
# BUILD-TAGGED `//go:build arch`, following this repo's own precedent
# (tests/integration is `-tags integration`, tests/acceptance is
# `-tags "acceptance integration"`). The tag makes this recipe the only thing
# that COMPILES them, which is what makes the group discrete: `test-default`
# no longer runs them at all, so a red step here is unambiguously an
# architectural violation and nothing else.
#
# The tag does NOT make them opt-in. `just test` — the recipe humans and agents
# actually type — runs `test-default` THEN `test-arch` and fails if either
# fails. Opt-in gates are how this repo acquired gates that do not gate
# (test-conformance is red and referenced by no workflow); the aggregate `test`
# target is what keeps that from happening here, so it is the thing that must
# stay honest.
#
# ANTI-VACUOUS GUARD — and the tag makes it matter MORE, not less. A `-run`
# regex that matches nothing exits 0 from `go test`; so now does a MISSPELLED
# TAG, which yields zero selected tests and zero compile errors, because the
# tagged files simply drop out of the build. Both failure modes are invisible
# without a count. test-pkg's guard (grep for `[no tests to run]`) cannot be
# reused verbatim: selecting a prefix across ./... means almost EVERY package
# prints that message legitimately, so it would fire on a healthy run. The
# module-wide equivalent is to COUNT what ran and refuse to pass on zero —
# `go test -v` prints one `--- PASS/FAIL/SKIP:` line per top-level test at
# column 0 — and to report the count either way.
#
# No -race: `test-default` already runs the whole untagged suite under -race,
# and these are reflection/AST/source-walk assertions with no concurrency of
# their own.
test-arch: _require-generated
    #!/usr/bin/env bash
    set -euo pipefail
    set +e
    output=$(go test -trimpath -count=1 -tags arch -run 'TestArch_' -v ./... 2>&1)
    status=$?
    set -e
    # Gates and package results only, not the "no tests to run" noise from the
    # 200-odd packages that hold none.
    grep -E '^(--- |    --- |FAIL|ok .*[0-9]s)' <<<"$output" | grep -vE '^ok .*\[no tests to run\]' || true
    ran=$(grep -cE '^--- (PASS|FAIL|SKIP): TestArch_' <<<"$output" || true)
    if [ "$status" -ne 0 ]; then
        echo "" >&2
        echo "ARCHITECTURAL INVARIANT VIOLATED — $ran arch gate(s) ran, at least one failed." >&2
        echo "This is not an ordinary test break: a class of bug the codebase has already" >&2
        echo "paid for has reappeared. Read the failure above; it names the instance." >&2
        printf '%s\n' "$output" >&2
        exit "$status"
    fi
    if [ "$ran" -eq 0 ]; then
        echo "error: -tags arch -run 'TestArch_' selected NO tests — the gate ran nothing and" >&2
        echo "would have exited 0 saying so. Either the naming convention was broken by a" >&2
        echo "rename, or this recipe's -run pattern is wrong, or the build tag is wrong and" >&2
        echo "every //go:build arch file dropped silently out of the build. All three are the" >&2
        echo "gate failing." >&2
        exit 1
    fi
    echo ""
    echo "architectural invariants: $ran gate(s) passed"

# ===== Generated code preconditions =====

# _require-generated makes generated protobuf present and current by simply
# running the generator. buf is idempotent and cheap when nothing changed, so
# there is nothing to decide here — asking "is it missing? is it stale?" only
# adds a heuristic that can be wrong, and being wrong means compiling against a
# stale ABI.
#
# *.pb.go is gitignored, so a fresh clone or `git worktree add` never has it and
# every package above a leaf then fails to build with a misleading "no required
# module provides package github.com/ctxloom/ctxloom/...". That used to be a
# manual step on every new worktree, and worktree-per-unit-of-work is how this
# repo says to work, so the friction sat on the common path.
#
# What this replaced was worse than a manual step: an enumeration of
# `git ls-files '*.proto'`, a hard-coded skip of internal/adapters/coordgrpc/pb/google/*,
# and a derivation of "<stem>.pb.go plus <stem>_grpc.pb.go when the file
# declares a service" — three separate re-implementations of what buf actually
# emits, maintained by nobody and caught by nothing when buf.gen.yaml changes.
# It also tested existence ONLY, so a .pb.go older than its .proto passed.
_require-generated: proto

# _mutation-prereqs is the one precondition every mutation lane takes. Each lane
# compiles the tree (ooze in a symlinked laboratory, gremlins in its own copy),
# and a tree that cannot build makes every mutant fail to compile — scored as a
# kill, so the run measures nothing. Generated code is gitignored, so a fresh
# worktree cannot build until it is generated. Generating and compiling here, as
# just dependencies, stops a broken tree before any mutant is scored.
_mutation-prereqs: _require-generated build

# ===== Coverage / test-isolation gates =====

# Fail (and clean up) if any test wrote a nested internal/**/.ctxloom into the
# source tree instead of isolating through t.TempDir(). internal/adapters/operations'
# TestMain catches this for itself; other packages had no such guard, so a
# regression there was caught by nothing but a .gitignore rule for
# internal/**/.ctxloom — which hides the symptom (git status stays clean) but
# the directory still physically exists, which is what confuses worktree-safe
# WIP detection and blocks worktree reaping. This runs after every `just
# test`, so the leak is a build failure instead of invisible disk residue.
#
# Imported, not duplicated. It used to exist twice, once in each root
# justfile, and a coverage/leak check that only runs on one side is exactly
# the shape of bug this file exists to remove.
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

# Filter coverage output using patterns from .coverignore.
# Usage: _filter_coverage <input> <output>
#
# Imported, not duplicated. It decides what counts toward coverage, so a
# host copy and a container copy that drift would make local and CI coverage
# numbers disagree SILENTLY — no gate fails, the numbers simply stop meaning
# the same thing. One definition means that disagreement cannot arise.
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

# ===== Pin direction =====

# Refuse a staged pin that goes BACKWARDS from HEAD. The buildpins/enginepins
# tests prove the pin files agree with their consumers; only this gate can see
# the direction of an edit. Pure shell over `git show`, so it costs ~40ms and
# runs on every commit (lefthook.yml). A deliberate downgrade names its pin in
# CTXLOOM_ALLOW_PIN_DOWNGRADE, which the gate echoes for the commit's reviewer.
lint-pins:
    ./scripts/lint-pins

# ===== Suites CI runs through justfile.container =====
#
# Shared for the reason at the top of this file: ci.yml calls these by bare
# name under JUST_JUSTFILE=justfile.container, and a recipe that exists only in
# the host justfile fails that step with "Justfile does not contain recipe".
# Nested calls name {{justfile()}} (the ROOT justfile) because just does not
# pass --justfile to a child `just`, so a bare one would resolve the host file.
# The root is justfile_directory(), not git: in a worktree mounted into the
# devcontainer, git cannot resolve the toplevel (see build/common.justfile).

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

# Run the cross-agent equity conformance suite (every registered backend through
# the shared agent.SettingsWriter contract). Tag-gated so it's excluded from the
# default `go test ./...`; run it explicitly here.
#
# Then the installed claude's secure-storage probe: a host run shares the
# human's login only while claude still takes its credential and both refresh
# locks from CLAUDE_SECURESTORAGE_CONFIG_DIR. Through test-pkg, so a -run that
# selects nothing fails instead of passing.
test-conformance:
    go test -trimpath -race -tags conformance ./internal/engines/conformance/...
    just -f {{justfile()}} test-pkg ./internal/engines/claude/ -tags conformance -run '^TestClaudeSecureStorage_'

# Run the acceptance suite against a COVERAGE-INSTRUMENTED ctxloom and report
# what it actually executed.
#
# WHY THIS EXISTS, and what it is NOT for. completeness_test.go answers
# "was this leaf REACHED?" from testenv.RecordedInvocations() — the argv the
# suite actually started, resolved to a leaf by cobra's own root.Find(). That
# gate is correct and stays: it keeps flag-level credit (`--engine <name>`
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
    coverbin="{{justfile_directory()}}/.coverbin"
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
        go test -trimpath -timeout 30m -tags "acceptance integration" -count=1 ./tests/acceptance/...
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
        go test -trimpath -v -tags "acceptance integration coveragegate" -count=1 \
        -run 'TestCLICoverage_' ./tests/acceptance/... || status=1
    exit "$status"
