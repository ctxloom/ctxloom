#!/usr/bin/env bash
# Self-test of scripts/land-sweep.sh (or $LAND_SWEEP): a fixture cargo
# workspace of two crates, `old` and `fresh`, built into a staged target of its
# own (GATES_STAGED_CACHE) and its checkout's target/. It holds that the sweep
# removes target/ratchet-base and the incremental directories, keeps
# target/ratchet/*.list, ages the staged target (old's units go, fresh's stay)
# under staged-tree.lock; and that a held staged-tree.lock, a held
# target/.ratchet-base.lock and a held cargo build lock each skip their sweep
# and say so. Needs cargo and the pinned cargo-sweep (TRIM_SWEEP_VERSION,
# default 0.8.0).
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
sweep=${LAND_SWEEP:-$here/land-sweep.sh}
export TRIM_SWEEP_VERSION=${TRIM_SWEEP_VERSION:-0.8.0}
tmp=$(mktemp -d)
holders=()
cleanup() { for h in "${holders[@]}"; do kill -- "-$h" 2>/dev/null || true; done; rm -rf "$tmp"; }
trap cleanup EXIT
fail() { echo "FAIL land-sweep.selftest: $*" >&2; exit 1; }
unset CARGO_TARGET_DIR CARGO_BUILD_TARGET_DIR RUSTC_WRAPPER
has() { grep -qF -- "$2" <<< "$1" || fail "no line '$2' in: $1"; }
hasnt() { ! grep -qF -- "$2" <<< "$1" || fail "unexpected '$2' in: $1"; }
hold() { # hold <lockfile>: flock it in the background until the test ends
    setsid flock "$1" sleep 120 < /dev/null > /dev/null 2>&1 &
    holders+=($!)
    for _ in $(seq 50); do flock -n "$1" true 2> /dev/null || return 0; sleep 0.1; done
    fail "could not hold $1"
}

ws=$tmp/ws
export GATES_STAGED_CACHE=$tmp/cache
st=$GATES_STAGED_CACHE/staged-target
mkdir -p "$ws/old/src" "$ws/fresh/src"
printf '[workspace]\nmembers = ["old", "fresh"]\nresolver = "2"\n' > "$ws/Cargo.toml"
for c in old fresh; do
    printf '[package]\nname = "%s"\nversion = "0.1.0"\nedition = "2021"\n' "$c" > "$ws/$c/Cargo.toml"
    printf 'pub fn %s() -> u32 { 1 }\n' "$c" > "$ws/$c/src/lib.rs"
done
(cd "$ws" && CARGO_INCREMENTAL=1 cargo build -q --offline --lib 2>&1) || fail "the fixture does not build"
(cd "$ws" && CARGO_TARGET_DIR=$st CARGO_INCREMENTAL=1 cargo build -q --offline --lib 2>&1) || fail "the staged fixture does not build"
age() { find "$1/debug/.fingerprint" -path '*/old-*' -exec touch -a -d '10 days ago' {} +; }
age "$st"
units() { find "$st/debug/deps" "$st/debug/.fingerprint" -mindepth 1 -maxdepth 1 -printf '%f\n' | sort; }
grow() {
    mkdir -p "$ws/target/ratchet-base/debug" "$ws/target/ratchet"
    echo x > "$ws/target/ratchet-base/debug/f"
    echo red > "$ws/target/ratchet/base.list"
    mkdir -p "$ws/target/debug/incremental/c-1"
    echo x > "$ws/target/debug/incremental/c-1/f"
}
grow
all=$(units)
mkdir -p "$GATES_STAGED_CACHE"

# Held locks: each sweep is skipped and said, nothing is removed.
hold "$GATES_STAGED_CACHE/staged-tree.lock"
hold "$ws/target/.ratchet-base.lock"
hold "$ws/target/debug/.cargo-lock"
out=$("$sweep" "$ws" 2>&1) || fail "exit $?: $out"
has "$out" "skip  sweep (ratchet-base): ratchet-base.sh holds its lock"
has "$out" "skip  sweep (incremental): cargo holds a build lock"
has "$out" "skip  sweep (staged-target): a commit gate holds $GATES_STAGED_CACHE/staged-tree.lock"
[ -e "$ws/target/ratchet-base/debug/f" ] || fail "ratchet-base was swept under its lock"
[ -e "$ws/target/debug/incremental/c-1/f" ] || fail "incremental was swept under cargo's lock"
[ "$(units)" = "$all" ] || fail "the staged target was swept under its lock"
for h in "${holders[@]}"; do kill -- "-$h" 2>/dev/null || true; done
holders=()
sleep 0.3

# Free: ratchet-base and incremental go, the cached list stays, the staged
# target is swept by age (old's units go, fresh's stay).
out=$("$sweep" "$ws" 2>&1) || fail "exit $?: $out"
has "$out" "ok   sweep (ratchet-base) freed "
has "$out" "ok   sweep (incremental, 1 dirs) freed "
has "$out" "ok   sweep (staged-target, unused 2 days) freed "
[ ! -e "$ws/target/ratchet-base" ] || fail "target/ratchet-base is still there"
[ ! -e "$ws/target/debug/incremental" ] || fail "the incremental directory is still there"
[ -s "$ws/target/ratchet/base.list" ] || fail "the cached list was swept"
left=$(units)
grep -q '^old-' <<< "$left" && fail "old's staged artifacts are still there: $left"
grep -q '^fresh-' <<< "$left" || fail "fresh's staged artifacts were swept: $left"

# Nothing to sweep: each says so, and the exit is 0.
out=$("$sweep" "$ws" 2>&1) || fail "second run exit $?: $out"
has "$out" "skip  sweep (ratchet-base): none"
has "$out" "skip  sweep (incremental): none"
hasnt "$out" "FAIL"
# The lock is let go: a commit gate can take it.
flock -n "$GATES_STAGED_CACHE/staged-tree.lock" true || fail "the sweep kept staged-tree.lock"
echo "ok   land-sweep.selftest"
