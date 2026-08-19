#!/usr/bin/env bash
# The SURVIVOR RATCHET for the mutation-vs-cucumber gate.
#
# Usage: survivor_ratchet.sh <baseline-file> <run-log>
#
# THE INVARIANT: a mutation run that left MORE mechanisms unverified than the
# recorded baseline must not exit 0. The guard beside it in the recipe refuses
# a run that produced no score — one that never looked. This one refuses a run
# that looked and found things worse than they were. Without it the gate
# cannot fail on a result at all: the harness releases with
# WithMinimumThreshold(0), deliberately, so that no threshold is ever chosen
# before a measurement exists, which leaves regression as the only thing that
# can legitimately red it.
#
# ATTRIBUTION is per TARGET, never aggregate: one number for a whole run lets
# an improvement in one target mask a regression in another, and each target
# mutates a different file with a different suite behind it.
#
# A target is identified by the `ooze-target: <test name>` marker the harness
# prints to stdout immediately before releasing ooze. Marker and summary box
# travel the same stream from the same goroutine, and ooze summarizes in a
# t.Cleanup that runs before the next target's marker — so the box that
# follows a marker is that target's, whatever order `go test` chooses to flush
# its own bookkeeping in. Nothing here reads `=== RUN` or `--- PASS`.
#
# CTXLOOM_MUTATION_BASELINE=update rewrites the rows this run covered, marking
# them `measured`, instead of judging them. That is how a `recorded`, `stale`
# or `unknown` row becomes a real one.
set -uo pipefail

if [ "$#" -ne 2 ]; then
    echo "usage: $0 <baseline-file> <run-log>" >&2
    exit 2
fi
baseline=$1
log=$2

for f in "$baseline" "$log"; do
    if [ ! -r "$f" ]; then
        echo "error: cannot read $f" >&2
        exit 2
    fi
done

