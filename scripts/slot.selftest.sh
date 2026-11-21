#!/usr/bin/env bash
# Self-test of scripts/slot.sh (or $SLOT) against a fixture repository with
# an origin, a pool of 3 slots and stand-in owners (sleep processes, given as
# --holder): a lease makes a slot on its first use and prints its path alone,
# a new branch from --from and an existing one; a full pool refuses, saying
# each slot's holder, and --wait gets the slot let go meanwhile; a release is
# refused while the slot holds uncommitted work or an unpushed HEAD, and
# allowed with --allow-unpushed; a release detaches HEAD and frees the slot;
# a crashed owner's slot frees itself; a free slot left dirty is passed over
# by the next lease, said; and list says each state. The sweep a release runs
# is trim-targets.selftest.sh's to test (here --no-sweep).
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
slot=${SLOT:-$here/slot.sh}
tmp=$(mktemp -d)
pids=()
cleanup() {
    for p in ${pids[@]+"${pids[@]}"}; do kill "$p" 2>/dev/null || true; done
    rm -rf "$tmp"
}
trap cleanup EXIT
fail() { echo "FAIL slot.selftest: $*" >&2; exit 1; }
while IFS= read -r v; do unset "$v"; done < <(git rev-parse --local-env-vars)
export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t
g() { git -c init.defaultBranch=integ "$@"; }

g init -q --bare "$tmp/origin.git"
g init -q "$tmp/repo"
echo one > "$tmp/repo/file"
g -C "$tmp/repo" add file
g -C "$tmp/repo" commit -q -m one
g -C "$tmp/repo" remote add origin "$tmp/origin.git"
g -C "$tmp/repo" push -q origin integ
g -C "$tmp/repo" branch -q existing
g -C "$tmp/repo" fetch -q origin

export SLOT_REPO=$tmp/repo SLOT_ROOT=$tmp/pool SLOT_PREFIX=proj--slot- SLOT_COUNT=3
# A stand-in owner: its pid in $1's name.
owner() { sleep 600 < /dev/null > /dev/null 2>&1 & pids+=($!); printf -v "$1" %s $!; }
run() { out=$("$slot" "$@" 2> "$tmp/err") || { rc=$?; err=$(cat "$tmp/err"); return $rc; }; err=$(cat "$tmp/err"); }
has() { grep -qF -- "$2" <<< "$1" || fail "no '$2' in: $1"; }

# A first lease makes slot-1 and prints its path alone.
owner a
run lease task/a --from integ --holder "$a" --by agent-a || fail "lease a: $err"
[ "$out" = "$tmp/pool/proj--slot-1" ] || fail "lease a printed '$out'"
[ "$(git -C "$out" symbolic-ref --short HEAD)" = task/a ] || fail "slot-1 is not on task/a"
has "$err" "slot-1 leased by agent-a (owner pid $a), on task/a"
# A new branch needs --from; an existing one does not.
owner b
rc=0; run lease task/new --holder "$b" || rc=$?
[ "$rc" -eq 1 ] || fail "a new branch without --from: exit $rc"
has "$err" "no branch 'task/new' here or on origin; give --from <base> to make it"
run lease existing --holder "$b" --by agent-b || fail "lease b: $err"
[ "$out" = "$tmp/pool/proj--slot-2" ] || fail "lease b printed '$out'"
owner c
run lease task/c --from integ --holder "$c" --by agent-c || fail "lease c: $err"

# The pool is full: refused, each holder said.
owner d
rc=0; run lease task/d --from integ --holder "$d" --by agent-d || rc=$?
[ "$rc" -eq 1 ] || fail "a full pool: exit $rc"
has "$err" "no slot free of 3:"
has "$err" "slot-1 leased by agent-a (owner pid $a"
has "$err" "slot-2 leased by agent-b (owner pid $b"
has "$err" "slot-3 leased by agent-c (owner pid $c"
has "$err" "every slot is leased or unusable; --wait <secs> waits for one"
run list || fail "list: $err"
has "$out" "slot-1  leased  $tmp/pool/proj--slot-1  by agent-a (owner pid $a sleep), branch task/a, since "

