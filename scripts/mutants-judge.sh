#!/usr/bin/env bash
# The mutation gate's judge (`_mutants-judge` in gates/gates.just runs it once
# cargo mutants has run and its exit code is known to be a verdict): from
# cargo-mutants' output, every mutant found must have been tested, and each
# tier's kill rate must reach its threshold. Killed = caught or timed out;
# missed = survived. A failing verdict goes to <out>/verdict.txt: its summary
# line, then the kill rate by tier, for the gate's extract.
#
# Usage: mutants-judge.sh <out> <thresholds> <cargo-mutants exit code>
#   <out>         the gate's output directory, holding mutants.out/
#   <thresholds>  "<path>=<percent> ..." (the justfile's mutation_thresholds)
# Exit 0 on a pass, 1 on a failing verdict.
set -euo pipefail
out=${1:?usage: mutants-judge.sh <out> <thresholds> <rc>}
thresholds=${2:?usage: mutants-judge.sh <out> <thresholds> <rc>}
rc=${3:?usage: mutants-judge.sh <out> <thresholds> <rc>}
verdict() { printf '%s\n' "$@" | tee -a "$out/verdict.txt"; }

# Every mutant found must have been tested: a run that found some and judged
# fewer (its baseline failing under an exit code that did not say so, or cut
# short) has not shown the tests catch them, so it fails rather than passing
# on the ones it never ran.
found=0
[ -f "$out/mutants.out/mutants.json" ] && found=$(jq length "$out/mutants.out/mutants.json")
if [ "$found" -eq 0 ]; then
    echo "mutants-staged: no mutants: the staged diff changes no mutable code."
    exit 0
fi
outcomes="$out/mutants.out/outcomes.json"
judged='[.outcomes[] | select(.scenario | type == "object") | {name: .scenario.Mutant.name, file: .scenario.Mutant.file, summary, log_path}]'
tested=0
[ -f "$outcomes" ] && tested=$(jq "$judged | length" "$outcomes")
if [ "$tested" -ne "$found" ]; then
    verdict "$found mutants found but $tested tested (cargo mutants exit $rc); nothing is judged until every one is" \
        "run log: target/logs/mutants-staged.log"
    exit 1
fi
read -ra tiers <<< "$thresholds"
# Each mutant's tier: the longest threshold prefix its file is under.
tiered=$(jq --arg tiers "${tiers[*]}" "$judged"' | ($tiers | split(" ") | map(split("=") | {prefix: .[0], min: (.[1] | tonumber)})) as $t
    | map(. as $m | . + {tier: ([$t[] | . as $tier | select($m.file | startswith($tier.prefix + "/"))] | max_by(.prefix | length))})' "$outcomes")
untiered=$(jq -r '.[] | select(.tier == null) | .file' <<< "$tiered" | sort -u)
if [ -n "$untiered" ]; then
    verdict "no threshold in mutation_thresholds (justfile) for: $(paste -sd ' ' <<< "$untiered")"
    exit 1
fi
report=$(jq -r 'group_by(.tier.prefix)[] | (map(select(.summary == "CaughtMutant" or .summary == "Timeout")) | length) as $killed
    | (map(select(.summary == "MissedMutant")) | length) as $missed | ($killed + $missed) as $viable
    | .[0].tier as $tier | (if $viable == 0 then 100 else ($killed * 100 / $viable | floor) end) as $rate
    | "\(if $killed * 100 >= $tier.min * $viable then "pass" else "FAIL" end)  \($tier.prefix): \($killed)/\($viable) killed (\($rate)%), threshold \($tier.min)%"
        + (map(select(.summary == "Unviable")) | length | if . > 0 then "; \(.) unviable, not counted" else "" end)
        + (map(select(.summary == "Timeout")) | length | if . > 0 then "; \(.) timed out, counted killed" else "" end)' <<< "$tiered")
echo "mutants-staged: kill rate by tier:"
sed 's/^/  /' <<< "$report"
if grep -q '^FAIL' <<< "$report"; then
    verdict "below the kill-rate threshold: $(grep '^FAIL' <<< "$report" | sed 's/^FAIL  //; s/;.*//' | paste -sd ';' | sed 's/;/; /g')" \
        "kill rate by tier:" "$(sed 's/^/  /' <<< "$report")"
    exit 1
fi
