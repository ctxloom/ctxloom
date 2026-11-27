#!/usr/bin/env bash
# Run a whole-workspace test run under the machine-wide test lock:
# `tests-lock.sh <label> <command>...`.
#
# Why (2026-11-16): each whole-workspace run is memory-capped
# (gates/memory-cap.sh, `tests_memory_max`), but up to three agents work on
# one machine, and two capped runs at once can still take all its RAM (two
# global OOMs took an editor and every session down). Agents serialized
# their runs by hand with an outer flock, but the pre-commit hook's
# `test-staged` runs the whole workspace when a `workspace_wide` file is
# staged, and took no lock. Now every whole-workspace run takes this one:
# `test`, the workspace leg of `test-staged` (`_test-workspace`, and
# `push-check`'s and `merge-check`'s through it), `_tests --workspace` run by
# itself, so `closeout`'s, `closeout-ratchet`'s and `land-ratchet`'s test
# legs, and scripts/ratchet-base.sh's feature runner. A per-crate run
# (`_tests <packages>`, `test-pkg`, `test-staged`'s changed crates) does not.
#
# The lock: flock(1) on ONE file per user, `$TESTS_LOCK_FILE`, by default
# `${XDG_CACHE_HOME:-<account home>/.cache}/ctxloom/gates/workspace-tests.lock`,
# the account's home from the passwd database (gates/account-cache.sh, the one
# resolution, shared with the staged target), NEVER `$HOME`: an agent session's
# HOME is a per-session home (ruling 112), and keyed on it each session had a
# lock file of its own and runs in different sessions did not serialize
# (2026-11-27: ten such files).
# The cache dir, not $XDG_RUNTIME_DIR: an agent's shell often has no
# XDG_RUNTIME_DIR (memory-cap.sh supplies one) and the landing command sets
# its own, so keyed on it two runs could take two different files; and
# /run/user/<uid> is removed when the user's last session ends, so a run
# holding it could be joined by one that made it anew. The cache dir is on a
# persistent filesystem. The path
# names no repository, so the copies of these gates in taskloom and annals
# serialize against ctxloom's too, as the memory they share needs.
#
# Waiting: a run that finds the lock held says so ONCE on stderr, what it
# waits for and the holder's pid, label and directory (the holder writes
# them into the lock file once it has it; "pid unknown" when it has not
# yet), then waits for it. No timeout: flock is released by the kernel when
# its holder dies, however it dies, so a crashed or killed run leaves no
# stale lock and a wait is only ever on a live run, which nextest bounds
# (each test killed at 120 s). A timeout would fail a slow but legitimate
# wait, and its retry is the overlap this lock is for. A holder that hangs
# is found by the pid said.
#
# Nesting: the holder exports GATES_TESTS_LOCK_HELD=<the lock file> to its
# command, and a run that finds it naming its lock file is inside the
# holder's run and takes nothing (flock would block on its own ancestor:
# the lock is per open file description, and the descriptor is not passed
# down, below). So `land-ratchet` -> `test` -> `_test-workspace` ->
# `_tests --workspace` takes it once, at the outermost.
#
# The descriptor (8) is this script's alone: the command runs with `8>&-`.
# bash opens it without close-on-exec, so a command given it would hand it
# to everything it starts, and a process that outlives the run (a test's
# detached daemon) would hold every later run's lock (OD-64, the same bug
# with `.staged-tree.lock`, 2026-11-15).
#
# No flock (macOS, Windows' Git Bash: CI, where each job has a machine of
# its own): the run is not serialized, and that is said once on stderr,
# never silently; its command still runs, with GATES_TESTS_LOCK_HELD set so
# a nested run does not say it again.
#
# Exit: the command's own; 2 on usage. Self-test:
# scripts/tests-lock.selftest.sh (`just selftest`).
set -euo pipefail
[ $# -ge 2 ] || { echo "usage: tests-lock.sh <label> <command>..." >&2; exit 2; }
label=$1
shift
lock=${TESTS_LOCK_FILE:-$("$(dirname "${BASH_SOURCE[0]}")/account-cache.sh")/workspace-tests.lock}

if [ "${GATES_TESTS_LOCK_HELD:-}" = "$lock" ]; then
    exec "$@"
fi
export GATES_TESTS_LOCK_HELD=$lock
if ! command -v flock > /dev/null 2>&1; then
    echo "note $label: flock is not on PATH, so this whole-workspace test run is not serialized machine-wide ($lock)" >&2
    exec "$@"
fi

mkdir -p "$(dirname "$lock")"
# `>>`, never `>`: opening must not truncate the holder's pid away.
exec 8>> "$lock"
if ! flock -n 8; then
    holder=$(head -c 512 "$lock" 2>/dev/null | tr -d '\n' || true)
    echo "wait $label: another whole-workspace test run holds the machine-wide test lock $lock (${holder:-pid unknown}); waiting for it" >&2
    flock 8
fi
printf 'pid %s: %s in %s\n' "$$" "$label" "$PWD" > "$lock"
rc=0
"$@" 8>&- || rc=$?
# Still holding it: the next holder writes its own.
: > "$lock"
exit "$rc"
