#!/usr/bin/env bash
# Before `just install` or `install-windows` replaces the installed binary
# (OD-62: one ctxloom version runs at a time), the NEW binary asks every
# running ctxloom of the home to stop: `ctxloom shutdown [when]`, its stdout
# said as `ok   shutdown: ...` lines, its stderr (an error and its fix) as
# it is.
#
# Owner ruling 2026-11-16 ("flush before install"): shutdown delivers the
# home's holds through the daemon, or confirms they are empty, and a hold it
# could not deliver is a decline; the binary is then NOT replaced (a hold
# an older binary wrote carries type URLs a newer daemon may not know; the
# accepted cost, a stuck hold blocks an install).
#
#   exit 0: install. Everything stopped (shutdown's 0), or something still
#           runs (any other code, say 32): said, and the install goes on;
#   exit 1: do not install. Shutdown declined (48-63): held facts no daemon
#           took, or a time that is no span; said with a FAIL line.
#
# Usage: install-shutdown.sh <built binary> <ctxloom home> [when]
set -uo pipefail
built=$1 home=$2 when=${3:-}
CTXLOOM_HOME="$home" "$built" shutdown ${when:+"$when"} --format text | sed 's/^/ok   shutdown: /'
rc=${PIPESTATUS[0]}
case "$rc" in
    0) echo "ok   nothing of ctxloom runs" ;;
    4[89]|5[0-9]|6[0-3])
        echo "FAIL install: \`ctxloom shutdown\` declined (said above, with its fix); the installed binary is not replaced" >&2
        echo "  fix: deliver what is held (\`ctxloom doctor\` names it), or give a time that is a span, then install again" >&2
        exit 1
        ;;
    *) echo "note some of ctxloom still runs (said above); installing anyway" ;;
esac
