#!/usr/bin/env bash
# nextest's setup script `build-binaries` (.config/nextest.toml, owner ruling
# 144): build the programs the acceptance tests of the `ctxloom` package run,
# ONCE per nextest run, and name each built executable to every test through
# $NEXTEST_ENV (https://nexte.st/docs/configuration/setup-scripts/). Without
# it each test process (nextest runs one per test) built them itself, a cargo
# freshness check of about 2.3 s per test.
#
# - CTXLOOM_TEST_BINARY: `ctxloom` with `test-support,purge`
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
# diagnostics are rendered to stderr, which nextest shows on a failure). A
# Windows jq ends its line CRLF, and the CR is no part of the path.
built() {
    local name=$1
    shift
    "$cargo" build --quiet --message-format=json-render-diagnostics "$@" --bin "$name" \
        | jq -r --arg name "$name" 'select(.reason == "compiler-artifact" and .target.name == $name
            and .executable != null) | .executable' | tr -d '\r'
}

ctxloom=$(built ctxloom --package ctxloom --features test-support,purge)
stub=$(built ctxloom-companion-stub --package stub-companion)
for exe in "$ctxloom" "$stub"; do
    if [ ! -x "$exe" ]; then
        echo "nextest-build-binaries: cargo named no executable (got '$exe')" >&2
        exit 1
    fi
done
# `ctxloom` is run from a copy: <profile>/ctxloom is where any cargo build of
# the package writes, and a plain one (`just build`'s, in this checkout while
# a test run goes on) over it made the tests run a binary with no test-support,
# whose account home is the real one, and reach the owner's real daemon.
# (crates/root/bin/ctxloom/src/main.test.rs `TEST_BINARIES`.)
kept="$(dirname "$ctxloom")/test-binaries"
mkdir -p "$kept"
cp "$ctxloom" "$kept/.ctxloom.$$"
mv -f "$kept/.ctxloom.$$" "$kept/$(basename "$ctxloom")"
ctxloom="$kept/$(basename "$ctxloom")"
{
    echo "CTXLOOM_TEST_BINARY=$ctxloom"
    echo "CTXLOOM_TEST_COMPANION_STUB=$stub"
} >> "$NEXTEST_ENV"
# Named for gates/red-sanity.sh too, which checks after a red run that they
# still exist (a deleted build tree is no test failure).
printf '%s\n%s\n' "$ctxloom" "$stub" > "${CARGO_TARGET_DIR:-target}/nextest-binaries.list"
