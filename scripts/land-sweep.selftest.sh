#!/usr/bin/env bash
# Self-test of scripts/land-sweep.sh (or $LAND_SWEEP), over fixture trees of
# plain files: a checkout's target/ and a staged target (GATES_STAGED_CACHE).
# It holds that the sweep removes target/ratchet-base and the incremental
# directories and keeps target/ratchet/*.list; that in the staged target the
# incremental directories go and the rest stays while it is within the cap
# (STAGED_TARGET_CAP), and that over the cap it is removed whole; and that a
# held staged-tree.lock, a held target/.ratchet-base.lock and a held cargo
# build lock each skip their sweep and say so.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
sweep=${LAND_SWEEP:-$here/land-sweep.sh}
tmp=$(mktemp -d)
holders=()
cleanup() { for h in "${holders[@]}"; do kill -- "-$h" 2>/dev/null || true; done; rm -rf "$tmp"; }
trap cleanup EXIT
fail() { echo "FAIL land-sweep.selftest: $*" >&2; exit 1; }
unset CARGO_TARGET_DIR CARGO_BUILD_TARGET_DIR STAGED_TARGET_CAP
has() { grep -qF -- "$2" <<< "$1" || fail "no line '$2' in: $1"; }
hasnt() { ! grep -qF -- "$2" <<< "$1" || fail "unexpected '$2' in: $1"; }
hold() { # hold <lockfile>: flock it in the background until the test ends
    setsid flock "$1" sleep 120 < /dev/null > /dev/null 2>&1 &
    holders+=($!)
    for _ in $(seq 50); do flock -n "$1" true 2> /dev/null || return 0; sleep 0.1; done
    fail "could not hold $1"
}
release() {
    for h in "${holders[@]}"; do kill -- "-$h" 2>/dev/null || true; done
    holders=()
    sleep 0.3
}

ws=$tmp/ws
export GATES_STAGED_CACHE=$tmp/cache
st=$GATES_STAGED_CACHE/staged-target
mkdir -p "$GATES_STAGED_CACHE"
grow() {
    rm -rf "$ws/target" "$st"
    mkdir -p "$ws/target/ratchet-base/debug" "$ws/target/ratchet" "$ws/target/debug/incremental/c-1"
    echo x > "$ws/target/ratchet-base/debug/f"
    echo red > "$ws/target/ratchet/base.list"
    echo x > "$ws/target/debug/incremental/c-1/f"
    mkdir -p "$st/debug/deps" "$st/debug/incremental/c-1" "$st/x86/debug/incremental/c-2"
    echo x > "$st/debug/deps/lib.rlib"
    head -c 100000 /dev/zero > "$st/debug/incremental/c-1/f"
    head -c 100000 /dev/zero > "$st/x86/debug/incremental/c-2/f"
}
grow

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
[ -e "$st/debug/incremental/c-1/f" ] || fail "the staged target was swept under its lock"
release

# Within the cap: ratchet-base and incremental go, the cached list stays; in
# the staged target the incremental directories go and the rest is kept.
out=$("$sweep" "$ws" 2>&1) || fail "exit $?: $out"
has "$out" "ok   sweep (ratchet-base) freed "
has "$out" "ok   sweep (incremental, 1 dirs) freed "
has "$out" "ok   sweep (staged-target incremental) freed "
hasnt "$out" "over"
[ ! -e "$ws/target/ratchet-base" ] || fail "target/ratchet-base is still there"
[ ! -e "$ws/target/debug/incremental" ] || fail "the incremental directory is still there"
[ -s "$ws/target/ratchet/base.list" ] || fail "the cached list was swept"
[ ! -e "$st/debug/incremental" ] || fail "the staged incremental directory is still there"
[ ! -e "$st/x86/debug/incremental" ] || fail "the staged nested incremental directory is still there"
[ -s "$st/debug/deps/lib.rlib" ] || fail "the staged target's other files were swept within the cap"
flock -n "$GATES_STAGED_CACHE/staged-tree.lock" true || fail "the sweep kept staged-tree.lock"

# Over the cap (still, after the incremental directories): removed whole.
grow
head -c 300000 /dev/zero > "$st/debug/deps/big.rlib"
out=$(STAGED_TARGET_CAP=100K "$sweep" "$ws" 2>&1) || fail "exit $?: $out"
has "$out" "ok   sweep (staged-target incremental) freed "
has "$out" "ok   sweep (staged-target over 100K) removed "
[ ! -e "$st" ] || fail "the staged target over its cap is still there"
flock -n "$GATES_STAGED_CACHE/staged-tree.lock" true || fail "the sweep kept staged-tree.lock"

# Nothing to sweep: each says so, and the exit is 0.
out=$("$sweep" "$ws" 2>&1) || fail "second run exit $?: $out"
has "$out" "skip  sweep (ratchet-base): none"
has "$out" "skip  sweep (incremental): none"
has "$out" "skip  sweep (staged-target): none"
hasnt "$out" "FAIL"

# A cap that is no size is said, nothing removed, exit 0.
grow
out=$(STAGED_TARGET_CAP=lots "$sweep" "$ws" 2>&1) || fail "exit $?: $out"
has "$out" "FAIL sweep (staged-target): STAGED_TARGET_CAP 'lots' is no size"
[ -e "$st/debug/deps/lib.rlib" ] || fail "the staged target was swept under a bad cap"
echo "ok   land-sweep.selftest"