# A release is refused while the slot holds work, then while unpushed.
echo work > "$tmp/pool/proj--slot-1/new-file"
rc=0; run release slot-1 --no-sweep || rc=$?
[ "$rc" -eq 1 ] || fail "a dirty release: exit $rc"
has "$err" "slot-1 holds uncommitted work (1 files"
g -C "$tmp/pool/proj--slot-1" add new-file
g -C "$tmp/pool/proj--slot-1" commit -q -m work
rc=0; SLOT_FROM=$tmp/pool/proj--slot-1 run release --no-sweep || rc=$?
[ "$rc" -eq 1 ] || fail "an unpushed release: exit $rc"
has "$err" "slot-1: on task/a, "
has "$err" "is in no remote-tracking ref (push it, or --allow-unpushed)"
g -C "$tmp/pool/proj--slot-1" push -q origin task/a
SLOT_FROM=$tmp/pool/proj--slot-1 run release --no-sweep || fail "release a: $err"
has "$err" "slot-1 released by agent-a, detached at "
git -C "$tmp/pool/proj--slot-1" symbolic-ref -q HEAD > /dev/null && fail "the released slot is still on its branch"
kill -0 "$a" || fail "the release stopped the owner"
run list || fail "list: $err"
has "$out" "slot-1  free    $tmp/pool/proj--slot-1  detached at "
has "$out" ", clean, last leased by agent-a on task/a"

# The freed slot is taken again, warm (an existing slot, its files kept).
echo build > "$tmp/pool/proj--slot-1/ignored-not"   # untracked: dirty
rc=0; run lease task/d --from integ --holder "$d" || rc=$?
[ "$rc" -eq 1 ] || fail "a dirty free slot was taken: exit $rc"
has "$err" "slot-1 passed over: free, but holds uncommitted work (1 files)"
rm "$tmp/pool/proj--slot-1/ignored-not"
run lease task/d --from integ --holder "$d" --by agent-d || fail "lease d: $err"
[ "$out" = "$tmp/pool/proj--slot-1" ] || fail "lease d printed '$out'"

# --wait: slot-2's owner releases (allowed unpushed) while e waits.
owner e
( sleep 1; "$slot" release slot-2 --allow-unpushed --no-sweep 2> /dev/null ) &
pids+=($!)
run lease task/e --from integ --holder "$e" --by agent-e --wait 20 || fail "lease e (waiting): $err"
[ "$out" = "$tmp/pool/proj--slot-2" ] || fail "lease e printed '$out'"
rc=0; run lease task/f --from integ --holder "$e" --wait 1 || rc=$?
[ "$rc" -eq 1 ] || fail "a timed-out wait: exit $rc"
has "$err" "waited 1s; none came free"

# A crashed owner frees its slot by itself; its work, if any, stays.
kill "$c"; wait "$c" 2>/dev/null || true
for _ in $(seq 50); do run list; grep -q "slot-3  free" <<< "$out" && break; sleep 0.1; done
has "$out" "slot-3  free    $tmp/pool/proj--slot-3  on task/c, clean, last leased by agent-c on task/c"
owner f
run lease task/f --from integ --holder "$f" --by agent-f || fail "lease after a crash: $err"
[ "$out" = "$tmp/pool/proj--slot-3" ] || fail "lease f printed '$out'"

# Usage, and a release of a slot nobody holds.
rc=0; "$slot" > /dev/null 2>&1 || rc=$?
[ "$rc" -eq 2 ] || fail "usage: exit $rc"
kill "$f"; wait "$f" 2>/dev/null || true
for _ in $(seq 50); do run list; grep -q "slot-3  free" <<< "$out" && break; sleep 0.1; done
rc=0; run release slot-3 --no-sweep || rc=$?
[ "$rc" -eq 1 ] || fail "releasing a free slot: exit $rc"
has "$err" "slot-3 is not leased"

echo "ok   slot.selftest (lease makes and switches, full pool refused and waited, dirty and unpushed releases refused, crashed owner freed, dirty slot passed over)"
