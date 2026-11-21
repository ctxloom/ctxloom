#!/usr/bin/env bash
# The throwaway tree's lock is the gate's alone: `_scratch-tree` holds
# `target/.staged-tree.lock` (flock, an fd bash opens without close-on-exec)
# for the run, and the recipe it runs must not inherit that descriptor, or
# anything the recipe leaves running (a test's detached daemon) holds the
# gate's lock after the gate, and the next commit's gate waits on it
# (2026-11-15, U8r: two leaked test daemons, half an hour). The same holds
# for scripts/ratchet-base.sh's runner and its `.ratchet-base.lock`.
# Checked through procfs: the recipe lists its own descriptors, and starts a
# detached sleeper that outlives it; once the gate returns, the lock is free.
# Off Linux (no /proc/self/fd) it says it skips. The gates are
# gates/gates.just (or $GATES_JUST).
set -euo pipefail
while IFS= read -r v; do unset "$v"; done < <(git rev-parse --local-env-vars)
if [ ! -d /proc/self/fd ]; then
    echo "SKIP scratch-tree-lock.selftest: no /proc/self/fd"
    exit 0
fi
here=$(cd "$(dirname "$0")" && pwd)
gates=${GATES_JUST:-$here/../gates/gates.just}
tmp=$(mktemp -d)
sleeper=
trap '[ -z "$sleeper" ] || kill "$sleeper" 2>/dev/null || true; rm -rf "$tmp"' EXIT
fail() { echo "FAIL scratch-tree-lock.selftest: $*" >&2; exit 1; }

fx=$tmp/fx
mkdir -p "$fx/gates"
cp "$gates" "$fx/gates/gates.just"
cp "$(dirname "$gates")/drop-worktree.sh" "$fx/gates/drop-worktree.sh"
cp "$(dirname "$gates")/memory-cap.sh" "$fx/gates/memory-cap.sh"
cp "$(dirname "$gates")/tests-lock.sh" "$fx/gates/tests-lock.sh"
cp "$(dirname "$gates")/red-sanity.sh" "$fx/gates/red-sanity.sh"
# The fixture's workspace runs take a test lock of their own, never the
# machine's (gates/tests-lock.sh), and are inside no other run's.
export TESTS_LOCK_FILE=$tmp/workspace-tests.lock
unset GATES_TESTS_LOCK_HELD
cat > "$fx/justfile" <<'J'
TOP := `git rev-parse --show-toplevel`
import 'gates/gates.just'
staged_tree := "../.{name}.staged"
feature_rules := ""
features_prepare := ""
red_judge := ""
staged_tests_filter := ""
mutation_thresholds := "crates=90"
mutants_memory_max := "16G"
mutants_swap_max := "2G"
tests_memory_max := "none"
tests_swap_max := "0"
workspace_wide := "Cargo.toml"

# What the recipe's process holds open, one target per line, and a
# sleeper that outlives the recipe in a session of its own, as a test's
# daemon does: its pid.
[positional-arguments]
probe out:
    #!/usr/bin/env bash
    set -euo pipefail
    for fd in /proc/$$/fd/*; do readlink "$fd" || true; done > "$1.fds"
    setsid sleep 30 > /dev/null 2>&1 < /dev/null &
    echo $! > "$1.sleeper"
J
git -C "$fx" init -q -b main
printf 'target/\n' > "$fx/.gitignore"
git -C "$fx" add -A
git -C "$fx" -c user.email=t@e -c user.name=t commit -q -m init

(cd "$fx" && just _staged-tree probe "$tmp/probe" > "$tmp/out" 2>&1) \
    || fail "the gate's recipe failed: $(tail -n 20 "$tmp/out")"
sleeper=$(cat "$tmp/probe.sleeper")
lock=$fx/target/.staged-tree.lock
[ -e "$lock" ] || fail "the gate made no lock at $lock"
if grep -qF '.staged-tree.lock' "$tmp/probe.fds"; then
    fail "the recipe inherited the gate's lock: $(grep -F '.staged-tree.lock' "$tmp/probe.fds")"
fi
kill -0 "$sleeper" 2>/dev/null || fail "the sleeper did not outlive the recipe"
flock -n "$lock" true || fail "what the recipe left running holds the gate's lock"

# ratchet-base runs its runner without its lock's descriptor.
grep -q '7>&-' "$here/ratchet-base.sh" \
    || fail "scripts/ratchet-base.sh runs its runner with its lock's descriptor (7) open"

echo "ok   scratch-tree-lock.selftest"
