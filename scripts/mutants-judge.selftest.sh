#!/usr/bin/env bash
# Self-test of mutants-judge.sh, the mutation gate's verdict, over fixture
# cargo-mutants outputs (mutants.json, outcomes.json, each mutant's log).
# Checks: a pass; a tier below its threshold; mutants found and not all
# tested; a file under no threshold; no mutants at all; and (mutation gate
# hollow-pass, 2026-11-10) a tier whose every mutant was unviable judged
# nothing and FAILS, and a mutant build that failed for a reason that is not
# the mutant (a cargo configuration error, such as a feature the package does
# not have) FAILS the gate as such, never counted "unviable".
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
fail() { echo "FAIL mutants-judge.selftest: $*" >&2; exit 1; }
thresholds="crates/core/domain=90 crates/core/application=90"

# fixture <name> <file:summary:log> ...: a cargo-mutants output whose
# mutants are in <file>, judged <summary>, their build log saying <log>
# (one of: compile, config, ok).
fixture() {
    local dir=$tmp/$1; shift
    mkdir -p "$dir/mutants.out/log"
    local n=0 mutants="[]" outcomes='[{"scenario":"Baseline","summary":"Success"}]'
    for spec in "$@"; do
        n=$((n + 1))
        IFS=: read -r file summary log <<< "$spec"
        local name="$file:$n: replace f with ()" log_path="log/m$n.log"
        mutants=$(jq --arg f "$file" --arg n "$name" '. + [{name: $n, file: $f}]' <<< "$mutants")
        outcomes=$(jq --arg f "$file" --arg n "$name" --arg s "$summary" --arg l "$log_path" \
            '. + [{scenario: {Mutant: {name: $n, file: $f}}, summary: $s, log_path: $l}]' <<< "$outcomes")
        case "$log" in
        compile) printf 'error[E0308]: mismatched types\nerror: could not compile `x` (lib) due to 1 previous error\n' ;;
        config) printf "error: the package 'session-index' does not contain this feature: session-cli/session-destroy\n" ;;
        *) printf 'test result: ok\n' ;;
        esac > "$dir/mutants.out/$log_path"
    done
    echo "$mutants" > "$dir/mutants.out/mutants.json"
    jq -n --argjson o "$outcomes" '{outcomes: $o}' > "$dir/mutants.out/outcomes.json"
    echo "$dir"
}
judge() { "$here/mutants-judge.sh" "$1" "$thresholds" 0; }
D=crates/core/domain/x/src/lib.rs
A=crates/core/application/y/src/lib.rs

dir=$(fixture pass "$D:CaughtMutant:ok" "$D:Timeout:ok" "$A:CaughtMutant:ok" "$A:Unviable:compile")
said=$(judge "$dir") || fail "a pass failed: $said"
grep -q "pass  crates/core/application: 1/1 killed (100%), threshold 90%; 1 unviable, not counted" <<< "$said" \
    || fail "a pass's report: $said"

dir=$(fixture low "$D:CaughtMutant:ok" "$D:MissedMutant:ok")
if said=$(judge "$dir"); then fail "a tier below its threshold passed: $said"; fi
grep -q "below the kill-rate threshold: crates/core/domain: 1/2" "$dir/verdict.txt" || fail "low verdict: $(cat "$dir/verdict.txt")"

dir=$(fixture short "$D:CaughtMutant:ok")
jq '. + [{name: "never run", file: "x"}]' "$dir/mutants.out/mutants.json" > "$tmp/m" && mv "$tmp/m" "$dir/mutants.out/mutants.json"
if said=$(judge "$dir"); then fail "a run that tested fewer than it found passed: $said"; fi
grep -q "2 mutants found but 1 tested" "$dir/verdict.txt" || fail "short verdict: $(cat "$dir/verdict.txt")"

dir=$(fixture untiered "crates/elsewhere/src/lib.rs:CaughtMutant:ok")
if said=$(judge "$dir"); then fail "an untiered file passed: $said"; fi
grep -q "no threshold in mutation_thresholds" "$dir/verdict.txt" || fail "untiered verdict"

mkdir -p "$tmp/none"
said=$(judge "$tmp/none") || fail "no mutants failed: $said"
grep -q "no mutants" <<< "$said" || fail "no mutants said: $said"

# Hollow: a tier that found mutants and judged none (every one unviable,
# for a reason in the mutant itself) has shown nothing: it fails.
dir=$(fixture hollow "$D:CaughtMutant:ok" "$A:Unviable:compile" "$A:Unviable:compile")
if said=$(judge "$dir"); then fail "a tier that judged nothing (all unviable) passed: $said"; fi
grep -q "crates/core/application: 2 mutants found and none judged" "$dir/verdict.txt" \
    || fail "hollow verdict: $(cat "$dir/verdict.txt" 2>/dev/null)"

# A configuration error: a mutant's build failed before compiling it (cargo
# refused the run itself). It is the gate's failure, named, not "unviable",
# even when every tier would otherwise pass.
dir=$(fixture config "$D:CaughtMutant:ok" "$D:CaughtMutant:ok" "$D:Unviable:config" "$A:CaughtMutant:ok")
if said=$(judge "$dir"); then fail "a mutant build's configuration error passed as unviable: $said"; fi
grep -q "1 mutant build(s) failed before the mutant was compiled" "$dir/verdict.txt" \
    || fail "config verdict: $(cat "$dir/verdict.txt" 2>/dev/null)"
grep -q "does not contain this feature" "$dir/verdict.txt" || fail "the config verdict names the error"
echo "ok   mutants-judge.selftest"
