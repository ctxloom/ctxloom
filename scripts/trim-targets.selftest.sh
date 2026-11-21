#!/usr/bin/env bash
# Self-test of scripts/trim-targets.sh (or $TRIM_TARGETS) against a fixture
# cargo workspace of two crates, `old` and `fresh`, built: `old`'s
# fingerprints are aged 10 days (their access time) and its incremental cache
# too (its write time), `fresh`'s are left as built. A dry run says what it
# would free and removes nothing; the real run removes `old`'s units and
# incremental cache alone, and the next build rebuilds `old` and not `fresh`
# (the fingerprints still agree with what is left). A checkout whose cargo
# build lock is held is kept and said; one without a target/ is skipped; the
# pool's slots are the default checkouts; another cargo-sweep version, and
# usage, are refused. Needs cargo and the pinned cargo-sweep on PATH
# (TRIM_SWEEP_VERSION, the justfile's cargo_sweep_version; default 0.8.0).
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
trim=${TRIM_TARGETS:-$here/trim-targets.sh}
export TRIM_SWEEP_VERSION=${TRIM_SWEEP_VERSION:-0.8.0}
tmp=$(mktemp -d)
holder=
cleanup() { [ -z "$holder" ] || kill "$holder" 2>/dev/null || true; rm -rf "$tmp"; }
trap cleanup EXIT
fail() { echo "FAIL trim-targets.selftest: $*" >&2; exit 1; }
unset CARGO_TARGET_DIR CARGO_BUILD_TARGET_DIR RUSTC_WRAPPER
has() { grep -qF -- "$2" <<< "$1" || fail "no line '$2' in: $1"; }

ws=$tmp/ws
mkdir -p "$ws/old/src" "$ws/fresh/src" "$tmp/notarget"
printf '[workspace]\nmembers = ["old", "fresh"]\nresolver = "2"\n' > "$ws/Cargo.toml"
for c in old fresh; do
    printf '[package]\nname = "%s"\nversion = "0.1.0"\nedition = "2021"\n' "$c" > "$ws/$c/Cargo.toml"
    printf 'pub fn %s() -> u32 { 1 }\n#[test]\nfn it() { assert_eq!(%s(), 1); }\n' "$c" "$c" > "$ws/$c/src/lib.rs"
done
build() { (cd "$ws" && CARGO_INCREMENTAL=1 cargo build -q --offline --lib --tests 2>&1); }
build > /dev/null || fail "the fixture does not build"
# `old` unused for 10 days: its fingerprints' access time, its incremental
# cache's write time.
find "$ws/target/debug/.fingerprint" -path '*/old-*' -exec touch -a -d '10 days ago' {} +
find "$ws/target/debug/incremental" -path '*/old-*' -exec touch -m -d '10 days ago' {} +
units() { find "$ws/target/debug/deps" "$ws/target/debug/.fingerprint" "$ws/target/debug/incremental" -mindepth 1 -maxdepth 1 -printf '%f\n' | sort; }
ls -A "$ws/target/debug/incremental" | grep -q '^old-' || fail "the fixture made no incremental cache for old"
all=$(units)

# A dry run says what it would free and removes nothing.
out=$("$trim" --dry-run "$ws" "$tmp/notarget" 2>&1) || fail "dry run: exit $?: $out"
has "$out" "would sweep $ws/target: "
has "$out" "skip    $tmp/notarget: no target/"
has "$out" "trim-targets: (dry run) would have swept 1 target/ directories of artifacts unused for 2 days, kept 0"
[ "$(units)" = "$all" ] || fail "the dry run removed something"

# The real run removes old's units and cache alone.
out=$("$trim" "$ws" 2>&1) || fail "exit $?: $out"
has "$out" "swept   $ws/target: "
has "$out" "trim-targets: swept 1 target/ directories of artifacts unused for 2 days, kept 0"
left=$(units)
grep -q '^old-' <<< "$left" && fail "old's artifacts are still there: $left"
grep -q '^libold-' <<< "$left" && fail "old's rlib is still there: $left"
grep -q '^fresh-' <<< "$left" || fail "fresh's artifacts were removed: $left"
grep -q '^libfresh-' <<< "$left" || fail "fresh's rlib was removed: $left"
# The next build rebuilds old and only old.
out=$(cd "$ws" && CARGO_INCREMENTAL=1 cargo build --offline --lib --tests 2>&1) || fail "the rebuild failed: $out"
has "$out" "Compiling old v0.1.0"
grep -q "Compiling fresh" <<< "$out" && fail "fresh was rebuilt: $out"

# A checkout whose cargo is building is kept.
flock "$ws/target/debug/.cargo-lock" sleep 60 < /dev/null > /dev/null 2>&1 &
holder=$!
for _ in $(seq 50); do flock -n "$ws/target/debug/.cargo-lock" true || break; sleep 0.1; done
find "$ws/target/debug/.fingerprint" -path '*/fresh-*' -exec touch -a -d '10 days ago' {} +
out=$("$trim" "$ws" 2>&1) || fail "while building: exit $?: $out"
has "$out" "kept    $ws: building (cargo holds $ws/target/debug/.cargo-lock)"
has "$out" "swept 0 target/ directories of artifacts unused for 2 days, kept 1"
grep -q '^fresh-' <<< "$(units)" || fail "a building checkout was swept"
kill "$holder"; wait "$holder" 2>/dev/null || true; holder=

# The pool's slots are the default checkouts.
mkdir -p "$tmp/pool/p--slot-2"
out=$(SLOT_ROOT=$tmp/pool SLOT_PREFIX=p--slot- SLOT_COUNT=2 "$trim" --days 5 2>&1) || fail "the pool: exit $?: $out"
has "$out" "skip    $tmp/pool/p--slot-1: no target/"
has "$out" "skip    $tmp/pool/p--slot-2: no target/"
has "$out" "unused for 5 days"

# Another cargo-sweep version, and usage: refused, nothing removed.
rc=0
out=$(TRIM_SWEEP_VERSION=0.0.1 "$trim" "$ws" 2>&1) || rc=$?
[ "$rc" -eq 1 ] || fail "another version: exit $rc, not 1: $out"
has "$out" "FAIL trim-targets: cargo-sweep 0.0.1 is needed, found 'cargo-sweep $TRIM_SWEEP_VERSION' (fix: cargo install --locked cargo-sweep@0.0.1)"
rc=0
"$trim" --days x > /dev/null 2>&1 || rc=$?
[ "$rc" -eq 2 ] || fail "usage: exit $rc, not 2"
grep -q '^fresh-' <<< "$(units)" || fail "a refused run removed something"

echo "ok   trim-targets.selftest (aged units and caches swept, fresh kept and not rebuilt, dry run removes nothing, building kept, pool default, version pinned)"
