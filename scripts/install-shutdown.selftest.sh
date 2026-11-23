#!/usr/bin/env bash
# scripts/install-shutdown.sh, as `just install` and `install-windows` run
# it before they replace the installed binary (OD-62; owner ruling
# 2026-11-16, "flush before install"), over a fake `ctxloom` that exits as
# FAKE_RC says: 0 goes on (nothing runs); a failure (32) goes on, said; a
# decline (48-63: held facts no daemon took, or a time that is no span)
# refuses, exit 1, so the binary is not replaced. The fake's stdout is
# prefixed `ok   shutdown: `, its stderr passed through as it is.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
script=$here/install-shutdown.sh
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
fail() { echo "FAIL install-shutdown.selftest: $*" >&2; exit 1; }

fake=$tmp/ctxloom
cat > "$fake" <<'S'
#!/usr/bin/env bash
echo "home $CTXLOOM_DAEMON_ROOT args $*"
echo "a step"
echo "ctxloom: what shutdown said" >&2
exit "${FAKE_RC:-0}"
S
chmod +x "$fake"

# run <rc> <when>: the script's exit, its stdout and its stderr.
run() {
    set +e
    FAKE_RC=$1 "$script" "$fake" "$tmp/home" "$2" > "$tmp/out" 2> "$tmp/err"
    got=$?
    set -e
}

run 0 ""
[ "$got" = 0 ] || fail "exit 0 went on: got $got"
grep -qx "ok   shutdown: home $tmp/home args shutdown --format text" "$tmp/out" || fail "the home and no time: $(cat "$tmp/out")"
grep -qx "ok   shutdown: a step" "$tmp/out" || fail "stdout prefixed: $(cat "$tmp/out")"
grep -qx "ok   nothing of ctxloom runs" "$tmp/out" || fail "done said: $(cat "$tmp/out")"
grep -qx "ctxloom: what shutdown said" "$tmp/err" || fail "stderr as it is: $(cat "$tmp/err")"
grep -q "^ok" "$tmp/err" && fail "an error line said ok: $(cat "$tmp/err")"

run 0 "now"
grep -qx "ok   shutdown: home $tmp/home args shutdown now --format text" "$tmp/out" || fail "the time passed: $(cat "$tmp/out")"

run 32 "now"
[ "$got" = 0 ] || fail "a failure (32) goes on: got $got"
grep -q "^note some of ctxloom still runs" "$tmp/out" || fail "a failure said: $(cat "$tmp/out")"

for rc in 48 49 63; do
    run "$rc" "now"
    [ "$got" = 1 ] || fail "a decline ($rc) refuses: got $got"
    grep -q "^FAIL install: " "$tmp/err" || fail "a decline ($rc) said FAIL: $(cat "$tmp/err")"
    grep -q "not replaced" "$tmp/err" || fail "the binary kept said ($rc): $(cat "$tmp/err")"
    grep -q "nothing of ctxloom runs" "$tmp/out" && fail "a decline ($rc) said done"
done

echo "ok   install-shutdown.selftest"
