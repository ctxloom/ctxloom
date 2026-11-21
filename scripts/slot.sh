#!/usr/bin/env bash
# The worktree pool (owner ruling of 2026-11-16): a few long-lived checkouts
# of this repository, each with a warm target/, that an agent LEASES, switches
# to its task's branch, works in, and RELEASES. A switch rebuilds only what
# the branch changed (measured: 35-102 s against 133 s cold, CLAUDE.md).
#
#   slot.sh lease <branch> [--from <base>] [--wait <secs>] [--by <who>] [--holder <pid>]
#   slot.sh release [<slot>] [--allow-unpushed] [--no-sweep]
#   slot.sh list
#
# The slots are <root>/<prefix><n>, n = 1..<count> (ctxloom's justfile:
# ~/workspace/worktrees/ctxloom--slot-1..3 of the user's own home). One is
# made, `git worktree add`, on its first lease.
#
# lease: takes the first slot that is free and clean (an existing one before
# one not yet made), switches it to <branch> (made from <base> with --from
# when no such branch exists; a branch only on origin is tracked, as `git
# switch` does), and prints the slot's path on stdout, alone (everything else
# is on stderr), so `cd "$(just slot lease my/branch --from origin/main)"`.
# A free slot holding uncommitted work (a crashed holder's), or a detached
# HEAD no ref holds, is never taken: it is said and passed over. All slots
# leased: each is said with its holder, and the lease refused, or with
# --wait retried every 2 s until <secs> have passed.
#
# The lease is an advisory flock(2) (flock(1), the semantics of the platform
# crate `file-lock` on unix: taken at once or not at all, gone with its
# holder) on <git common dir>/ctxloom-slots/slot-<n>.lock, held by a small
# holder process of its own session (setsid, stdio closed) that lives as long
# as the lease's OWNER: the first ancestor of this script that is not a
# shell or `just` (an agent's: its Claude Code process), or --holder <pid>.
# When the owner exits (or crashes) the holder exits and the lock is free, so
# a crashed agent's slot frees itself; its work, if any, stays in the slot,
# which is then passed over until someone cleans it. The owner is checked by
# pid AND start time (a reused pid is not the owner). slot-<n>.lease beside
# the lock says who holds it (owner, --by, branch, since); a lease file whose
# lock is free is a past lease, said as such.
#
# release: the slot is the one given (`slot-2`, `2` or its path), else the
# one holding the directory `just` was run from (SLOT_FROM). Refused while
# it holds a tracked change or an untracked file, or while its HEAD is in no
# remote-tracking ref (not pushed) unless --allow-unpushed. Then its HEAD is
# detached (the branch is free for any other checkout; the files and
# target/ stay warm), the holder stopped, and the slot's target/ swept of
# artifacts not used for days (scripts/trim-targets.sh) unless --no-sweep.
#
# list: one line per slot: leased (by whom, what branch, since when), free
# (at what, clean or not, and the last lease), or not made yet.
#
# Exit: 0; 1 refused or failed (said on stderr, `slot: ...`); 2 usage.
# Environment: SLOT_REPO (default: the repository holding this script),
# SLOT_ROOT, SLOT_PREFIX, SLOT_COUNT (the justfile gives them), SLOT_FROM.
# Self-test: scripts/slot.selftest.sh (`just selftest`).
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
repo=${SLOT_REPO:-$here/..}
root=${SLOT_ROOT:?SLOT_ROOT is the directory holding the slots}
prefix=${SLOT_PREFIX:?SLOT_PREFIX is the slots name before its number}
count=${SLOT_COUNT:-3}
while IFS= read -r v; do unset "$v"; done < <(git rev-parse --local-env-vars)

usage() {
    echo "usage: slot.sh lease <branch> [--from <base>] [--wait <secs>] [--by <who>] [--holder <pid>]" >&2
    echo "       slot.sh release [<slot>] [--allow-unpushed] [--no-sweep]" >&2
    echo "       slot.sh list" >&2
    exit 2
}
die() { echo "slot: $*" >&2; exit 1; }
say() { echo "slot: $*" >&2; }

common=$(git -C "$repo" rev-parse --path-format=absolute --git-common-dir)
locks=$common/ctxloom-slots
mkdir -p "$locks"

