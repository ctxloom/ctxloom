#!/usr/bin/env bash
# Sweep stale build artifacts out of the worktree pool's target/ directories
# by age (owner ruling of 2026-11-16): `trim-targets.sh [--dry-run] [--days
# <n>] [<checkout>...]` (`just trim-targets [--dry-run] [--days <n>]
# [<checkout>...]`; no checkout: every slot of the pool, scripts/slot.sh).
# Each branch a slot is switched to leaves the units it alone built (its test
# executables, about 8G a switch); this removes, in each checkout's target/
# (the gates' own trees under it, target/staged and target/ratchet-base,
# included), what no build has used for <n> days (default 2):
#
#   - every unit whose fingerprint (target/**/.fingerprint/<unit>-<hash>) no
#     build has read for <n> days, with everything of that hash in deps/,
#     build/ and the profile directory: cargo-sweep's `sweep --time <n>`, the
#     released tool at the pinned version (TRIM_SWEEP_VERSION, the
#     justfile's cargo_sweep_version; another version, or none, fails, saying
#     the install line). A unit is removed WITH its fingerprint, so cargo
#     rebuilds it when a build next wants it, never trusts a half-removed one;
#   - every rustc incremental directory (target/**/incremental/<crate>-<hash>)
#     in which nothing was written for <n> days (rustc makes a new session
#     directory at every compile of its crate, and an incremental cache it
#     does not find it rebuilds).
#
# "Used" is the file's access time (cargo reads a fingerprint at every build
# that has its unit): on a `relatime` mount (Linux's default; /home here) an
# access time is renewed at most once a day, so <n> is good to about a day; a
# `noatime` mount keeps none, so it FAILS there and removes nothing. A
# checkout whose cargo is building (it holds target/**/.cargo-lock) is kept,
# and said; a checkout without a target/ is skipped. One line each:
#
#   swept   <checkout>/target: <before> -> <after>
#   would sweep <checkout>/target: <before>, about <size> to free
#   kept    <checkout>: building (cargo holds <lock>)
#   skip    <checkout>: no target/
#
# then a summary line. `--dry-run` judges the same and removes nothing.
#
# TRIM_TARGET_DIR names the target directory to sweep instead of
# <checkout>/target (one checkout then); the shared staged target
# (scripts/land-sweep.sh) is swept so, with the checkout's Cargo.toml read for
# the sweep and CARGO_TARGET_DIR pointing at that directory.
#
# Exit: 0; 1 when it cannot sweep (the tool, a noatime mount, a failed sweep);
# 2 on usage. Environment: TRIM_SWEEP_VERSION (required), and for the pool
# SLOT_ROOT, SLOT_PREFIX and SLOT_COUNT (the justfile gives them).
# Self-test: scripts/trim-targets.selftest.sh (`just selftest`).
set -euo pipefail
dry= days=2 checkouts=()
usage() { echo "usage: trim-targets.sh [--dry-run] [--days <n>] [<checkout>...]" >&2; exit 2; }
while [ $# -gt 0 ]; do
    case "$1" in
        --dry-run) dry=1; shift ;;
        --days) [ $# -ge 2 ] || usage; days=$2; shift 2 ;;
        -*) usage ;;
        *) checkouts+=("$1"); shift ;;
    esac
done
case "$days" in ''|*[!0-9]*) usage ;; esac
fail() { echo "FAIL trim-targets: $*" >&2; exit 1; }

want=${TRIM_SWEEP_VERSION:?TRIM_SWEEP_VERSION is the cargo-sweep pinned}
have=$(cargo-sweep --version 2>/dev/null || true)
[ "$have" = "cargo-sweep $want" ] \
    || fail "cargo-sweep $want is needed, found '${have:-none}' (fix: cargo install --locked cargo-sweep@$want)"

if [ ${#checkouts[@]} -eq 0 ]; then
    : "${SLOT_ROOT:?SLOT_ROOT is the directory of the pool}" "${SLOT_PREFIX:?}"
    for n in $(seq "${SLOT_COUNT:-3}"); do checkouts+=("$SLOT_ROOT/$SLOT_PREFIX$n"); done
fi

human() { numfmt --to=iec-i --suffix=B "$1"; }
size_of() { du -sb "$1" 2>/dev/null | cut -f1; }
swept=0 kept=0
for c in "${checkouts[@]}"; do
    t=${TRIM_TARGET_DIR:-$c/target}
    if [ ! -d "$t" ]; then
        echo "skip    $c: no target/"
        continue
    fi
    if findmnt -n -o OPTIONS -T "$t" 2>/dev/null | tr ',' '\n' | grep -qx noatime; then
        fail "$t is on a noatime mount: no access time says what a build used, so nothing is removed"
    fi
    # Hold every cargo build lock under target/ for the sweep (a build that
    # starts meanwhile waits); one held by a running cargo keeps the checkout.
    busy=
    fds=()
    while IFS= read -r -d '' l; do
        exec {fd}<"$l"
        fds+=("$fd")
        flock -n "$fd" || { busy=$l; break; }
    done < <(find "$t" -name .cargo-lock -type f -print0 2>/dev/null)
    if [ -n "$busy" ]; then
        for fd in "${fds[@]}"; do exec {fd}<&-; done
        echo "kept    $c: building (cargo holds $busy)"
        kept=$((kept + 1))
        continue
    fi
    before=$(size_of "$t")
    log=$(mktemp)
    if ! (cd "$c" && CARGO_TARGET_DIR="$t" CARGO_NET_OFFLINE=true cargo-sweep sweep ${dry:+--dry-run} --time "$days" "$c") > "$log" 2>&1; then
        for fd in "${fds[@]}"; do exec {fd}<&-; done
        tail -n 5 "$log" >&2; rm -f "$log"
        fail "cargo-sweep failed in $c"
    fi
    # cargo-sweep's own sum: "[INFO] Cleaned|Would clean: 1.2 GiB from ..."
    sweep_said=$(grep -oE '(Cleaned|Would clean:) [0-9.]+ [KMGT]?i?B' "$log" | sed 's/^Would clean: //; s/^Cleaned //' | paste -sd+ - || true)
    rm -f "$log"
    stale=0
    while IFS= read -r -d '' d; do
        [ -z "$(find "$d" -maxdepth 1 -mtime "-$days" -print -quit)" ] || continue
        stale=$((stale + $(size_of "$d")))
        [ -n "$dry" ] || rm -rf "$d"
    done < <(find "$t" -path '*/incremental/*' -prune -type d -print0 2>/dev/null)
    for fd in "${fds[@]}"; do exec {fd}<&-; done
    if [ -n "$dry" ]; then
        echo "would sweep $c/target: $(human "$before"), about ${sweep_said:-0 B} of units and $(human "$stale") of incremental caches to free"
    else
        echo "swept   $c/target: $(human "$before") -> $(human "$(size_of "$t")")"
    fi
    swept=$((swept + 1))
done
echo "trim-targets: ${dry:+(dry run) would have }swept $swept target/ directories of artifacts unused for $days days, kept $kept"
