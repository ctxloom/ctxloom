#!/usr/bin/env bash
# Run a command under a memory cap: `memory-cap.sh <label> <memory_max>
# <swap_max> <command>...`, or `memory-cap.sh <label> <memory_max> <swap_max>
# --check` to say only whether the cap can be had.
#
# Its callers: every cargo-mutants run (`mutants_memory_max`) and every
# test gate's run (`_tests`, so `test`, `test-pkg`, `test-staged` and the
# test leg of `closeout`, `closeout-ratchet` and `land-ratchet`:
# `tests_memory_max`, owner ruling of 2026-11-16). A cap bounds one run;
# gates/tests-lock.sh keeps two whole-workspace runs from overlapping.
#
# Why (2026-11-15): a cargo-mutants mutant whose test grew without bound
# filled all RAM and swap twice; the kernel's global OOM killer took the
# test, and systemd then stopped the whole user scope it ran under (an
# editor's terminals, every session in them). Capped, the runaway meets the
# cap's own OOM, which kills the largest process in the cap alone.
#
# The command runs in a transient systemd user scope (`systemd-run --user
# --scope`): MemoryMax=<memory_max>, MemorySwapMax=<swap_max> and
# OOMPolicy=continue. That last is the point: with the default (stop) the
# scope's OOM stops every process in it, the whole mutation run; with
# continue only the process the kernel picks (the runaway test) is killed,
# its test fails, cargo-mutants counts the mutant caught and goes on (and
# a nextest run reports that test failed and runs the rest).
# Not guaranteed: that ONLY the runaway dies. The kernel's cgroup OOM can take
# a second process of the scope if one charges memory before the first
# victim's is released (GitHub's ubuntu-24.04 runner, kernel 7.0: a
# `head | tail` runaway took the wrapper too, 21 runs in 60; none in 40 on
# 6.12). The run then ends 137, a failure and never a silent pass.
# `--collect` lets a scope that ended failed (an OOM kill) go, so its name
# is free again. Inside the scope the command is started only once the
# scope's memory.max is seen set (a manager without the memory controller
# delegated would take the property and enforce nothing). When the command
# ends, the scope's memory peak (cgroup v2's memory.peak) is said, and how
# many processes its OOM killer killed (memory.events' oom_kill):
#
#   <label>: memory peak 3.2G (cap MemoryMax=16G MemorySwapMax=2G)[; the cap's
#   OOM killer killed 1 process(es) of the run]
#
# A shell with no XDG_RUNTIME_DIR (an agent's) gets /run/user/<uid> when its
# bus is there. With no user bus to reach, or a scope that cannot be made,
# it FAILS, saying why and the fix; it never runs the command uncapped
# unasked. The opt-out is <memory_max> `none`: the command runs uncapped,
# and the run says so. The variable that opts out is the caller's, named
# in what is said (MEMORY_CAP_OPT_OUT, default MUTANTS_MEMORY_MAX; the
# test gates' is TESTS_MEMORY_MAX).
#
# Per-mutant reap (owner ruling 2026-11-25): with MEMORY_CAP_REAP=<seconds>
# in the environment (the mutation recipes set it; cargo-mutants has no
# per-mutant hook, so this is a watcher in the scope), a watcher polls the
# scope's cgroup.procs every <seconds> and kills every process of the scope
# that is not the command's own process tree (descendants of the command,
# by parent pid) nor this wrapper or the watcher. A detached leftover
# (setsid, double fork) of a finished mutant's test is orphaned, so it is no
# descendant, and goes; one seen orphaned is killed on its SECOND consecutive
# look (a grace for a process whose parent is between fork and exit). Each
# is logged to stderr as `<label>: reaped pid <pid> (<comm>): <cmdline head>`;
# a last sweep runs when the command ends. The command's exit is unchanged.
# 2026-11-25: 1,866 copies of a test harness, spawned detached by one mutant,
# filled the 8G cap and its OOM killer killed cargo-mutants itself.
#
# Exit: the command's own; 1 when the cap cannot be had; 2 on usage.
# Environment: MEMORY_CAP_RUN_USER, the directory holding the per-user
# runtime directories (default /run/user; the self-test's seam), and
# MEMORY_CAP_OPT_OUT, the opt-out's variable.
# Self-test: scripts/memory-cap.selftest.sh (`just selftest`).
set -euo pipefail
opt_out=${MEMORY_CAP_OPT_OUT:-MUTANTS_MEMORY_MAX}

