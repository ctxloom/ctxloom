#!/usr/bin/env bash
# The ratchet's verdict (owner ruling 165): `ratchet-judge.sh <base list>
# <slice list>`. Each list is the feature runner's GHERKIN_RATCHET output, a
# line per scenario, `red<TAB><id>` or `green<TAB><id>`. The slice passes when
# its red scenarios are a SUBSET of the base's: it may turn red green, never
# add a red scenario (one the base does not have) or break a green one (one
# the base passes).
#
#   ok   ratchet (base 136 red; here 130 red, 6 turned green)
#   FAIL ratchet: 2 new red scenarios: 1 broke green, 1 added red (full list: <slice list>)
#     broke green  cli/x.feature :: s
#     added red    journeys/y.feature :: t
#
# At most RATCHET_LIST_MAX (default 30) scenarios are named, then a `+K more`
# line. Exit 0 on a pass, 1 on a new red or a list that is missing or empty
# (a feature runner that listed nothing has shown nothing).
set -euo pipefail
base=${1:?usage: ratchet-judge.sh <base list> <slice list>}
slice=${2:?usage: ratchet-judge.sh <base list> <slice list>}
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
LC_ALL=C comm -13 "$work/base.red" "$work/slice.red" > "$work/new"
LC_ALL=C comm -12 "$work/new" "$work/base.green" > "$work/broke"
LC_ALL=C comm -23 "$work/new" "$work/base.green" > "$work/added"
count() { grep -c '' "$1" || true; }
base_red=$(count "$work/base.red")
slice_red=$(count "$work/slice.red")
new=$(count "$work/new")
if [ "$new" -eq 0 ]; then
    fixed=$(LC_ALL=C comm -23 "$work/base.red" "$work/slice.red" | grep -c '' || true)
    printf 'ok   ratchet (base %d red; here %d red, %d turned green)\n' "$base_red" "$slice_red" "$fixed"
    exit 0
fi
printf 'FAIL ratchet: %d new red scenarios: %d broke green, %d added red (full list: %s)\n' \
    "$new" "$(count "$work/broke")" "$(count "$work/added")" "$slice" >&2
{ sed 's/^/  broke green  /' "$work/broke"; sed 's/^/  added red    /' "$work/added"; } > "$work/named"
head -n "$cap" "$work/named" >&2
[ "$new" -le "$cap" ] || echo "  +$((new - cap)) more: grep '^red' $slice" >&2
exit 1
