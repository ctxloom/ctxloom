#!/usr/bin/env bash
# The close-out of a slice landing on an integration branch that carries a red
# spec on purpose (owner ruling 165): `just closeout-ratchet <base>`. The
# gates of `just closeout` (build, lint, lint-deps, test, lint-os,
# mutants-batch), each as usual, but for one thing: the feature runner (its
# tests of one feature file each, `tests::gherkin_features::features::*`) may fail, so long
# as the scenarios red on the slice are a SUBSET of those red on the base.
# A slice may turn red green; it may never add a red scenario or break a
# green one (scripts/ratchet-judge.sh).
#
#   1. The base's list: `just ratchet-base <base>` (scripts/ratchet-base.sh,
#      the feature runner on the base's tip in a throwaway worktree, cached by
#      its sha). Without it nothing can be judged: FAIL, exit 1, at once.
#   2. Every gate runs, so one report shows everything that is red. `test` is
#      `just test` with GHERKIN_RATCHET naming target/logs/ratchet/slice.list:
#      the feature runner writes its scenario list there and passes; every
#      other test fails `test` as it always does. The list is then judged
#      (the `ratchet` line), each scenario whose colour differs from the
#      base's list first run again on the base, alone and in its feature
#      (`ratchet-base.sh <base> --reconfirm`): a difference the base does not
#      reproduce is FLAKY and fails the ratchet (scripts/ratchet-judge.sh).
#      What a re-confirmation found is cached by the base's sha
#      (target/ratchet/<sha>.reconfirm, beside the base's list), each id
#      both re-runs gave its listed colour: a later land on the same base
#      re-runs only the ids not cached there. A flaky id is never cached,
#      and the cache is renamed into place only once its re-confirmation
#      succeeded, so an interrupted run's work does not count.
#      `mutants-batch` mutation-tests
#      merge-base(<base>, HEAD)..HEAD with GHERKIN_RATCHET_BASE naming the
#      base's list when it has red: the feature runner judges every run
#      against it in the test, so the base's red scenarios do not make the
#      baseline red, a new red still does, and a mutant of the binary that
#      turns a scenario red is killed (as `just mutants-shard` runs it). A
#      base with no red sets nothing: the plain gate.
#   3. A summary line: `ok   closeout-ratchet (6 gates; base 1a2b3c4: 136 red,
#      4 turned green) 812s`, or `FAIL closeout-ratchet: ...`.
#
# On a base with no red scenario (main) the ratchet is the plain closeout: the
# slice may have none either.
#
# `--no-mutation` before the base is the landing gate, `just land-ratchet
# <base>` (owner ruling of 2026-11-13): every gate above but mutants-batch
# (dropped even from a RATCHET_GATES that names it), the ratchet judged the
# same, every line said as `land-ratchet`. It runs at each branch's landing on
# the integration branch; the full closeout-ratchet runs once per batch and
# before main.
#
# However it ends (pass, fail, or the base's list not made), it sweeps what
# the run grew that a later build does not need (owner ruling 2026-11-24),
# scripts/land-sweep.sh: target/ratchet-base (never reused: each base is a new
# sha), the checkout's incremental directories, and the shared staged target
# by age, under its lock; the cached lists in target/ratchet/ are kept. One
# `ok   sweep (...)` line each; a sweep failure is said and changes no verdict.
#
# Exit: 0 when every gate passed and no scenario turned red; 1 when any gate
# failed, a scenario turned red or the base's list could not be made; 2 on a
# usage error.
#
# Environment: RATCHET_GATES (space-separated recipe names, default the six
# above), RATCHET_BASE_LIST (the base's list, instead of `ratchet-base`),
# RATCHET_RECONFIRM_CACHE (the re-confirmation cache's path) and
# RATCHET_RECONFIRM (a command given `<base> <ids> <out>`, instead of
# `ratchet-base.sh --reconfirm`): the self-test's seams; the mutants-batch
# variables; RATCHET_SWEEP (the sweep's command, instead of
# scripts/land-sweep.sh) is the self-test's seam.
set -uo pipefail
top=$(git rev-parse --show-toplevel)
here=$(cd "$(dirname "$0")" && pwd)
name=closeout-ratchet mutation=1
if [ "${1:-}" = --no-mutation ]; then
    name=land-ratchet mutation=
    shift
fi
if [ -z "${1:-}" ]; then
    echo "FAIL $name: usage: $name <base> (the integration branch the slice lands on)" >&2
    exit 2
