#!/usr/bin/env bash
# Self-test of the memory cap of the mutation run and the test gates,
# gates/memory-cap.sh (or $MEMORY_CAP). Checks: with no user bus to reach it
# FAILS, saying why and the fix, and runs nothing (the command, and
# `--check`); the opt-out `none` runs the command uncapped and says so; both
# name the caller's opt-out variable (MEMORY_CAP_OPT_OUT: the test gates'
# TESTS_MEMORY_MAX); usage is refused. Where a systemd user bus is
# reachable, also: the command runs inside a scope whose memory.max is the
# cap, its exit code is the wrapper's, and a process of it that outgrows the
# cap is OOM-killed while the run itself goes on (OOMPolicy=continue; a lone
# runaway, as the kernel may take a second process of the scope), the
# peak and the kill said; and, with cargo-nextest installed, a nextest run
# of a fixture crate under the cap whose one test runs away: that test alone
# is killed and reported failed, the tests beside it pass, and the run ends
# with nextest's own 100 (a test failed). Without a bus (or nextest) that
# part says it skips. How `_tests` is wrapped is tests-exit.selftest.sh's.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
cap=${MEMORY_CAP:-$here/../gates/memory-cap.sh}
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
fail() { echo "FAIL memory-cap.selftest: $*" >&2; exit 1; }

# No bus: no XDG_RUNTIME_DIR, no DBUS_SESSION_BUS_ADDRESS, and an empty
# runtime directory in place of /run/user.
nobus=(env -u XDG_RUNTIME_DIR -u DBUS_SESSION_BUS_ADDRESS "MEMORY_CAP_RUN_USER=$tmp/run-user")
mkdir -p "$tmp/run-user/$(id -u)"
rc=0
"${nobus[@]}" "$cap" probe 1G 0 touch "$tmp/ran" > "$tmp/out" 2>&1 || rc=$?
[ "$rc" -eq 1 ] || fail "no bus: exit $rc, not 1: $(cat "$tmp/out")"
[ ! -e "$tmp/ran" ] || fail "no bus: the command ran uncapped"
grep -q "^FAIL probe: no memory cap: no systemd user bus" "$tmp/out" || fail "no bus: no FAIL line: $(cat "$tmp/out")"
grep -q "^  fix: .*MUTANTS_MEMORY_MAX=none" "$tmp/out" || fail "no bus: no fix naming the opt-out: $(cat "$tmp/out")"
rc=0
"${nobus[@]}" "$cap" probe 1G 0 --check > "$tmp/out" 2>&1 || rc=$?
[ "$rc" -eq 1 ] || fail "no bus, --check: exit $rc, not 1: $(cat "$tmp/out")"
grep -q "^FAIL probe: no memory cap" "$tmp/out" || fail "no bus, --check: no FAIL line: $(cat "$tmp/out")"

# The opt-out: uncapped, said, the command's own exit.
rc=0
"${nobus[@]}" "$cap" probe none 0 bash -c 'touch "$1"; exit 7' opt "$tmp/ran" > "$tmp/out" 2>&1 || rc=$?
[ "$rc" -eq 7 ] || fail "opt-out: exit $rc, not the command's 7: $(cat "$tmp/out")"
[ -e "$tmp/ran" ] || fail "opt-out: the command did not run"
grep -q "^probe: memory cap: none (MUTANTS_MEMORY_MAX=none): this run is UNCAPPED" "$tmp/out" \
    || fail "opt-out: not said: $(cat "$tmp/out")"
rm -f "$tmp/ran"
"${nobus[@]}" "$cap" probe none 0 --check > "$tmp/out" 2>&1 || fail "opt-out, --check: failed: $(cat "$tmp/out")"
grep -q "UNCAPPED" "$tmp/out" || fail "opt-out, --check: not said: $(cat "$tmp/out")"

# The caller's opt-out variable is named, failing and opting out alike.
tests_opt=(env MEMORY_CAP_OPT_OUT=TESTS_MEMORY_MAX)
rc=0
"${tests_opt[@]}" "${nobus[@]}" "$cap" tests 1G 0 touch "$tmp/ran" > "$tmp/out" 2>&1 || rc=$?
[ "$rc" -eq 1 ] || fail "tests, no bus: exit $rc, not 1: $(cat "$tmp/out")"
[ ! -e "$tmp/ran" ] || fail "tests, no bus: the command ran uncapped"
grep -q "^  fix: .*TESTS_MEMORY_MAX=none" "$tmp/out" || fail "tests, no bus: the fix does not name TESTS_MEMORY_MAX: $(cat "$tmp/out")"
! grep -q MUTANTS_MEMORY_MAX "$tmp/out" || fail "tests, no bus: the mutation run's variable named: $(cat "$tmp/out")"
"${tests_opt[@]}" "${nobus[@]}" "$cap" tests none 0 touch "$tmp/ran" > "$tmp/out" 2>&1 || fail "tests, opt-out: failed: $(cat "$tmp/out")"
[ -e "$tmp/ran" ] || fail "tests, opt-out: the command did not run"
grep -q "^tests: memory cap: none (TESTS_MEMORY_MAX=none): this run is UNCAPPED" "$tmp/out" \
    || fail "tests, opt-out: not said in its own variable: $(cat "$tmp/out")"
