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
# ./internal/lm/isolation/..., ./internal/agentcoord/coord/... and
# ./internal/vpio/dockerexec/...; the container copy (the one
# .github/workflows/ci.yml actually invokes) ran only
# ./internal/lm/isolation/.... Every docker-gated test under
# internal/agentcoord/coord — TestCoordContainerDirect_NoPluginNoPort, the
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
# internal/agentcoord/coord suite is ~115s of which the
# TestCoordContainerProgress_* trio is ~75s. That is per-commit money, not
# nightly money: these guard a defect class that ships SILENTLY (a container
# child that never receives its prompt looks identical to a healthy one from
# every cheap signal), and a red nightly on a branch nobody is standing on is
# noise, not a gate.
docker_integration_pkgs := "./internal/lm/isolation/... ./internal/agentcoord/coord/... ./internal/vpio/dockerexec/... ./internal/acp/... ./internal/mockengine/... ./internal/testsupport/containercell/..."

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
test-docker-integration: _require-generated _check-docker-integration-pkgs _check-docker-skip-gate
    go test -v -tags docker_integration {{docker_integration_pkgs}}

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
    output=$(go test -count=1 -tags arch -run 'TestArch_' -v ./... 2>&1)
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

# Fail LOUDLY when a checkout has no generated protobuf.
#
# .gitignore excludes *.pb.go (only the .proto sources are tracked — see task
# ratty-amuck: tracking the artifacts was tried and reverted, generating them
# is the standing answer), so a freshly created git worktree has none of the
# seven generated files. Every package above a leaf then fails to COMPILE
# with go's module-resolution message:
#
#   internal/agentcoord/coord/approval.go:13:2: no required module provides
#   package github.com/ctxloom/ctxloom/internal/agentcoord; to add it:
#           go get github.com/ctxloom/ctxloom/internal/agentcoord
#
# which names neither protobuf nor the worktree, and whose suggested fix
# (`go get` an internal package of this very module) is actively wrong. Every
# delegated agent in this project has paid that tax. The recipes that invoke
# `go` on the whole tree WITHOUT first going through the container `build`
# (which runs `proto` itself) depend on this, so the first thing such a
# worktree prints is the actual problem.
#
# The expected file set is DERIVED, not listed: every tracked .proto outside
# the vendored internal/agentcoord/google (excluded from codegen by
# buf.gen.yaml) must have a sibling <name>.pb.go, plus <name>_grpc.pb.go when
# it declares a service. Adding a proto extends the check for free.
_require-generated:
    #!/usr/bin/env bash
    set -euo pipefail
    missing=()
    while IFS= read -r proto; do
        case "$proto" in ./internal/agentcoord/google/*) continue ;; esac
        stem="${proto%.proto}"
        [ -f "$stem.pb.go" ] || missing+=("$stem.pb.go")
        if grep -q '^service ' "$proto"; then
            [ -f "${stem}_grpc.pb.go" ] || missing+=("${stem}_grpc.pb.go")
        fi
    done < <(find . -name '*.proto' -not -path './.git/*' | sort)
    if [ "${#missing[@]}" -ne 0 ]; then
        echo "error: generated protobuf missing in this worktree (*.pb.go is gitignored, so a fresh git worktree never has it):" >&2
        printf '  %s\n' "${missing[@]}" >&2
        echo >&2
        echo "Without these, every package above a leaf fails to build with a misleading" >&2
        echo "\"no required module provides package github.com/ctxloom/ctxloom/...\" — that is" >&2
        echo "THIS, not your change. Do NOT run the 'go get' go suggests, and do NOT copy" >&2
        echo "*.pb.go from another checkout (a stale one still compiles, against the wrong ABI)." >&2
        echo >&2
        echo "fix:  just proto                        # host: regenerates via the devcontainer" >&2
        echo "  or: just build                        # host: proto + schemas + binary" >&2
        echo "  or: just -f justfile.container proto   # already inside the devcontainer" >&2
        exit 1
    fi