# --- what the run measured -------------------------------------------------
# Emits one "target total killed survived" line per marker, plus a final
# "#orphans N" counting summary boxes that arrived with no marker to own them
# (which would mean the harness stopped announcing its targets, and that no
# measurement in this log can be attributed to anything).
measured=$(awk '
    function num(s,   t) { t = s; sub(/^[^0-9]*/, "", t); sub(/[^0-9].*$/, "", t); return t + 0 }
    BEGIN { esc = sprintf("%c", 27); cur = ""; n = 0; orphans = 0 }
    { line = $0; gsub(esc "\\[[0-9;]*[a-zA-Z]", "", line) }
    line ~ /^ooze-target: / {
        cur = line
        sub(/^ooze-target:[ \t]*/, "", cur)
        sub(/[ \t\r]+$/, "", cur)
        if (!(cur in seen)) {
            seen[cur] = 1; order[++n] = cur
            total[cur] = -1; killed[cur] = -1; surv[cur] = -1
        }
        next
    }
    index(line, "\xe2\x80\xa2 Total:")    { if (cur == "") orphans++;    else total[cur]  = num(line); next }
    index(line, "\xe2\x80\xa2 Killed:")   { if (cur != "") killed[cur] = num(line); next }
    index(line, "\xe2\x80\xa2 Survived:") { if (cur != "") surv[cur]   = num(line); next }
    END {
        for (i = 1; i <= n; i++) {
            k = order[i]
            printf "%s %d %d %d\n", k, total[k], killed[k], surv[k]
        }
        printf "#orphans %d\n", orphans
    }
' "$log")

orphans=$(sed -n 's/^#orphans //p' <<<"$measured")
measured=$(grep -v '^#orphans ' <<<"$measured")

if [ -z "$measured" ]; then
    echo "error: the run named no mutation target — nothing in this log can be attributed." >&2
    echo "       The harness prints 'ooze-target: <test name>' to stdout before every" >&2
    echo "       release; ${orphans:-0} summary box(es) arrived without one. Do not read" >&2
    echo "       this as a pass." >&2
    exit 1
fi
if [ "${orphans:-0}" -ne 0 ]; then
    echo "error: $orphans mutation summary box(es) belong to no announced target." >&2
    echo "       A score that cannot be attributed cannot be ratcheted. Do not read" >&2
    echo "       this as a pass." >&2
    exit 1
fi

# --- what the baseline records ---------------------------------------------
declare -A base_total base_surv base_prov
while read -r name btotal bsurv bprov; do
    [ -n "${name:-}" ] || continue
    case "$name" in \#*) continue ;; esac
    base_total[$name]=$btotal
    base_surv[$name]=$bsurv
    base_prov[$name]=$bprov
done < <(grep -vE '^[[:space:]]*(#|$)' "$baseline")

update=0
[ "${CTXLOOM_MUTATION_BASELINE:-}" = "update" ] && update=1

failed=0
declare -A new_total new_surv
notes=()

while read -r name mtotal mkilled msurv; do
    [ -n "${name:-}" ] || continue
    if [ "$mtotal" -lt 0 ] || [ "$msurv" -lt 0 ]; then
        echo "error: $name announced itself and produced no summary — it measured NOTHING." >&2
        failed=1
        continue
    fi
    if [ "$mtotal" -eq 0 ]; then
        echo "error: $name produced ZERO mutants. The scoping or the virus matched no code," >&2
        echo "       so a clean report here is about an empty mutant set." >&2
        failed=1
        continue
    fi

    new_total[$name]=$mtotal
    new_surv[$name]=$msurv

    prov=${base_prov[$name]:-}
    if [ -z "$prov" ]; then
        if [ "$update" -eq 1 ]; then
            notes+=("RECORDED  $name: $msurv survivors of $mtotal (new row)")
            continue
        fi
        echo "error: $name has no row in $baseline — an unbaselined measurement is not" >&2
        echo "       a pass, because nothing can say whether it got worse. Add a row, or" >&2
        echo "       re-run with CTXLOOM_MUTATION_BASELINE=update." >&2
        failed=1
        continue
    fi

    if [ "$update" -eq 1 ]; then
        notes+=("RECORDED  $name: $msurv survivors of $mtotal (was ${base_surv[$name]} of ${base_total[$name]}, $prov)")
        continue
    fi

    if [ "$prov" = unknown ]; then
        notes+=("UNBASELINED  $name: $msurv survivors of $mtotal. No count was ever recorded for this target,")
        notes+=("             so this run is neither a pass nor a failure on it. Record it with")
        notes+=("             CTXLOOM_MUTATION_BASELINE=update.")
        continue
    fi

    if [ "$msurv" -gt "${base_surv[$name]}" ]; then
        echo "error: $name REGRESSED: ${base_surv[$name]} survivors -> $msurv." >&2
        echo "       Mutant set: ${base_total[$name]} -> $mtotal. If the mutant set grew, the new" >&2
        echo "       code arrived with no scenario that kills it; if it did not, coverage" >&2
        echo "       that existed has stopped verifying something." >&2
        echo "       Kill the new survivors, or raise the row in $baseline in this same" >&2
        echo "       change with the reason — a raise is a decision, not a formality." >&2
        failed=1
        continue
    fi

    if [ "$msurv" -lt "${base_surv[$name]}" ]; then
        notes+=("IMPROVED  $name: ${base_surv[$name]} survivors -> $msurv. Lower the row in $baseline in this")
        notes+=("          same change, or the ratchet banks the slack instead of tracking the debt down.")
        continue
    fi

    notes+=("HELD  $name: $msurv survivors of $mtotal (baseline ${base_surv[$name]}, $prov)")
done <<<"$measured"

if [ "$update" -eq 1 ] && [ "$failed" -eq 0 ] && [ "${#new_total[@]}" -ne 0 ]; then
    # Rewrite each covered row IN PLACE, so the file's order and its header
    # survive; a target with no row yet is appended.
    pending=$(mktemp)
    for name in "${!new_total[@]}"; do
        printf '%s %s %s\n' "$name" "${new_total[$name]}" "${new_surv[$name]}"
    done | sort > "$pending"
    tmp=$(mktemp "${baseline}.XXXXXX")
    awk -v pending="$pending" '
        BEGIN {
            while ((getline line < pending) > 0) {
                split(line, f, " ")
                nt[f[1]] = f[2]; ns[f[1]] = f[3]
            }
            close(pending)
        }
        /^[[:space:]]*(#|$)/ { print; next }
        {
            if ($1 in nt) {
                printf "%-42s %5d %9d  measured\n", $1, nt[$1], ns[$1]
                done[$1] = 1
                next
            }
            print
        }
        END {
            for (k in nt) if (!(k in done)) printf "%-42s %5d %9d  measured\n", k, nt[k], ns[k]
        }
    ' "$baseline" > "$tmp"
    mv "$tmp" "$baseline"
    rm -f "$pending"
    echo
    echo "=== survivor ratchet (BASELINE UPDATED) ==="
else
    echo
    echo "=== survivor ratchet ==="
fi

for n in "${notes[@]:-}"; do
    [ -n "$n" ] && echo "$n"
done

exit "$failed"
