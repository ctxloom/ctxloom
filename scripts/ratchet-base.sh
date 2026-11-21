#!/usr/bin/env bash
# The base's scenario list for the ratchet (owner ruling 165): `just
# ratchet-base <base>`. The feature runner, under GHERKIN_RATCHET, is run
# on the base commit (its tests of one feature file each,
# `tests::gherkin_features::features::*`, which nextest runs at once and
# which assemble the one list between them; a base's cluster tests,
# `tests::gherkin_features::clusters::*`, from before them; or its one
# `every_feature_runs` from before those),
# checked out alone in a throwaway worktree beside the checkout
# (.<checkout>.ratchet-base, its own target dir target/ratchet-base), and its
# list is cached by the base's sha at target/ratchet/<sha>.list; a cached
# list is used as it is.
#
#   ok   ratchet-base (1a2b3c4: 136 red of 512) 301.0s
#
# A base from before the runner could list its scenarios gets the commit
# that taught it (the first in HEAD's history adding GHERKIN_RATCHET to
# gherkin_features.test.rs) applied on it, its runner files only, and says
# so; one it does not apply to fails. Only that test runs, so nothing but the
# list is judged on the base.
#
# `ratchet-base <base> --reconfirm <ids> <out>` (owner, 2026-11-14) runs each
# scenario id of the file <ids> again on the base, in the same worktree:
# ALONE (GHERKIN_SCENARIO), then in its feature file (GHERKIN_ONLY), and
# writes `<alone><TAB><in its feature><TAB><id>` per id to <out>, each colour
# `red`, `green` or `none`, and the re-runs' failures to <out>.failures. A
# base whose runner cannot run one scenario gets the commit that taught it
# applied, as above. Exit 0 with <out> written, 1 otherwise.
#
# The worktree is removed once the list is cached; an interrupted run leaves
# it, at the same sha, so the next run resumes the build where it stopped
# (cargo fingerprints by mtime). Exit 0 with the list cached, 1 otherwise.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
scrub=(env)
while IFS= read -r var; do scrub+=(-u "$var"); done < <(git rev-parse --local-env-vars)
g() { "${scrub[@]}" git "$@"; }

top=$(g rev-parse --show-toplevel)
[ -n "${1:-}" ] || { echo "FAIL ratchet-base: usage: ratchet-base <base> [--reconfirm <ids> <out>]" >&2; exit 1; }
ids= out=
if [ "${2:-}" = --reconfirm ]; then
    ids=${3:?usage: ratchet-base <base> --reconfirm <ids> <out>}
    out=${4:?usage: ratchet-base <base> --reconfirm <ids> <out>}
fi
base_sha=$(g -C "$top" rev-parse --verify --quiet "$1^{commit}") || { echo "FAIL ratchet-base: $1 is not a commit" >&2; exit 1; }
short=${base_sha:0:7}
cache_dir=$top/target/ratchet
cache=$cache_dir/$base_sha.list
mkdir -p "$cache_dir" "$top/target/logs"
log=$top/target/logs/ratchet-base.log
summary() { printf 'ok   ratchet-base (%s: %d red of %d)%s\n' "$short" "$(grep -c '^red' "$cache" || true)" "$(grep -c '' "$cache")" "$1"; }
if [ -z "$ids" ] && [ -s "$cache" ]; then
    summary " cached"
    exit 0
fi

# The rest, the base's build and its feature runner, is a whole-workspace
# run: the script runs again under the machine-wide test lock
# (gates/tests-lock.sh; a wait on it is said on stderr), which a caller
# already holding it does not take again. Before `.ratchet-base.lock`, so
# every holder of both took the test lock first (one order: no deadlock),
# and so the lock's descriptor is never this script's own.
if [ -z "${RATCHET_BASE_UNDER_LOCK:-}" ]; then
    RATCHET_BASE_UNDER_LOCK=1 exec "$here/../gates/tests-lock.sh" ratchet-base "$0" "$@"
fi
unset RATCHET_BASE_UNDER_LOCK

exec 7> "$top/target/.ratchet-base.lock"
flock 7
runner=crates/root/bin/ctxloom/src/gherkin_features.test.rs
wt=$(dirname "$top")/.$(basename "$top").ratchet-base
stamp=$cache_dir/worktree.stamp
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
trap 'exit 130' INT TERM HUP

# The first commit is taken with `sed -n 1p`, never `head -n 1`: head closes
# the pipe early, git log dies of SIGPIPE, and pipefail with set -e ends the
# script there without a word (exit 141).
emitter=
if ! g -C "$top" grep -q GHERKIN_RATCHET "$base_sha" -- "$runner"; then
    emitter=$(g -C "$top" log --reverse --format=%H -S GHERKIN_RATCHET HEAD -- "$runner" | sed -n 1p)
    if [ -z "$emitter" ]; then
        echo "FAIL ratchet-base: $short cannot list its scenarios, and HEAD has no commit that teaches it (GHERKIN_RATCHET)" >&2
        exit 1
    fi
fi
# A re-confirmation runs one scenario alone: a base whose runner cannot is
# taught by the commit that taught HEAD's.
chooser=
if [ -n "$ids" ] && ! g -C "$top" grep -q GHERKIN_SCENARIO "$base_sha" -- "$runner"; then
    chooser=$(g -C "$top" log --reverse --format=%H -S GHERKIN_SCENARIO HEAD -- "$runner" | sed -n 1p)
    if [ -z "$chooser" ]; then
        echo "FAIL ratchet-base: $short cannot run one scenario alone, and HEAD has no commit that teaches it (GHERKIN_SCENARIO)" >&2
        exit 1
    fi
