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
#   - the shared staged target (<cache>/staged-target, the one `_scratch-tree`
#     builds into), only while <cache>/staged-tree.lock is taken (flock -n;
#     held by a commit gate, it is skipped and said, never waited for): first
#     its rustc incremental directories (staged-target/*/incremental,
#     */*/incremental), then, if it is still larger than the cap
#     (STAGED_TARGET_CAP, default 40G), the whole of it. Not swept by age:
#     every commit in every worktree builds into it, so nothing in it is ever
#     two days unused and an age sweep freed 0B at five landings in a row
#     while it grew to 114G.
#
# Nothing is touched while a cargo holds a .cargo-lock of the target. One
# terse line each (`ok   sweep (<what>) <freed>`, `skip  sweep (<what>): <why>`,
# `FAIL sweep (<what>): <why>`); a failure is said and never changes the exit
# status, which is always 0 (a landing's verdict is not the sweep's).
# <cache> is $GATES_STAGED_CACHE, else ${XDG_CACHE_HOME:-<account home>/.cache}/
# ctxloom/gates, resolved as gates/gates.just's `_scratch-tree` does.
# Environment: STAGED_TARGET_CAP (an IEC size such as 40G; the justfile's
# staged_target_cap passes it). Self-test: scripts/land-sweep.selftest.sh.
set -uo pipefail
here=$(cd "$(dirname "$0")" && pwd)
top=$(cd "${1:-$(git rev-parse --show-toplevel)}" && pwd)
t=$top/target
cap=${STAGED_TARGET_CAP:-40G}
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
    local cache
    cache=${GATES_STAGED_CACHE:-$("$(dirname "${BASH_SOURCE[0]}")/../gates/account-cache.sh")}
    local st=$cache/staged-target d n freed=0 size limit
    [ -d "$st" ] || { echo "skip  sweep (staged-target): none"; return; }
    if ! limit=$(numfmt --from=iec "${cap%B}" 2> /dev/null); then
        echo "FAIL sweep (staged-target): STAGED_TARGET_CAP '$cap' is no size"
        return
    fi
    # `>>`: opening must not truncate a holder's note. The lock is held for
    # the sweep, so a commit gate meanwhile waits and says so.
    exec 9>> "$cache/staged-tree.lock"
    if ! flock -n 9; then
        echo "skip  sweep (staged-target): a commit gate holds $cache/staged-tree.lock"
        exec 9>&-
        return
    fi
    while IFS= read -r -d '' d; do
        n=$(size_of "$d")
        if rm -rf "$d"; then freed=$((freed + ${n:-0}))
        else echo "FAIL sweep (staged-target incremental): could not remove $d"; fi
    done < <(find "$st" -maxdepth 3 -name incremental -type d -print0 2> /dev/null)
    echo "ok   sweep (staged-target incremental) freed $(human "$freed")"
    size=$(size_of "$st")
    if [ "${size:-0}" -gt "$limit" ]; then
        if rm -rf "$st"; then echo "ok   sweep (staged-target over $cap) removed $(human "$size")"
        else echo "FAIL sweep (staged-target over $cap): could not remove $st"; fi
    fi
    exec 9>&-
}

sweep_ratchet_base
sweep_incremental
sweep_staged
exit 0