path_of() { echo "$root/$prefix$1"; }
lock_of() { echo "$locks/slot-$1.lock"; }
lease_of() { echo "$locks/slot-$1.lease"; }
# A process's start time (field 22 of /proc/<pid>/stat), empty when gone.
start_of() { awk '{ sub(/^.*\) /, ""); print $20 }' "/proc/$1/stat" 2>/dev/null || true; }
field() { sed -n "s/^$2=//p" "$1" 2>/dev/null | head -n 1; }

# Whether slot $1's lock is held (by a live holder).
held() {
    local l
    l=$(lock_of "$1")
    [ -e "$l" ] || return 1
    ! flock -n "$l" true 2>/dev/null
}

# Slot $1's worktree state: absent | foreign | clean | dirty:<n> | orphan
state_of() {
    local p n
    p=$(path_of "$1")
    [ -e "$p" ] || { echo absent; return; }
    [ -e "$p/.git" ] && [ "$(git -C "$p" rev-parse --path-format=absolute --git-common-dir 2>/dev/null)" = "$common" ] \
        || { echo foreign; return; }
    n=$(git -C "$p" status --porcelain=v1 | grep -c '' || true)
    [ "$n" -eq 0 ] || { echo "dirty:$n"; return; }
    if ! git -C "$p" symbolic-ref -q HEAD > /dev/null \
        && [ -z "$(git -C "$p" for-each-ref --count=1 --contains HEAD)" ]; then
        echo orphan; return
    fi
    echo clean
}

where_of() {
    local p b
    p=$(path_of "$1")
    if b=$(git -C "$p" symbolic-ref -q --short HEAD); then echo "on $b"
    else echo "detached at $(git -C "$p" rev-parse --short HEAD)"; fi
}

who_of() {
    local f
    f=$(lease_of "$1")
    echo "$(field "$f" by) (owner pid $(field "$f" owner_pid) $(field "$f" owner_comm)), branch $(field "$f" branch), since $(field "$f" since)"
}

# The lease's owner: the first ancestor not a shell, `just` or a wrapper.
owner_pid() {
    local p=$PPID c
    while [ "$p" -gt 1 ]; do
        c=$(cat "/proc/$p/comm" 2>/dev/null) || break
        case "$c" in
            sh|bash|zsh|dash|ksh|fish|just|env|timeout|flock|setsid|nice|systemd-run) ;;
            *) echo "$p"; return ;;
        esac
        p=$(awk '{ sub(/^.*\) /, ""); print $2 }' "/proc/$p/stat")
    done
    echo $PPID
}