fi
base=$1
base_sha=$(git -C "$top" rev-parse --verify --quiet "$base^{commit}") || { echo "FAIL $name: $base is not a commit" >&2; exit 2; }
fork=$(git -C "$top" merge-base "$base_sha" HEAD) || { echo "FAIL $name: $base and HEAD have no merge-base" >&2; exit 2; }
read -ra listed <<< "${RATCHET_GATES:-build lint lint-deps test lint-os mutants-batch}"
gates=()
for gate in "${listed[@]}"; do
    [ -z "$mutation" ] && [ "$gate" = mutants-batch ] && continue
    gates+=("$gate")
done
start=$(date +%s)
# The sweep runs at every exit from here on and never changes the status.
sweep_caches() { "${RATCHET_SWEEP:-$here/land-sweep.sh}" "$top" 2>&1 || echo "FAIL sweep: land-sweep.sh exited $?"; }
trap sweep_caches EXIT

base_list=${RATCHET_BASE_LIST:-$top/target/ratchet/$base_sha.list}
if [ -z "${RATCHET_BASE_LIST:-}" ]; then
    if ! just --justfile "$top/justfile" ratchet-base "$base_sha" 2>&1; then
        echo "FAIL $name: the base's scenario list could not be made, so nothing can be judged" >&2
        exit 1
    fi
fi
dir=$top/target/logs/ratchet
mkdir -p "$dir"
reconfirm_cache=${RATCHET_RECONFIRM_CACHE:-$top/target/ratchet/$base_sha.reconfirm}
slice_list=$dir/slice.list
rm -f "$slice_list"

# The ratchet's verdict, each difference from the base's list re-confirmed
# on the base first (owner, 2026-11-14): `ratchet-base.sh --reconfirm`, or
# RATCHET_RECONFIRM (the self-test's seam, given the same arguments); with
# RATCHET_BASE_LIST and no RATCHET_RECONFIRM nothing is re-confirmed.
judge_ratchet() {
    local ids=$dir/differ.ids reconfirmed=$dir/reconfirmed.list
    rm -f "$reconfirmed" "$reconfirmed.failures"
    if [ -s "$slice_list" ]; then
        { LC_ALL=C comm -12 <(sed -n 's/^red\t//p' "$slice_list" | LC_ALL=C sort -u) \
              <(sed -n 's/^green\t//p' "$base_list" | LC_ALL=C sort -u)
          LC_ALL=C comm -12 <(sed -n 's/^green\t//p' "$slice_list" | LC_ALL=C sort -u) \
              <(sed -n 's/^red\t//p' "$base_list" | LC_ALL=C sort -u); } | LC_ALL=C sort -u > "$ids"
    else
        : > "$ids"
    fi
    local reconfirm=${RATCHET_RECONFIRM:-}
    if [ -z "$reconfirm" ] && [ -n "${RATCHET_BASE_LIST:-}" ]; then
        "$here/ratchet-judge.sh" "$base_list" "$slice_list" 2>&1
        return
    fi
    : > "$reconfirmed"
    : > "$reconfirmed.failures"
    # What an earlier re-confirmation on this base found is cached by its
    # sha (target/ratchet/<sha>.reconfirm, `<alone><TAB><in its
    # feature><TAB><id>`, and its failures records in
    # <sha>.reconfirm.failures): an id cached there is not run again. Only
    # an id both re-runs gave its listed colour is cached, so a flaky one is
    # re-run every time. The cache is published by a rename once its
    # re-confirmation succeeded (its failures first, its list last), so a
    # run interrupted before that leaves the cache as it was.
    local todo=$dir/reconfirm.todo fresh=$dir/reconfirm.fresh
    : > "$todo"
    if [ -s "$ids" ]; then
        if [ -s "$reconfirm_cache" ]; then
            awk -F'\t' 'NR == FNR { want[$0] = 1; next } ($3 in want)' "$ids" "$reconfirm_cache" > "$reconfirmed"
            awk -F'\t' 'NR == FNR { have[$3] = 1; next } !($0 in have)' "$reconfirmed" "$ids" > "$todo"
            [ ! -e "$reconfirm_cache.failures" ] || records_of "$reconfirmed" "$reconfirm_cache.failures" >> "$reconfirmed.failures"
        else
            cp "$ids" "$todo"
        fi
        local cached=$(($(grep -c '' "$ids") - $(grep -c '' "$todo")))
        [ "$cached" -eq 0 ] || echo "note ratchet: $cached of the $(grep -c '' "$ids") scenarios whose colour differs were re-confirmed on ${base_sha:0:7} before (target/ratchet/${base_sha}.reconfirm)"
    fi
    if [ -s "$todo" ]; then
        local run=("$here/ratchet-base.sh" "$base_sha" --reconfirm)
        [ -z "$reconfirm" ] || run=("$reconfirm" "$base_sha")
        rm -f "$fresh" "$fresh.failures"
        if ! "${run[@]}" "$todo" "$fresh" 2>&1; then
            echo "FAIL ratchet: the base could not re-confirm the $(grep -c '' "$todo") scenarios whose colour differs (full log: target/logs/ratchet-base.log)" >&2
            return 1
        fi
        cat "$fresh" >> "$reconfirmed"
        [ ! -e "$fresh.failures" ] || cat "$fresh.failures" >> "$reconfirmed.failures"
        cache_reproduced "$fresh"
    fi
    "$here/ratchet-judge.sh" "$base_list" "$slice_list" "$reconfirmed" 2>&1
}