fi
want="$base_sha ${emitter:-none} ${chooser:-none}"
remove_tree() {
    # The base's tree and its own record only, never a repository-wide prune
    # (in a container that mounts only this checkout it deletes every other
    # worktree's record): gates/drop-worktree.sh.
    "${scrub[@]}" "$here/../gates/drop-worktree.sh" "$top" "$wt"
    rm -f "$stamp"
}
if [ ! -d "$wt" ] || [ "$(cat "$stamp" 2>/dev/null)" != "$want" ] \
    || [ "$(g -C "$wt" rev-parse HEAD 2>/dev/null)" != "$base_sha" ]; then
    remove_tree
    g -C "$top" worktree add --quiet --detach --no-checkout "$wt" "$base_sha"
    g -C "$wt" read-tree -u --reset HEAD
    for teacher in $emitter $chooser; do
        g -C "$top" diff --binary --no-color --no-ext-diff "$teacher^" "$teacher" \
            -- 'crates/root/bin/ctxloom/src/gherkin*.test.rs' > "$work/teacher.patch"
        if ! g -C "$wt" apply --whitespace=nowarn "$work/teacher.patch" 2> "$work/apply.err"; then
            echo "FAIL ratchet-base: $short predates the runner HEAD's ratchet needs, and its commit ${teacher:0:7} does not apply to it; merge the base's line into it first" >&2
            head -n 10 "$work/apply.err" >&2
            remove_tree
            exit 1
        fi
    done
    echo "$want" > "$stamp"
fi
[ -z "$emitter" ] || echo "note ratchet-base: $short predates the scenario list; ${emitter:0:7}'s runner change was applied on it"
[ -z "$chooser" ] || echo "note ratchet-base: $short cannot run one scenario alone; ${chooser:0:7}'s runner change was applied on it"

# The test gates' memory cap (gates/memory-cap.sh, the checkout's, at the
# justfile's `tests_memory_max` and `tests_swap_max`; TESTS_MEMORY_MAX=none
# opts out), had before anything is built: no user bus is a failure.
cap=(env MEMORY_CAP_OPT_OUT=TESTS_MEMORY_MAX "$top/gates/memory-cap.sh" ratchet-base
    "$(just --justfile "$top/justfile" --evaluate tests_memory_max)"
    "$(just --justfile "$top/justfile" --evaluate tests_swap_max)")
"${cap[@]}" --check

# The feature runner on the base, its list to $1, the rest of the arguments
# its environment; its output appended to the log. Built without cargo's
# incremental cache (CARGO_INCREMENTAL=0, 2026-11-18): the base's tree is
# a new commit each time, so the cache is written and never reused, while
# target/debug and target/staged keep it. The runner is the base's:
# its feature tests, its clusters, or its one test from before them; it runs
# under the cap.
run_features() {
    local list=$1
    shift
    (cd "$wt" && "${scrub[@]}" "CARGO_TARGET_DIR=$top/target/ratchet-base" CARGO_INCREMENTAL=0 "GHERKIN_RATCHET=$list" "$@" \
        "${cap[@]}" cargo nextest run --manifest-path "$wt/Cargo.toml" --package ctxloom --no-fail-fast --no-tests=fail \
        --status-level fail --final-status-level fail --success-output never --failure-output final \
        --hide-progress-bar --cargo-quiet -E "$runner_tests") \
        >> "$log" 2>&1 7>&-
}

runner_tests='test(=tests::gherkin_features::every_feature_runs) | test(/^tests::gherkin_features::clusters::/) | test(/^tests::gherkin_features::features::/)'
start=$(date +%s)
: > "$log"
if [ -n "$ids" ]; then
    colour() { awk -F'\t' -v want="$2" '$2 == want { print $1; found = 1; exit } END { if (!found) print "none" }' "$1" 2>/dev/null || echo none; }
    : > "$work/out"
    : > "$work/out.failures"
    while IFS= read -r id; do
        [ -n "$id" ] || continue
        feature=${id%% :: *}
        rm -f "$work/alone" "$work/alone.failures" "$work/feature" "$work/feature.failures"
        if ! run_features "$work/alone" "GHERKIN_SCENARIO=$id" || ! run_features "$work/feature" "GHERKIN_ONLY=$feature"; then
            echo "FAIL ratchet-base: the feature runner did not re-run '$id' on $short (full log: target/logs/ratchet-base.log)" >&2
            exit 1
        fi
        printf '%s\t%s\t%s\n' "$(colour "$work/alone" "$id")" "$(colour "$work/feature" "$id")" "$id" >> "$work/out"
        cat "$work/alone.failures" "$work/feature.failures" >> "$work/out.failures" 2>/dev/null || true
    done < "$ids"
    mv "$work/out" "$out"
    mv "$work/out.failures" "$out.failures"
    remove_tree
    printf 'ok   ratchet-base (%s: %d re-confirmed) %ds\n' "$short" "$(grep -c '' "$out")" "$(($(date +%s) - start))"
    exit 0
fi
rc=0
run_features "$work/list" || rc=$?
secs=$(($(date +%s) - start))
if [ "$rc" -ne 0 ] || [ ! -s "$work/list" ]; then
    echo "FAIL ratchet-base: the feature runner did not list $short's scenarios (nextest exit $rc) (full log: target/logs/ratchet-base.log)" >&2
    grep -E '^[^ ]+:[0-9]+:[0-9]+: error|^error(\[[A-Z0-9]+\])?: |^ +FAIL ' "$log" | head -n 20 >&2 || true
    exit 1
fi
mv "$work/list" "$cache"
# Why each of the base's red scenarios is red, cached beside its list.
if [ -e "$work/list.failures" ]; then mv "$work/list.failures" "$cache.failures"; else rm -f "$cache.failures"; fi
remove_tree
summary " ${secs}.0s"
