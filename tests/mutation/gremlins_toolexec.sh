#!/bin/sh
# The package lane's -toolexec wrapper: `go` invokes it as
#   gremlins_toolexec.sh <tool path> <tool args...>
# for every toolchain program it runs. It runs the tool unchanged, and when a
# COMPILE or VET fails it records that the `go` command which invoked it built
# nothing a test could run.
#
# WHY A TOOLEXEC WRAPPER: gremlins v0.6.0 runs `go test` per mutant, discards
# its output, and keeps only the exit code — 1 is KILLED, 2 NOT VIABLE — and
# `go test` exits 1 on a build failure. So a mutant that does not compile is
# scored as a kill, and nothing gremlins prints or reports (its JSON carries
# only type, status and position) can tell the two apart. What it does pass
# through is its environment, so GOFLAGS=-toolexec reaches every mutant's
# build. The alternatives were rejected: shimming `go` on PATH, and modifying
# gremlins.
#
# One `go` command is one mutant, so the record is keyed by the parent PID —
# a package compiled in two variants for one test run is still one mutant.
# The harness (packageMutationTarget.release) turns each record into a
# `gremlins-invalid-mutant:` line.
#
# The tool's exit status and stderr pass through untouched: gremlins' verdict
# must not move, because survivor counts and their baselines gate on it.
set -u

case "${1##*/}" in
compile | vet) ;;
*) exec "$@" ;;
esac

err=$(mktemp) || exit 1
"$@" 2>"$err"
status=$?
cat "$err" >&2

if [ "$status" -ne 0 ] && [ -n "${CTXLOOM_GREMLINS_INVALID_DIR:-}" ]; then
    # Write-then-rename: two variants failing at once must not interleave.
    rec="$CTXLOOM_GREMLINS_INVALID_DIR/$PPID"
    { printf '%s: ' "${1##*/}"; grep -m1 . "$err" || echo "exit $status"; } >"$rec.$$"
    mv -f "$rec.$$" "$rec"
fi
rm -f "$err"
exit "$status"
