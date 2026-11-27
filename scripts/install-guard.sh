#!/usr/bin/env bash
# Run first by `just install` and `install-windows`: refuse inside a ctxloom
# session (2026-11-27). The install's `ctxloom shutdown` stops every
# interactive frontend of the home, the session that ran the install
# included (it did, twice), and in a session $HOME is the session home, so
# shutdown would also name the wrong ctxloom home and decline
# `ctxloom-wiring-daemon-other-home`. A session is told by CTXLOOM_SESSION,
# set and non-empty. Nothing is built or installed.
#
#   exit 0:  not in a session, go on;
#   exit 48: a decline (the class of a refusal), said as a FAIL line and a fix.
set -uo pipefail
if [ -n "${CTXLOOM_SESSION:-}" ]; then
    echo "FAIL install: refused inside a ctxloom session (CTXLOOM_SESSION=$CTXLOOM_SESSION): the install stops every running ctxloom, this session with it, and the home here is the session home; nothing was built or installed" >&2
    echo "  fix: run \`just install\` from your own terminal, or prefix it with \`!\` in the agent prompt (\`! just install\`)" >&2
    exit 48
fi
