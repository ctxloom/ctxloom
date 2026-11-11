#!/usr/bin/env bash
# nextest's setup script `build-binaries` (.config/nextest.toml, owner ruling
# 144): build the programs the acceptance tests of the `ctxloom` package run,
# ONCE per nextest run, and name each built executable to every test through
# $NEXTEST_ENV (https://nexte.st/docs/configuration/setup-scripts/). Without
# it each test process (nextest runs one per test) built them itself, a cargo
# freshness check of about 2.3 s per test.
#
# - CTXLOOM_TEST_BINARY: `ctxloom` with `test-support,session-destroy`
#   (crates/root/bin/ctxloom/src/main.test.rs `binary()`);
# - CTXLOOM_TEST_COMPANION_STUB: `ctxloom-companion-stub`
#   (companion_stub.test.rs `binary()`).
#
# nextest runs it from the workspace root with the run's environment, so the
# build lands in the same target directory as the tests (CARGO_TARGET_DIR,
# when the gates set one). It builds the dev profile, the profile the gates
# and cargo-mutants test in; a test built in another profile ignores these
# paths (`binary()` checks the directory) and builds for itself.
set -euo pipefail
: "${NEXTEST_ENV:?run by nextest as a setup script, which names the file to write}"
cargo=${CARGO:-cargo}

# The executable cargo built for `--bin <name>`, from its JSON messages (the
# diagnostics are rendered to stderr, which nextest shows on a failure).
built() {
    local name=$1
    shift
    "$cargo" build --quiet --message-format=json-render-diagnostics "$@" --bin "$name" \
        | jq -r --arg name "$name" 'select(.reason == "compiler-artifact" and .target.name == $name
            and .executable != null) | .executable'
}

ctxloom=$(built ctxloom --package ctxloom --features test-support,session-destroy)
stub=$(built ctxloom-companion-stub --package stub-companion)
for exe in "$ctxloom" "$stub"; do
    if [ ! -x "$exe" ]; then
        echo "nextest-build-binaries: cargo named no executable (got '$exe')" >&2
        exit 1
    fi
done
{
    echo "CTXLOOM_TEST_BINARY=$ctxloom"
    echo "CTXLOOM_TEST_COMPANION_STUB=$stub"
} >> "$NEXTEST_ENV"
