#!/usr/bin/env bash
# Sweep the build caches a landing grew (owner ruling 2026-11-24: /home filled
# twice in two days; "sweep after every landing"): `land-sweep.sh [<checkout>]`
# (default: the checkout it runs in), run by scripts/closeout-ratchet.sh at
# its end, pass or fail. It removes what a later build does not need:
#
#   - <checkout>/target/ratchet-base, the ratchet base's build: each landing
#     builds a base tree of a new sha with CARGO_INCREMENTAL=0, so it is never
#     reused. The cached lists and re-confirmations in target/ratchet/ (small,
#     reused by sha) are NOT touched. Skipped while ratchet-base.sh runs
#     (target/.ratchet-base.lock held);
#   - every rustc incremental directory of the checkout's target/
#     (target/*/incremental, target/*/*/incremental), which grows without
#     bound and is rebuilt on the next compile of its crate;
#   - the shared staged target (<cache>/staged-target, the one
#     `_scratch-tree` builds into), by AGE (trim-targets.sh: cargo-sweep and
#     stale incremental directories, LAND_SWEEP_DAYS, default 2), and only
#     while <cache>/staged-tree.lock is taken (flock -n); held by a commit
#     gate, it is skipped and said. It is swept by age rather than removed
#     whole: every commit gate builds its staged tree there, and a whole
#     removal makes the next commit rebuild every dependency; by age the
#     units the current dependency set still uses stay warm.
#
# Nothing is touched while a cargo holds a .cargo-lock of the target. One
# terse line each (`ok   sweep (<what>) <freed>`, `skip  sweep (<what>): <why>`,
# `FAIL sweep (<what>): <why>`); a failure is said and never changes the exit
# status, which is always 0 (a landing's verdict is not the sweep's).
# <cache> is $GATES_STAGED_CACHE, else ${XDG_CACHE_HOME:-<account home>/.cache}/
# ctxloom/gates, resolved as gates/gates.just's `_scratch-tree` does.
# Environment: TRIM_SWEEP_VERSION (the pinned cargo-sweep; default 0.8.0),
# LAND_SWEEP_DAYS. Self-test: scripts/land-sweep.selftest.sh.
set -uo pipefail
here=$(cd "$(dirname "$0")" && pwd)
top=$(cd "${1:-$(git rev-parse --show-toplevel)}" && pwd)
t=$top/target
days=${LAND_SWEEP_DAYS:-2}
export TRIM_SWEEP_VERSION=${TRIM_SWEEP_VERSION:-0.8.0}
size_of() { du -sb "$1" 2>/dev/null | cut -f1; }
human() { numfmt --to=iec-i --suffix=B "${1:-0}"; }

# True when a cargo holds a build lock under $1.
building() {
    local l
    while IFS= read -r -d '' l; do
        flock -n "$l" true 2>/dev/null || return 0
    done < <(find "$1" -name .cargo-lock -type f -print0 2>/dev/null)
    return 1
}

sweep_ratchet_base() {
    local what="$t/ratchet-base" before
    [ -d "$what" ] || { echo "skip  sweep (ratchet-base): none"; return; }
    exec 7> "$t/.ratchet-base.lock"
    if ! flock -n 7; then
        echo "skip  sweep (ratchet-base): ratchet-base.sh holds its lock"
        exec 7>&-
        return
    fi
    if building "$what"; then
        echo "skip  sweep (ratchet-base): cargo holds a build lock"
    else
        before=$(size_of "$what")
        if rm -rf "$what"; then echo "ok   sweep (ratchet-base) freed $(human "$before")"
        else echo "FAIL sweep (ratchet-base): could not remove $what"; fi
    fi
    exec 7>&-
}

sweep_incremental() {
    local dirs=() d freed=0 n
    [ -d "$t" ] || { echo "skip  sweep (incremental): no target/"; return; }
    while IFS= read -r -d '' d; do dirs+=("$d"); done \
        < <(find "$t" -maxdepth 3 -name incremental -type d -not -path "$t/ratchet-base/*" -print0 2>/dev/null)
    [ ${#dirs[@]} -gt 0 ] || { echo "skip  sweep (incremental): none"; return; }
    if building "$t"; then
        echo "skip  sweep (incremental): cargo holds a build lock"
        return
    fi
    for d in "${dirs[@]}"; do
        n=$(size_of "$d")
        if rm -rf "$d"; then freed=$((freed + ${n:-0}))
        else echo "FAIL sweep (incremental): could not remove $d"; return; fi
    done
    echo "ok   sweep (incremental, ${#dirs[@]} dirs) freed $(human "$freed")"
}

sweep_staged() {
    local cache account
    cache=${GATES_STAGED_CACHE:-}
    if [ -z "$cache" ]; then
        cache=${XDG_CACHE_HOME:-}
        if [ -z "$cache" ]; then
            account=$(getent passwd "$(id -u)" 2> /dev/null | cut -d: -f6 || true)
            [ -n "$account" ] || account=$(eval echo "~$(id -un)")
            cache=${account:-$HOME}/.cache
        fi
        cache=$cache/ctxloom/gates
    fi
    local st=$cache/staged-target before out
    [ -d "$st" ] || { echo "skip  sweep (staged-target): none"; return; }
    # `>>`: opening must not truncate a holder's note. The lock is held for
    # the sweep, so a commit gate meanwhile waits and says so.
    exec 9>> "$cache/staged-tree.lock"
    if ! flock -n 9; then
        echo "skip  sweep (staged-target): a commit gate holds $cache/staged-tree.lock"
        exec 9>&-
        return
    fi
    before=$(size_of "$st")
    if out=$(TRIM_TARGET_DIR=$st "$here/trim-targets.sh" --days "$days" "$top" 2>&1); then
        if grep -q '^kept ' <<< "$out"; then
            echo "skip  sweep (staged-target): cargo holds a build lock"
        else
            echo "ok   sweep (staged-target, unused $days days) freed $(human $((before - $(size_of "$st"))))"
        fi
    else
        echo "FAIL sweep (staged-target): $(tail -n 1 <<< "$out")"
    fi
    exec 9>&-
}

sweep_ratchet_base
sweep_incremental
sweep_staged
exit 0