if [ "${1:-}" = --inside ]; then
    # In the scope: refuse to run unless the cap took; then run the command
    # (a child, so its peak can be read once it is over) and say the peak.
    shift
    label=$1 memory_max=$2 swap_max=$3
    shift 3
    cg=/sys/fs/cgroup$(sed -n 's/^0:://p' /proc/self/cgroup 2>/dev/null || true)
    max=$(cat "$cg/memory.max" 2>/dev/null || true)
    case "$max" in
        '' | max)
            echo "FAIL $label: the memory cap did not take: the scope's memory.max is '${max:-unreadable}' ($cg)" >&2
            echo "  fix: delegate the memory controller to the user manager (systemd's user@.service Delegate=), or opt out, stating it: $opt_out=none" >&2
            exit 1 ;;
    esac
    rc=0
    if [ -n "${MEMORY_CAP_REAP:-}" ]; then
        inside=$$
        reap_pass() {
            local mode=${1:-} pid line rest ppid p changed keep=" $inside $BASHPID $cmd " seen_now=" "
            local -A par=()
            for pid in $(cat "$cg/cgroup.procs" 2>/dev/null); do
                line=$(cat "/proc/$pid/stat" 2>/dev/null) || continue
                rest=${line##*) }
                # shellcheck disable=SC2086
                set -- $rest
                par[$pid]=$2
            done
            changed=1
            while [ "$changed" = 1 ]; do
                changed=0
                for pid in "${!par[@]}"; do
                    case "$keep" in *" $pid "*) continue ;; esac
                    ppid=${par[$pid]}
                    case "$keep" in *" $ppid "*) keep+="$pid "; changed=1 ;; esac
                done
            done
            for pid in "${!par[@]}"; do
                case "$keep" in *" $pid "*) continue ;; esac
                seen_now+="$pid "
                if [ "$mode" != final ]; then
                    case "$seen" in *" $pid "*) ;; *) continue ;; esac
                fi
                p=$(tr '\0' ' ' < "/proc/$pid/cmdline" 2>/dev/null | cut -c1-120)
                echo "$label: reaped pid $pid ($(cat "/proc/$pid/comm" 2>/dev/null)): $p" >&2
                kill -KILL "$pid" 2>/dev/null || true
            done
            seen=$seen_now
        }
        seen=" "
        "$@" &
        cmd=$!
        (
            while kill -0 "$cmd" 2>/dev/null; do
                reap_pass
                sleep "$MEMORY_CAP_REAP"
            done
        ) &
        watcher=$!
        wait "$cmd" || rc=$?
        kill "$watcher" 2>/dev/null || true
        wait "$watcher" 2>/dev/null || true
        reap_pass final
    else
        "$@" || rc=$?
    fi
    peak=$(cat "$cg/memory.peak" 2>/dev/null || true)
    [ -z "$peak" ] || peak=$(numfmt --to=iec "$peak" 2>/dev/null || echo "$peak bytes")
    kills=$(sed -n 's/^oom_kill //p' "$cg/memory.events" 2>/dev/null || true)
    oom=
    [ "${kills:-0}" -eq 0 ] || oom="; the cap's OOM killer killed $kills process(es) of the run"
    echo "$label: memory peak ${peak:-unreadable} (cap MemoryMax=$memory_max MemorySwapMax=$swap_max)$oom"
    exit "$rc"
fi

[ $# -ge 4 ] || { echo "usage: memory-cap.sh <label> <memory_max|none> <swap_max> <command>... | --check" >&2; exit 2; }
label=$1 memory_max=$2 swap_max=$3
shift 3

if [ "$memory_max" = none ]; then
    echo "$label: memory cap: none ($opt_out=none): this run is UNCAPPED; a runaway test can take the whole machine's memory"
    [ "$1" = --check ] && exit 0
    exec "$@"
fi

unable() {
    echo "FAIL $label: no memory cap: $1" >&2
    echo "  fix: $2" >&2
    exit 1
}
fix_bus="run it from a login session (or enable lingering: loginctl enable-linger $(id -un)), or opt out, stating it: $opt_out=none"
command -v systemd-run >/dev/null 2>&1 || unable "systemd-run is not on PATH, and the cap is a systemd user scope" "$fix_bus"
if [ -z "${XDG_RUNTIME_DIR:-}" ] && [ -z "${DBUS_SESSION_BUS_ADDRESS:-}" ]; then
    run_user=${MEMORY_CAP_RUN_USER:-/run/user}/$(id -u)
    [ -S "$run_user/bus" ] || unable "no systemd user bus: XDG_RUNTIME_DIR and DBUS_SESSION_BUS_ADDRESS are unset and there is no $run_user/bus" "$fix_bus"
    export XDG_RUNTIME_DIR=$run_user
fi

unit=memory-cap-$$-$RANDOM
caps=(-p "MemoryMax=$memory_max" -p "MemorySwapMax=$swap_max" -p OOMPolicy=continue)
if [ "$1" = --check ]; then
    err=$(systemd-run --user --scope --quiet --collect --unit "$unit" "${caps[@]}" -- "$0" --inside "$label" "$memory_max" "$swap_max" true 2>&1) \
        || unable "a capped systemd user scope could not be run: $(head -n 3 <<< "$err")" "$fix_bus"
    exit 0
fi

exec systemd-run --user --scope --quiet --collect --unit "$unit" "${caps[@]}" -- "$0" --inside "$label" "$memory_max" "$swap_max" "$@"
