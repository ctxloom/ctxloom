#!/bin/sh
# The -exec wrapper that caps a gremlins mutant's test timeout. `go test`
# invokes it as
#   gremlins_exec.sh <test binary> <test flags...>
# for every test binary it runs, and it runs the binary — with -test.timeout
# lowered when the run is a gremlins MUTANT run whose budget exceeds the cap.
#
# WHY: gremlins has no absolute timeout. Each mutant gets the coverage pass's
# duration (B) times .gremlins.yaml's timeout-coefficient, handed to `go test`
# as -timeout coef*B+2s. A multiplier is right for a fast package (see that
# file) and enormous for a slow one: a diff run's coverage pass of 140.7s gave
# every termui mutant 1h10m23s, and a mutant that deadlocks a test held its
# worker that whole time. gremlins' own deadline cannot be shortened without
# modifying gremlins, but the test binary's can, and a test binary that hits
# -test.timeout panics, which ends the `go test`.
#
# THE RULE, with coef, CAP and K from .gremlins.yaml (exported by
# mutation_tmp.sh) and B = (T - 2s) / coef recovered from the timeout T
# gremlins passed:
#   effective = min(T, max(CAP, K * B))
# CAP bounds the slow end; K * B keeps the cap from ever starving a package
# slower than it expects; min(T, ...) leaves a fast package's coef*B alone.
#
# VERDICT: a capped mutant is scored KILLED, not TIMED OUT. The test binary
# panics ("test timed out"), `go test` exits 1, and gremlins maps exit 1 to
# KILLED; only its own context deadline yields TIMED OUT. That is the right
# verdict for a mutant that hangs the suite: it was detected.
#
# A MUTANT RUN is recognised by gremlins' own flags: it passes -failfast and
# -timeout and no coverprofile. Its coverage pass carries -test.coverprofile,
# and nothing else in this repository runs `go test -failfast`. Everything
# else, and everything when the cap is not configured, runs unchanged.
set -u

case " $* " in
*" -test.failfast=true "*) ;;
*) exec "$@" ;;
esac
case " $* " in
*" -test.coverprofile="*) exec "$@" ;;
esac
if [ -z "${CTXLOOM_MUTATION_TIMEOUT_COEF:-}" ] || [ -z "${CTXLOOM_MUTATION_TIMEOUT_CAP:-}" ] ||
    [ -z "${CTXLOOM_MUTATION_TIMEOUT_FLOOR:-}" ]; then
    exec "$@"
fi

bin=$1
shift
timeout=""
for a in "$@"; do
    case "$a" in -test.timeout=*) timeout=${a#-test.timeout=} ;; esac
done
[ -n "$timeout" ] || exec "$bin" "$@"

# Prints the capped timeout in whole seconds, or nothing when T stands.
capped=$(LC_ALL=C awk -v t="$timeout" -v coef="$CTXLOOM_MUTATION_TIMEOUT_COEF" \
    -v cap="$CTXLOOM_MUTATION_TIMEOUT_CAP" -v k="$CTXLOOM_MUTATION_TIMEOUT_FLOOR" '
    # A Go time.Duration string, in seconds; -1 when unreadable.
    function seconds(s,   total, num, u) {
        total = 0
        if (s == "0") return 0
        while (s != "") {
            if (!match(s, /^[0-9]+(\.[0-9]+)?/)) return -1
            num = substr(s, 1, RLENGTH) + 0
            s = substr(s, RLENGTH + 1)
            if (substr(s, 1, 2) == "ns") { total += num / 1e9; s = substr(s, 3) }
            else if (substr(s, 1, 2) == "us") { total += num / 1e6; s = substr(s, 3) }
            else if (substr(s, 1, 3) == "\302\265s") { total += num / 1e6; s = substr(s, 4) }
            else if (substr(s, 1, 2) == "ms") { total += num / 1e3; s = substr(s, 3) }
            else if (substr(s, 1, 1) == "s") { total += num; s = substr(s, 2) }
            else if (substr(s, 1, 1) == "m") { total += num * 60; s = substr(s, 2) }
            else if (substr(s, 1, 1) == "h") { total += num * 3600; s = substr(s, 2) }
            else return -1
        }
        return total
    }
    BEGIN {
        T = seconds(t)
        if (T <= 0 || coef <= 0) exit
        B = (T - 2) / coef
        if (B < 0) B = 0
        floor = k * B
        eff = (cap > floor) ? cap : floor
        e = int(eff); if (e < eff) e++
        if (e < T) printf "%d\n", e
    }')

[ -n "$capped" ] || exec "$bin" "$@"

for a in "$@"; do
    shift
    case "$a" in
    -test.timeout=*) set -- "$@" "-test.timeout=${capped}s" ;;
    *) set -- "$@" "$a" ;;
    esac
done
exec "$bin" "$@"