rm -f "$tmp/ran"

# Usage.
rc=0
"$cap" probe 1G > "$tmp/out" 2>&1 || rc=$?
[ "$rc" -eq 2 ] || fail "usage: exit $rc, not 2"

# A real scope, where this machine has a user bus.
if ! "$cap" probe 1G 0 --check > "$tmp/out" 2>&1; then
    echo "SKIP memory-cap.selftest (the capped scope): no systemd user scope here: $(head -n 1 "$tmp/out")"
    echo "ok   memory-cap.selftest"
    exit 0
fi
# shellcheck disable=SC2016
rc=0
"$cap" probe 64M 0 bash -c 'cat "/sys/fs/cgroup$(sed -n "s/^0:://p" /proc/self/cgroup)/memory.max"; exit 5' \
    > "$tmp/out" 2>&1 || rc=$?
[ "$rc" -eq 5 ] || fail "scope: exit $rc, not the command's 5: $(cat "$tmp/out")"
grep -qx "$((64 * 1024 * 1024))" "$tmp/out" || fail "scope: memory.max is not the 64M cap: $(cat "$tmp/out")"
grep -q "^probe: memory peak .* (cap MemoryMax=64M MemorySwapMax=0)" "$tmp/out" || fail "scope: no peak line: $(cat "$tmp/out")"
# A process of the run outgrows the cap: the cap's OOM killer kills it, the
# machine is untouched and the wrapper (the run) goes on to say so and to
# exit with the command's own status, 137. The runaway is ONE process, no
# shell or pipe beside it: the kernel's cgroup OOM may take a SECOND process
# of the scope when one charges memory in the instant before the first
# victim's is released (measured on GitHub's ubuntu-24.04 runner, kernel
# 7.0: `head -c 256M /dev/zero | tail` under the cap took the wrapper
# too, 21 runs in 60; on 6.12 none in 40). That race is the kernel's and is
# not asserted away here; a lone runaway leaves it nothing to race with, so
# a scope stopped by the manager (OOMPolicy=stop) or an OOM kill of the
# wrapper shows as no kill line.
rc=0
"$cap" probe 64M 0 tail < /dev/zero > "$tmp/out" 2>&1 || rc=$?
[ "$rc" -eq 137 ] || fail "OOM: exit $rc, not the runaway's own 137: $(cat "$tmp/out")"
grep -q "^probe: memory peak .*; the cap's OOM killer killed 1 process(es) of the run" "$tmp/out" \
    || fail "OOM: the wrapper did not go on to say the kill (the scope was stopped?): $(cat "$tmp/out")"

# A test suite, as the test gates run one: nextest, each test a process of
# its own, under the cap. One test runs away; the cap's OOM killer kills it
# alone, nextest reports it failed and the others passed, and the run ends
# with nextest's 100 (a test failed), not a stopped scope.
if ! cargo nextest --version > /dev/null 2>&1; then
    echo "SKIP memory-cap.selftest (a runaway test under nextest): cargo-nextest is not installed"
    echo "ok   memory-cap.selftest"
    exit 0
fi
suite=$tmp/suite
mkdir -p "$suite/src"
printf '[package]\nname = "suite"\nversion = "0.1.0"\nedition = "2021"\n\n[workspace]\n' > "$suite/Cargo.toml"
cat > "$suite/src/lib.rs" <<'R'
#[cfg(test)]
mod tests {
    #[test]
    fn fine_before() {}
    // Grows without bound, every page written, until something stops it.
    #[test]
    fn runaway() {
        let mut held: Vec<Vec<u8>> = Vec::new();
        loop {
            held.push(vec![7u8; 1 << 20]);
        }
    }
    #[test]
    fn fine_after() {}
}
R
export CARGO_TARGET_DIR=$tmp/suite-target CARGO_TERM_COLOR=never
# Built outside the cap: the cap is the run's.
cargo test -q --manifest-path "$suite/Cargo.toml" --no-run > "$tmp/out" 2>&1 || fail "suite: did not build: $(tail -n 20 "$tmp/out")"
rc=0
"${tests_opt[@]}" "$cap" tests 512M 0 cargo nextest run --manifest-path "$suite/Cargo.toml" --no-fail-fast \
    --status-level all --final-status-level fail --hide-progress-bar > "$tmp/out" 2>&1 || rc=$?
[ "$rc" -eq 100 ] || fail "suite: exit $rc, not nextest's 100 for a failed test (the scope was stopped?): $(tail -n 30 "$tmp/out")"
grep -Eq "PASS .*fine_before" "$tmp/out" || fail "suite: the test before it did not pass: $(cat "$tmp/out")"
grep -Eq "PASS .*fine_after" "$tmp/out" || fail "suite: the test after it did not pass: $(cat "$tmp/out")"
grep -Eq "(SIGKILL|FAIL) .*runaway" "$tmp/out" || fail "suite: the runaway is not reported failed: $(cat "$tmp/out")"
grep -q "the cap's OOM killer killed 1 process(es) of the run" "$tmp/out" || fail "suite: the kill not said: $(tail -n 30 "$tmp/out")"
echo "ok   memory-cap.selftest"
