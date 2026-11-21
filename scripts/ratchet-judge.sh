#!/usr/bin/env bash
# The ratchet's verdict (owner ruling 165): `ratchet-judge.sh <base list>
# <slice list> [<re-confirmed>]`. Each list is the feature runner's
# GHERKIN_RATCHET output, a line per scenario, `red<TAB><id>` or
# `green<TAB><id>`. The slice passes when its red scenarios are a SUBSET of
# the base's: it may turn red green, never add a red scenario (one the base
# does not have) or break a green one (one the base passes).
#
#   ok   ratchet (base 136 red; here 130 red, 6 turned green)
#   FAIL ratchet: 2 new red scenarios: 1 broke green, 1 added red (full list: <slice list>; why: <slice list>.failures)
#     broke green  cli/x.feature :: s
#     added red    journeys/y.feature :: t
#
# The `why:` file is the feature runner's, beside its list: each red
# scenario's failed step and what it said. It is named when it is there.
#
# <re-confirmed> (owner, 2026-11-14), when given, is what the base said of
# each scenario whose colour here differs from the base's list, run again on
# the base: a line `<alone><TAB><in its feature><TAB><id>`, each colour `red`,
# `green` or `none` (not run). A difference both re-runs give the base's
# listed colour is reproduced, and judged as above (a new red stays one, a
# turned green passes). One that is not, or that was not re-run, is FLAKY:
# named with its colours and its reason (the first failures record of it, the
# slice's, the re-confirmation's or the base's), and it FAILS the ratchet,
# whichever way it went:
#
#   FAIL ratchet: 0 new red scenarios: 0 broke green, 0 added red, 1 flaky (full list: ...)
#     flaky        cli/x.feature :: s
#   FLAKY cli/x.feature :: s (base: alone red, in its feature green, listed green; here red)
#     why: Then it holds: it did not
#
# At most RATCHET_LIST_MAX (default 30) scenarios are named, then a `+K more`
# line. Exit 0 on a pass, 1 on a new red, a flaky difference or a list that
# is missing or empty (a feature runner that listed nothing has shown
# nothing).
set -euo pipefail
base=${1:?usage: ratchet-judge.sh <base list> <slice list> [<re-confirmed>]}
slice=${2:?usage: ratchet-judge.sh <base list> <slice list> [<re-confirmed>]}
reconfirmed=${3:-}
cap=${RATCHET_LIST_MAX:-30}
for list in "$base" "$slice"; do
    if [ ! -s "$list" ]; then
        echo "FAIL ratchet: no scenario list at $list (the feature runner listed nothing)" >&2
        exit 1
    fi
done
red() { sed -n 's/^red\t//p' "$1" | LC_ALL=C sort -u; }
green() { sed -n 's/^green\t//p' "$1" | LC_ALL=C sort -u; }
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
red "$base" > "$work/base.red"
green "$base" > "$work/base.green"
red "$slice" > "$work/slice.red"
green "$slice" > "$work/slice.green"
: > "$work/flaky"
: > "$work/flaky.said"
if [ -n "$reconfirmed" ]; then
    # Each scenario listed in both whose colour differs, and the colour the
    # base listed for it.
    { LC_ALL=C comm -12 "$work/slice.red" "$work/base.green" | sed 's/^/green\t/'
      LC_ALL=C comm -12 "$work/slice.green" "$work/base.red" | sed 's/^/red\t/'; } > "$work/differ"
    # The first failures record of `id` in any of the files: its step and the
    # first line of what it said.
    why() {
        local id=$1 file
        for file in "$slice.failures" "$reconfirmed.failures" "$base.failures"; do
            [ -e "$file" ] || continue
            awk -v want="red	$id" '
                $0 == want { on = 1; next }
                on && /^  step: / { step = substr($0, 9); next }
                on && /^  / { print step ": " substr($0, 3); found = 1; exit }
                on && $0 == "" { if (step != "") { print step; found = 1 }; exit }
                END { exit !found }' "$file" && return 0
        done
        return 1
    }
    while IFS=$'\t' read -r listed id; do
        [ -n "$id" ] || continue
        here=green
        [ "$listed" = green ] && here=red
        line=$(awk -F'\t' -v want="$id" '$3 == want { print $1 "\t" $2; exit }' "$reconfirmed")
        alone=${line%%$'\t'*}
        infeature=${line#*$'\t'}
        [ -n "$line" ] || { alone=none; infeature=none; }
        if [ "$alone" = "$listed" ] && [ "$infeature" = "$listed" ]; then
            continue
        fi
        echo "$id" >> "$work/flaky"
        {
            printf 'FLAKY %s (base: alone %s, in its feature %s, listed %s; here %s)\n' \
                "$id" "$alone" "$infeature" "$listed" "$here"
            reason=$(why "$id") && printf '  why: %s\n' "$reason"
        } >> "$work/flaky.said"
    done < "$work/differ"
fi
LC_ALL=C sort -u -o "$work/flaky" "$work/flaky"
# A flaky scenario is judged as flaky, never as a new red as well.
LC_ALL=C comm -13 "$work/base.red" "$work/slice.red" | LC_ALL=C comm -23 - "$work/flaky" > "$work/new"
LC_ALL=C comm -12 "$work/new" "$work/base.green" > "$work/broke"
LC_ALL=C comm -23 "$work/new" "$work/base.green" > "$work/added"
count() { grep -c '' "$1" || true; }
base_red=$(count "$work/base.red")
slice_red=$(count "$work/slice.red")
new=$(count "$work/new")
flaky=$(count "$work/flaky")
if [ "$new" -eq 0 ] && [ "$flaky" -eq 0 ]; then
    fixed=$(LC_ALL=C comm -23 "$work/base.red" "$work/slice.red" | grep -c '' || true)
    printf 'ok   ratchet (base %d red; here %d red, %d turned green)\n' "$base_red" "$slice_red" "$fixed"
    exit 0
fi
why_file=
[ -e "$slice.failures" ] && why_file="; why: $slice.failures"
flaky_said=
[ "$flaky" -eq 0 ] || flaky_said=", $flaky flaky"
printf 'FAIL ratchet: %d new red scenarios: %d broke green, %d added red%s (full list: %s%s)\n' \
    "$new" "$(count "$work/broke")" "$(count "$work/added")" "$flaky_said" "$slice" "$why_file" >&2
{ sed 's/^/  broke green  /' "$work/broke"; sed 's/^/  added red    /' "$work/added"
  sed 's/^/  flaky        /' "$work/flaky"; } > "$work/named"
head -n "$cap" "$work/named" >&2
named=$(count "$work/named")
[ "$named" -le "$cap" ] || echo "  +$((named - cap)) more: grep '^red' $slice" >&2
if [ "$flaky" -gt 0 ]; then
    head -n "$((cap * 2))" "$work/flaky.said" >&2
fi
exit 1
