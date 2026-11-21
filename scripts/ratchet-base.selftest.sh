#!/usr/bin/env bash
# scripts/ratchet-base.sh reads the first commit that taught the runner from
# a `git log` whose output can outrun what a reader that stops at one line
# takes: with `head -n 1` git log died of SIGPIPE, and under pipefail and
# set -e the script exited 141 without a word (2026-11-15). The fixture
# forces it: a history whose matching log is far longer than a pipe buffer
# (64 KiB), so git log is still writing when its reader stops, every time.
# Its base is a side commit the teacher does not apply to, so the script
# ends at its own named refusal, before any build: exit 1 and a `FAIL
# ratchet-base:` line, never a silent 141.
set -euo pipefail
while IFS= read -r v; do unset "$v"; done < <(git rev-parse --local-env-vars)
here=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
fail() { echo "FAIL ratchet-base.selftest: $*" >&2; exit 1; }
# The script takes a test lock of its own, never the machine's
# (gates/tests-lock.sh), and is inside no other run's.
export TESTS_LOCK_FILE=$tmp/workspace-tests.lock
unset GATES_TESTS_LOCK_HELD

runner=crates/root/bin/ctxloom/src/gherkin_features.test.rs
fx=$tmp/fx
git init --quiet "$fx"
git -C "$fx" config user.name selftest
git -C "$fx" config user.email selftest@example.invalid
commits=8000
# Every commit on main toggles a GHERKIN_RATCHET line in the runner, so each
# is a `-S GHERKIN_RATCHET` match: 8000 hashes, about 320 KiB of log.
{
    off=$'line\n'
    on=$'line\nGHERKIN_RATCHET\n'
    for i in $(seq 0 "$commits"); do
        if [ "$i" -eq 0 ] || [ $((i % 2)) -eq 0 ]; then body=$off; else body=$on; fi
        printf 'commit refs/heads/main\ncommitter s <s@example.invalid> %d +0000\ndata 2\nc\n' "$((1700000000 + i))"
        printf 'M 100644 inline %s\ndata %d\n%s\n' "$runner" "${#body}" "$body"
    done
    # The base: a side commit whose runner the teacher's patch cannot reach.
    other=$'other\n'
    printf 'commit refs/heads/base\ncommitter s <s@example.invalid> 1600000000 +0000\ndata 2\nb\n'
    printf 'M 100644 inline %s\ndata %d\n%s\n' "$runner" "${#other}" "$other"
} | git -C "$fx" fast-import --quiet
git -C "$fx" checkout --quiet main
[ "$(git -C "$fx" log --format=%H -S GHERKIN_RATCHET main -- "$runner" | wc -c)" -gt 131072 ] \
    || fail "the fixture's matching log is not longer than two pipe buffers"

rc=0
(cd "$fx" && "$here/ratchet-base.sh" base) > "$tmp/out" 2> "$tmp/err" || rc=$?
[ "$rc" -ne 141 ] || fail "ratchet-base exited 141 (SIGPIPE) without a word, reading the first teacher commit"
[ "$rc" -eq 1 ] || fail "ratchet-base exited $rc, not 1: $(cat "$tmp/err")"
grep -q '^FAIL ratchet-base: .* does not apply to it' "$tmp/err" \
    || fail "ratchet-base did not name why it stopped: $(cat "$tmp/err")"
echo "ok   ratchet-base.selftest (a teacher found past a full pipe)"
