#!/usr/bin/env bash
# The machine-wide test lock (gates/tests-lock.sh), checked on a fixture
# workspace whose `cargo` is a fake that records when its `nextest run`
# starts and ends, what descriptors it holds, and leaves a detached sleeper
# behind, as a test's daemon does:
#   - two concurrent `just test` runs serialize: the second starts its tests
#     only once the first has ended, and says ONCE on stderr that it waits,
#     naming the holder's pid; nesting inside its own run (`test` ->
#     `_test-workspace` -> `_tests --workspace`, each a taker) does not
#     deadlock, nor does a run nested in an explicit outer holder;
#   - the run's processes do not inherit the lock's descriptor, and what a
#     run leaves running does not hold the lock after it;
#   - a run of named packages (`_tests <package>`, `test-pkg`) takes no lock:
#     it runs while the lock is held;
#   - with no flock on PATH the lock is skipped and that is said once, a
#     nested run saying nothing more.
# Every wait is bounded by `timeout`, so a deadlock fails, never hangs. Off
# Linux (no /proc/self/fd) or with no flock, the parts that need them say
# they skip. The gates are gates/gates.just (or $GATES_JUST).
set -euo pipefail
while IFS= read -r v; do unset "$v"; done < <(git rev-parse --local-env-vars)
unset GATES_TESTS_LOCK_HELD
here=$(cd "$(dirname "$0")" && pwd)
gates=${GATES_JUST:-$here/../gates/gates.just}
lock_sh=$(dirname "$gates")/tests-lock.sh
tmp=$(mktemp -d)
cleanup() {
    [ ! -s "$tmp/sleepers" ] || xargs kill < "$tmp/sleepers" 2>/dev/null || true
    rm -rf "$tmp"
}
trap cleanup EXIT
fail() { echo "FAIL tests-lock.selftest: $*" >&2; exit 1; }
export TESTS_LOCK_FILE=$tmp/workspace-tests.lock
lock=$TESTS_LOCK_FILE

# No flock: skipped and said, once, nested or not; the command still runs.
nobin=$tmp/noflock
mkdir -p "$nobin"
for tool in bash true; do ln -s "$(type -P "$tool")" "$nobin/$tool"; done
rc=0
env PATH="$nobin" bash "$lock_sh" outer bash "$lock_sh" inner true > "$tmp/noflock.out" 2> "$tmp/noflock.err" || rc=$?
[ "$rc" -eq 0 ] || fail "with no flock the command did not run: exit $rc: $(cat "$tmp/noflock.err")"
[ "$(grep -c '' "$tmp/noflock.err")" -eq 1 ] \
    || fail "with no flock, not said exactly once: $(cat "$tmp/noflock.err")"
grep -q "^note outer: flock is not on PATH, so this whole-workspace test run is not serialized" "$tmp/noflock.err" \
    || fail "with no flock, the skip is not said: $(cat "$tmp/noflock.err")"
[ ! -e "$lock" ] || fail "with no flock, a lock file was made"

if ! command -v flock > /dev/null 2>&1 || [ ! -d /proc/self/fd ]; then
    echo "SKIP tests-lock.selftest: serializing, nesting and the descriptor need flock and /proc/self/fd"
    echo "ok   tests-lock.selftest (no flock: said once)"
    exit 0
fi

