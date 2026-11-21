#!/usr/bin/env bash
# nextest's setup script `guard-homes` (.config/nextest.toml, OD-34): a test
# run inherits no home of the shell it was started in.
#
# The owner's shell and every agent's (a ctxloom session's) carry
# CTXLOOM_HOME, which ctxloom reads BEFORE HOME, and an engine's relocated
# CLAUDE_CONFIG_DIR, OPENCODE_CONFIG_DIR and XDG bases. A test that started
# from the process's environment and kept one of them wrote into the
# caller's real home: the wiring's fixtures, before 6911bd804, registered 85
# temporary projects in the owner's ~/.ctxloom-rs. Each is named here, for
# every test, as a FILE, under which nothing can be created: a test that
# reads one and writes under it fails (ENOTDIR) instead. A setup script can
# set a variable but not unset one, hence a file, not an absence.
#
# The tests that place a home do so themselves (a temporary one); the
# wiring's fixtures drop these names, and the binary's acceptance tests clear
# their child's environment. ctxloom-wiring's
# `test_the_run_inherits_no_home_a_test_could_write_into` holds the run to it.
set -euo pipefail
: "${NEXTEST_ENV:?run by nextest as a setup script, which names the file to write}"

target=${CARGO_TARGET_DIR:-target}
mkdir -p "$target/test-guard"
guard=$(cd "$target/test-guard" && pwd)/inherited-home
if [ ! -f "$guard" ]; then
    tmp=$(mktemp "$guard.XXXXXX")
    echo "Not a home: a test run names this file as every inherited home (scripts/nextest-guard-homes.sh, OD-34)." > "$tmp"
    mv -f "$tmp" "$guard"
fi
# Git Bash spells a path /c/...; a Windows test reads C:/...
if command -v cygpath >/dev/null 2>&1; then
    guard=$(cygpath -m "$guard")
fi
for name in CTXLOOM_HOME CLAUDE_CONFIG_DIR OPENCODE_CONFIG_DIR \
    XDG_CONFIG_HOME XDG_DATA_HOME XDG_STATE_HOME XDG_CACHE_HOME; do
    echo "$name=$guard"
done >> "$NEXTEST_ENV"