# Take slot $1's lock in a holder that lives while pid $2 (started $3) does.
# Answers the holder's pid, or fails when the lock is taken.
take() {
    local lock status hpid
    lock=$(lock_of "$1")
    status=$(mktemp "$locks/.take.XXXXXX")
    # shellcheck disable=SC2016
    setsid bash -c '
        # Whether pid $1 is running and started at $2 (a reused pid is not it).
        alive() {
            local s a
            s=$(cat "/proc/$1/stat" 2>/dev/null) || return 1
            s=${s##*) }
            read -ra a <<< "$s"
            [ "${a[19]}" = "$2" ]
        }
        cd /
        exec 9>>"$1"
        flock -w 2 9 || exit 3
        echo "$BASHPID" > "$4"
        while alive "$2" "$3"; do sleep 2 9>&-; done
    ' slot-holder "$lock" "$2" "$3" "$status" < /dev/null > /dev/null 2>&1 &
    hpid=$!
    for _ in $(seq 50); do
        [ -s "$status" ] && break
        kill -0 "$hpid" 2>/dev/null || break
        sleep 0.1
    done
    if [ -s "$status" ]; then
        cat "$status"; rm -f "$status"; return 0
    fi
    rm -f "$status"
    return 1
}

# Stop slot $1's holder (pid $2, else its lease file's), and wait (bounded)
# until its lock is free.
stop_holder() {
    local h=${2:-}
    [ -n "$h" ] || h=$(field "$(lease_of "$1")" holder_pid)
    [ -z "$h" ] || kill "$h" 2>/dev/null || true
    for _ in $(seq 50); do held "$1" || return 0; sleep 0.1; done
    die "slot-$1: its lock is still held after its holder (pid ${h:-?}) was stopped"
}

slot_of_path() {
    local d top n p
    d=$1
    top=$(git -C "$d" rev-parse --show-toplevel 2>/dev/null) || return 1
    for n in $(seq "$count"); do
        p=$(path_of "$n")
        [ -e "$p" ] && [ "$(cd "$p" && pwd -P)" = "$(cd "$top" && pwd -P)" ] && { echo "$n"; return 0; }
    done
    return 1
}

lease() {
    local branch= from= wait=0 by= owner= start n s deadline hpid p
    while [ $# -gt 0 ]; do
        case "$1" in
            --from) [ $# -ge 2 ] || usage; from=$2; shift 2 ;;
            --wait) [ $# -ge 2 ] || usage; wait=$2; shift 2 ;;
            --by) [ $# -ge 2 ] || usage; by=$2; shift 2 ;;
            --holder) [ $# -ge 2 ] || usage; owner=$2; shift 2 ;;
            -*) usage ;;
            *) [ -z "$branch" ] || usage; branch=$1; shift ;;
        esac
    done
    [ -n "$branch" ] || usage
    case "$wait" in ''|*[!0-9]*) usage ;; esac
    git check-ref-format --branch "$branch" > /dev/null 2>&1 || die "'$branch' is not a branch name"
    [ -n "$owner" ] || owner=$(owner_pid)
    start=$(start_of "$owner")
    [ -n "$start" ] || die "the lease's owner, pid $owner, is not running"
    [ -n "$by" ] || by=${CTXLOOM_SLOT_BY:-$(cat "/proc/$owner/comm")}
    if ! git -C "$repo" rev-parse --verify --quiet "refs/heads/$branch" > /dev/null \
        && ! git -C "$repo" rev-parse --verify --quiet "refs/remotes/origin/$branch" > /dev/null; then
        [ -n "$from" ] || die "no branch '$branch' here or on origin; give --from <base> to make it"
        git -C "$repo" rev-parse --verify --quiet "$from^{commit}" > /dev/null || die "--from '$from' is not a commit (fetch it?)"
    fi
    deadline=$(( $(date +%s) + wait ))
    local told=
    while :; do
        # An existing slot before one not yet made, each in number order.
        for pass in existing absent; do
            for n in $(seq "$count"); do
                p=$(path_of "$n")
                case "$pass" in existing) [ -e "$p" ] || continue ;; absent) [ ! -e "$p" ] || continue ;; esac
                held "$n" && continue
                s=$(state_of "$n")
                case "$s" in
                    dirty:*) [ -n "$told" ] || say "slot-$n passed over: free, but holds uncommitted work (${s#dirty:} files) at $(path_of "$n")"; continue ;;
                    orphan) [ -n "$told" ] || say "slot-$n passed over: free, but its detached HEAD is in no ref (commit it to a branch) at $(path_of "$n")"; continue ;;
                    foreign) [ -n "$told" ] || say "slot-$n passed over: $(path_of "$n") exists and is not a worktree of this repository"; continue ;;
                esac
                hpid=$(take "$n" "$owner" "$start") || continue
                # Taken: is it still clean (another may have worked in it between)?
                s=$(state_of "$n")
                case "$s" in clean|absent) ;; *) stop_holder "$n" "$hpid"; continue ;; esac
                if ! switch_to "$n" "$branch" "$from"; then
                    stop_holder "$n" "$hpid"
                    die "slot-$n: could not switch $p to '$branch' (git said why, above); the lease is let go"
                fi
                {
                    echo "holder_pid=$hpid"
                    echo "owner_pid=$owner"
                    echo "owner_start=$start"
                    echo "owner_comm=$(cat "/proc/$owner/comm")"
                    echo "by=$by"
                    echo "branch=$branch"
                    echo "since=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
                } > "$(lease_of "$n")"
                say "slot-$n leased by $by (owner pid $owner), on $branch"
                echo "$p"
                return 0
            done
        done
        told=1
        if [ "$(date +%s)" -ge "$deadline" ]; then
            say "no slot free of $count:"
            for n in $(seq "$count"); do
                if held "$n"; then say "  slot-$n leased by $(who_of "$n")"
                else say "  slot-$n free but $(state_of "$n") at $(path_of "$n")"; fi
            done
            if [ "$wait" -gt 0 ]; then die "waited ${wait}s; none came free"; fi
            die "every slot is leased or unusable; --wait <secs> waits for one"
        fi
        sleep 2
    done
}