fx=$tmp/fx
mkdir -p "$fx/gates" "$fx/crates/a/src" "$tmp/bin"
cp "$gates" "$fx/gates/gates.just"
for f in drop-worktree.sh memory-cap.sh tests-lock.sh; do cp "$(dirname "$gates")/$f" "$fx/gates/$f"; done
printf '[workspace]\nmembers = ["crates/a"]\nresolver = "2"\n' > "$fx/Cargo.toml"
printf '[package]\nname = "a"\nversion = "0.1.0"\nedition = "2021"\n' > "$fx/crates/a/Cargo.toml"
: > "$fx/crates/a/src/lib.rs"
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
J
# The fake cargo: `nextest run` logs `start|end <RUN> <ns>`, lists its own
# descriptors' targets to $tmp/fds.<RUN>, leaves a detached sleeper (its pid
# to $tmp/sleepers) and holds for HOLD seconds; everything else succeeds
# (the build, the doc tests, `nextest --version`, `metadata`'s one package).
cat > "$tmp/bin/cargo" <<S
#!/usr/bin/env bash
case "\${1:-} \${2:-}" in
    "nextest run")
        echo "start \$RUN \$(date +%s%N)" >> "$tmp/log"
        for fd in /proc/\$\$/fd/*; do readlink "\$fd" || true; done > "$tmp/fds.\$RUN"
        setsid sleep 60 > /dev/null 2>&1 < /dev/null &
        echo \$! >> "$tmp/sleepers"
        sleep "\${HOLD:-0}"
        echo "end \$RUN \$(date +%s%N)" >> "$tmp/log"
        ;;
    "metadata "*) echo '{"packages":[{"name":"a","targets":[{"kind":["lib"]}]}]}' ;;
esac
exit 0
S
chmod +x "$tmp/bin/cargo"
export PATH=$tmp/bin:$PATH
git -C "$fx" init -q -b main
git -C "$fx" -c user.email=t@e -c user.name=t commit -q --allow-empty -m init

# Two concurrent whole-workspace runs serialize.
(cd "$fx" && RUN=A HOLD=3 timeout 60 just test > "$tmp/A.out" 2> "$tmp/A.err") & a_pid=$!
for _ in $(seq 1 200); do grep -q '^start A' "$tmp/log" 2>/dev/null && break; sleep 0.05; done
grep -q '^start A' "$tmp/log" || fail "the first run never started its tests: $(cat "$tmp/A.err")"
holder=$(cat "$lock")
rc=0
(cd "$fx" && RUN=B timeout 60 just test > "$tmp/B.out" 2> "$tmp/B.err") || rc=$?
wait "$a_pid" || fail "the first run failed: $(cat "$tmp/A.out" "$tmp/A.err")"
[ "$rc" -eq 0 ] || fail "the second run failed (124 is a deadlock's timeout): exit $rc: $(cat "$tmp/B.out" "$tmp/B.err")"
a_end=$(sed -n 's/^end A //p' "$tmp/log")
b_start=$(sed -n 's/^start B //p' "$tmp/log")
[ -n "$a_end" ] && [ -n "$b_start" ] || fail "the runs' log is incomplete: $(cat "$tmp/log")"
[ "$b_start" -ge "$a_end" ] || fail "the second run started its tests before the first ended: $(cat "$tmp/log")"
[ "$(grep -c '^wait ' "$tmp/B.err")" -eq 1 ] || fail "the waiter did not say it waits exactly once: $(cat "$tmp/B.err")"
grep -qF "wait test: another whole-workspace test run holds the machine-wide test lock $lock ($holder); waiting for it" "$tmp/B.err" \
    || fail "the waiter did not name the lock and its holder ($holder): $(cat "$tmp/B.err")"
[[ $holder == "pid "[0-9]*": test in $fx" ]] || fail "the holder did not write its pid, label and directory: $holder"
! grep -q '^wait ' "$tmp/A.err" || fail "the first run waited on its own nested taker: $(cat "$tmp/A.err")"
grep -q '^ok   test (workspace) ' "$tmp/A.out" || fail "the first run's gate line: $(cat "$tmp/A.out")"

# The descriptor: no process of the run holds the lock, and what the runs
# left running does not hold it after them.
for run in A B; do
    if grep -qF "$lock" "$tmp/fds.$run"; then fail "run $run's tests inherited the lock: $(cat "$tmp/fds.$run")"; fi
done
xargs kill -0 < "$tmp/sleepers" 2>/dev/null || fail "the sleepers did not outlive the runs"
flock -n "$lock" true || fail "what the runs left running holds the lock"

# Nested in an explicit outer holder (as land-ratchet's test leg under a
# caller that holds it): no deadlock, no wait.
rc=0
(cd "$fx" && RUN=C timeout 60 "$fx/gates/tests-lock.sh" outer just test > "$tmp/C.out" 2> "$tmp/C.err") || rc=$?
[ "$rc" -eq 0 ] || fail "a run nested in a holder failed (124 is a deadlock's timeout): exit $rc: $(cat "$tmp/C.err")"
! grep -q '^wait ' "$tmp/C.err" || fail "a run nested in a holder waited on it: $(cat "$tmp/C.err")"

# A run of named packages takes no lock: it runs while another holds it.
flock "$lock" sleep 30 & held=$!
for _ in $(seq 1 100); do flock -n "$lock" true || break; sleep 0.05; done
rc=0
(cd "$fx" && RUN=D timeout 20 just _tests a > "$tmp/D.out" 2> "$tmp/D.err") || rc=$?
kill "$held" 2>/dev/null || true
wait "$held" 2>/dev/null || true
[ "$rc" -eq 0 ] || fail "a run of named packages waited on the lock (124) or failed: exit $rc: $(cat "$tmp/D.err")"
! grep -q '^wait ' "$tmp/D.err" || fail "a run of named packages waited on the lock: $(cat "$tmp/D.err")"

echo "ok   tests-lock.selftest (serialized, nested, descriptor closed, packages unlocked, no flock said)"