# The failures records (each from its `red<TAB><id>` line to the next) of
# the file $2 whose id is the third field of a line of $1.
records_of() {
    awk -F'\t' 'NR == FNR { want["red\t" $3] = 1; next } /^red\t/ { keep = ($0 in want) } keep' "$1" "$2"
}

# Add to the cache the lines of the fresh re-confirmation $1 that both
# re-runs gave the base's listed colour, with their failures records: into a
# temporary file beside each, renamed over it, the list last.
cache_reproduced() {
    local fresh=$1 good=$dir/reconfirm.good
    awk -F'\t' 'NR == FNR { listed[$2] = $1; next } $1 == $2 && listed[$3] == $1' "$base_list" "$fresh" > "$good"
    [ -s "$good" ] || return 0
    mkdir -p "$(dirname "$reconfirm_cache")"
    local list_tmp why_tmp
    list_tmp=$(mktemp "$reconfirm_cache.XXXXXX")
    why_tmp=$(mktemp "$reconfirm_cache.failures.XXXXXX")
    { [ ! -e "$reconfirm_cache" ] || cat "$reconfirm_cache"; cat "$good"; } > "$list_tmp"
    { [ ! -e "$reconfirm_cache.failures" ] || cat "$reconfirm_cache.failures"
      [ ! -e "$fresh.failures" ] || records_of "$good" "$fresh.failures"; } > "$why_tmp"
    mv "$why_tmp" "$reconfirm_cache.failures"
    mv "$list_tmp" "$reconfirm_cache"
}

failed=()
for gate in "${gates[@]}"; do
    case "$gate" in
        test)
            rc=0
            GHERKIN_RATCHET=$slice_list just --justfile "$top/justfile" test 2>&1 || rc=$?
            [ "$rc" -eq 0 ] || failed+=(test)
            # A run that did not build lists nothing: its failure is the test
            # gate's alone.
            if [ "$rc" -eq 0 ] || [ -e "$slice_list" ]; then
                judge_ratchet || failed+=(ratchet)
            fi
            ;;
        mutants-batch)
            judged=()
            ! grep -q '^red' "$base_list" || judged=("GHERKIN_RATCHET_BASE=$base_list")
            env "${judged[@]}" just --justfile "$top/justfile" mutants-batch "$fork" 2>&1 \
                || failed+=(mutants-batch)
            ;;
        *)
            just --justfile "$top/justfile" "$gate" 2>&1 || failed+=("$gate")
            ;;
    esac
done
secs=$(($(date +%s) - start))
red_count() { { grep -c '^red' "$1" 2>/dev/null || true; } | head -n 1; }
fixed=0
if [ -s "$slice_list" ]; then
    fixed=$(LC_ALL=C comm -23 <(sed -n 's/^red\t//p' "$base_list" | LC_ALL=C sort -u) \
        <(sed -n 's/^red\t//p' "$slice_list" | LC_ALL=C sort -u) | grep -c '' || true)
fi
if [ "${#failed[@]}" -eq 0 ]; then
    printf 'ok   %s (%d gates; base %s: %s red, %s turned green) %ds\n' \
        "$name" "${#gates[@]}" "${base_sha:0:7}" "$(red_count "$base_list")" "$fixed" "$secs"
    exit 0
fi
printf 'FAIL %s: %d failed: %s (base %s; logs: target/logs/)\n' \
    "$name" "${#failed[@]}" "${failed[*]}" "${base_sha:0:7}" >&2
exit 1