# Put slot $1 (held) on branch $2, made from $3 when new.
switch_to() {
    local p
    p=$(path_of "$1")
    mkdir -p "$root"
    if [ ! -e "$p" ]; then
        if git -C "$repo" rev-parse --verify --quiet "refs/heads/$2" > /dev/null; then
            git -C "$repo" worktree add -q "$p" "$2" >&2
        elif git -C "$repo" rev-parse --verify --quiet "refs/remotes/origin/$2" > /dev/null; then
            git -C "$repo" worktree add -q --track -b "$2" "$p" "origin/$2" >&2
        else
            git -C "$repo" worktree add -q --no-track -b "$2" "$p" "$3" >&2
        fi
    elif git -C "$p" rev-parse --verify --quiet "refs/heads/$2" > /dev/null \
        || git -C "$p" rev-parse --verify --quiet "refs/remotes/origin/$2" > /dev/null; then
        git -C "$p" switch -q "$2" >&2
    else
        git -C "$p" switch -q --no-track -c "$2" "$3" >&2
    fi
}

release() {
    local slot= unpushed= sweep=1 n p s
    while [ $# -gt 0 ]; do
        case "$1" in
            --allow-unpushed) unpushed=1; shift ;;
            --no-sweep) sweep=; shift ;;
            -*) usage ;;
            *) [ -z "$slot" ] || usage; slot=$1; shift ;;
        esac
    done
    if [ -z "$slot" ]; then
        n=$(slot_of_path "${SLOT_FROM:-$PWD}") || die "${SLOT_FROM:-$PWD} is in no slot; name one (slot-<n>, <n> or its path)"
    else
        case "$slot" in
            slot-[0-9]*) n=${slot#slot-} ;;
            [0-9]*) n=$slot ;;
            *) n=$(slot_of_path "$slot") || die "$slot is no slot" ;;
        esac
        [ "$n" -ge 1 ] && [ "$n" -le "$count" ] 2>/dev/null || die "there is no slot-$n (1 to $count)"
    fi
    p=$(path_of "$n")
    held "$n" || die "slot-$n is not leased"
    s=$(state_of "$n")
    case "$s" in
        dirty:*) die "slot-$n holds uncommitted work (${s#dirty:} files: commit, or remove what is not work), so it is not released: $(git -C "$p" status --short | head -n 5 | tr '\n' ' ')" ;;
        orphan) die "slot-$n: its detached HEAD is in no ref; commit it to a branch first" ;;
        clean) ;;
        *) die "slot-$n is $s" ;;
    esac
    if [ -z "$unpushed" ] && [ -z "$(git -C "$p" for-each-ref --count=1 --contains HEAD refs/remotes)" ]; then
        die "slot-$n: $(where_of "$n"), $(git -C "$p" rev-parse --short HEAD) is in no remote-tracking ref (push it, or --allow-unpushed)"
    fi
    git -C "$p" switch -q --detach >&2
    say "slot-$n released by $(field "$(lease_of "$n")" by), $(where_of "$n")"
    stop_holder "$n"
    if [ -n "$sweep" ]; then
        "$here/trim-targets.sh" "$p" >&2 || say "slot-$n: the sweep failed (it is released all the same)"
    fi
}

list() {
    local n s p f last
    for n in $(seq "$count"); do
        p=$(path_of "$n")
        s=$(state_of "$n")
        f=$(lease_of "$n")
        if held "$n"; then
            echo "slot-$n  leased  $p  by $(who_of "$n")"
        elif [ "$s" = absent ]; then
            echo "slot-$n  absent  $p  (made on its first lease)"
        else
            case "$s" in dirty:*) s="DIRTY (${s#dirty:} files)" ;; esac
            last=
            [ -z "$(field "$f" by)" ] || last=", last leased by $(field "$f" by) on $(field "$f" branch)"
            echo "slot-$n  free    $p  $(where_of "$n"), $s$last"
        fi
    done
}

[ $# -ge 1 ] || usage
cmd=$1; shift
case "$cmd" in
    lease) lease "$@" ;;
    release) release "$@" ;;
    list) [ $# -eq 0 ] || usage; list ;;
    *) usage ;;
esac
