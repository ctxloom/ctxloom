#!/usr/bin/env bash
# Self-test of the ratchet (owner ruling 165): ratchet-judge.sh on fixture
# scenario lists, then closeout-ratchet.sh in a fixture repository whose
# justfile has stub gates (its `test` writes a fixture list where
# GHERKIN_RATCHET says, and exits as told).
#
# The judge: a slice whose red set is a subset of the base's passes and
# counts what turned green; an equal set passes; a new red scenario fails; a
# broken green one fails, each named; a missing list fails. The closeout: a
# subset passes; a failure outside the feature runner (another test, another
# gate) fails though the list is a subset; a new red fails; on a base with no
# red scenario (main) one red scenario fails, as the plain closeout would;
# mutants-batch gets merge-base(base, HEAD) and GHERKIN_RATCHET_BASE (the
# base's list) when the base has red, and neither ratchet variable on main.
#
# The landing gate (`just land-ratchet <base>`: closeout-ratchet.sh
# --no-mutation <base>): the same gates and judge with no mutants-batch, even
# one RATCHET_GATES names, said as `land-ratchet`; a subset passes, a new red
# fails exactly as the closeout's does.
#
# The re-confirmation cache (target/ratchet/<sha>.reconfirm): a reproduced
# re-confirmation is cached and a later land on the same base runs it no
# more, only the ids not cached; a flaky one is not cached; a run killed
# during its re-confirmation leaves no cache.
set -euo pipefail
while IFS= read -r v; do unset "$v"; done < <(git rev-parse --local-env-vars)
here=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
fail() { echo "FAIL closeout-ratchet.selftest: $*" >&2; exit 1; }
list() { # <file> <red|green>:<id>...
    local file=$1 entry
    shift
    : > "$file"
    for entry in "$@"; do printf '%s\t%s\n' "${entry%%:*}" "${entry#*:}" >> "$file"; done
}
judge() { "$here/ratchet-judge.sh" "$tmp/base.list" "$tmp/slice.list" 2>&1; }

list "$tmp/base.list" "red:f/a.feature :: one" "red:f/a.feature :: two" "green:f/b.feature :: three"

# A subset passes, and counts what it turned green.
list "$tmp/slice.list" "red:f/a.feature :: one" "green:f/a.feature :: two" "green:f/b.feature :: three"
rc=0; out=$(judge) || rc=$?
[ "$rc" -eq 0 ] || fail "a subset was refused ($rc): $out"
grep -qx 'ok   ratchet (base 2 red; here 1 red, 1 turned green)' <<< "$out" || fail "subset line: $out"

# An equal set passes.
list "$tmp/slice.list" "red:f/a.feature :: one" "red:f/a.feature :: two" "green:f/b.feature :: three"
rc=0; out=$(judge) || rc=$?
[ "$rc" -eq 0 ] || fail "an equal set was refused ($rc): $out"
grep -qx 'ok   ratchet (base 2 red; here 2 red, 0 turned green)' <<< "$out" || fail "equal line: $out"

# A new red scenario (the base has no such scenario) fails, named.
list "$tmp/slice.list" "red:f/a.feature :: one" "red:f/c.feature :: four" "green:f/b.feature :: three"
rc=0; out=$(judge) || rc=$?
[ "$rc" -eq 1 ] || fail "a new red passed ($rc): $out"
grep -q '^FAIL ratchet: 1 new red scenarios: 0 broke green, 1 added red ' <<< "$out" || fail "new red line: $out"
grep -qx '  added red    f/c.feature :: four' <<< "$out" || fail "new red not named: $out"

# A broken green scenario fails, named.
list "$tmp/slice.list" "red:f/a.feature :: one" "red:f/b.feature :: three"
rc=0; out=$(judge) || rc=$?
[ "$rc" -eq 1 ] || fail "a broken green passed ($rc): $out"
grep -q '^FAIL ratchet: 1 new red scenarios: 1 broke green, 0 added red ' <<< "$out" || fail "broken green line: $out"
grep -qx '  broke green  f/b.feature :: three' <<< "$out" || fail "broken green not named: $out"
grep -q '(full list: [^;]*)$' <<< "$(grep '^FAIL' <<< "$out")" || fail "a why with no failures file: $out"

# The feature runner's failures file, beside the list, is named on the FAIL line.
printf 'red\tf/b.feature :: three\n  step: Then it holds\n  it did not\n\n' > "$tmp/slice.list.failures"
rc=0; out=$(judge) || rc=$?
[ "$rc" -eq 1 ] || fail "a broken green passed beside its failures ($rc): $out"
grep -q "^FAIL ratchet: .*(full list: $tmp/slice.list; why: $tmp/slice.list.failures)\$" <<< "$out" \
    || fail "the failures file is not named: $out"
rm -f "$tmp/slice.list.failures"

# The names are capped, with a tail.
reds=()
for i in $(seq 1 5); do reds+=("red:f/n.feature :: s$i"); done
list "$tmp/slice.list" "${reds[@]}"
rc=0; out=$(RATCHET_LIST_MAX=2 judge) || rc=$?
[ "$rc" -eq 1 ] || fail "five new reds passed"
[ "$(grep -c '^  added red' <<< "$out")" -eq 2 ] || fail "not capped at 2: $out"
grep -q '^  +3 more: ' <<< "$out" || fail "no tail: $out"

# A missing list fails: a runner that listed nothing has shown nothing.
rm -f "$tmp/slice.list"
rc=0; out=$(judge) || rc=$?
[ "$rc" -eq 1 ] || fail "a missing list passed"
grep -q '^FAIL ratchet: no scenario list ' <<< "$out" || fail "missing list line: $out"

# Re-confirmation (owner, 2026-11-14): each scenario whose colour differs
# from the base's list was run again on the base, alone and in its feature
# (`<alone><TAB><in its feature><TAB><id>`, the judge's third argument).
rjudge() { "$here/ratchet-judge.sh" "$tmp/base.list" "$tmp/slice.list" "$tmp/re.list" 2>&1; }
list "$tmp/base.list" "red:f/a.feature :: one" "red:f/a.feature :: two" "green:f/b.feature :: three"
# A new red the base reproduces green, alone and in its feature, is a new red.
list "$tmp/slice.list" "red:f/a.feature :: one" "red:f/a.feature :: two" "red:f/b.feature :: three"
printf 'green\tgreen\tf/b.feature :: three\n' > "$tmp/re.list"
rc=0; out=$(rjudge) || rc=$?
[ "$rc" -eq 1 ] || fail "a reproduced new red passed ($rc): $out"
grep -q '^FAIL ratchet: 1 new red scenarios: 1 broke green, 0 added red (' <<< "$out" || fail "reproduced new red line: $out"
! grep -q 'FLAKY' <<< "$out" || fail "a reproduced difference called flaky: $out"
# A turned green the base reproduces red keeps the base's red: it passes.
list "$tmp/slice.list" "red:f/a.feature :: one" "green:f/a.feature :: two" "green:f/b.feature :: three"
printf 'red\tred\tf/a.feature :: two\n' > "$tmp/re.list"
rc=0; out=$(rjudge) || rc=$?
[ "$rc" -eq 0 ] || fail "a reproduced turned green failed ($rc): $out"
grep -qx 'ok   ratchet (base 2 red; here 1 red, 1 turned green)' <<< "$out" || fail "reproduced turned green line: $out"
# A new red the base does not reproduce (red alone on the base, green listed)
# is FLAKY, with its reason, and fails: it never passes.
list "$tmp/slice.list" "red:f/a.feature :: one" "red:f/a.feature :: two" "red:f/b.feature :: three"
printf 'red\tgreen\tf/b.feature :: three\n' > "$tmp/re.list"
printf 'red\tf/b.feature :: three\n  step: Then it holds\n  it did not\n\n' > "$tmp/slice.list.failures"
rc=0; out=$(rjudge) || rc=$?
[ "$rc" -eq 1 ] || fail "a flaky new red passed ($rc): $out"
grep -q '^FAIL ratchet: 0 new red scenarios: 0 broke green, 0 added red, 1 flaky (' <<< "$out" || fail "flaky line: $out"
grep -qx 'FLAKY f/b.feature :: three (base: alone red, in its feature green, listed green; here red)' <<< "$out" \
    || fail "flaky not named: $out"
grep -qx '  why: Then it holds: it did not' <<< "$out" || fail "flaky reason: $out"
rm -f "$tmp/slice.list.failures"
# A turned green the base does not reproduce (its listed red is green on
# both re-runs) is FLAKY too, and fails.
list "$tmp/slice.list" "red:f/a.feature :: one" "green:f/a.feature :: two" "green:f/b.feature :: three"
printf 'green\tgreen\tf/a.feature :: two\n' > "$tmp/re.list"
rc=0; out=$(rjudge) || rc=$?
[ "$rc" -eq 1 ] || fail "a flaky turned green passed ($rc): $out"
grep -qx 'FLAKY f/a.feature :: two (base: alone green, in its feature green, listed red; here green)' <<< "$out" \
    || fail "flaky turned green not named: $out"
# A difference the re-confirmation did not run is FLAKY: nothing confirmed it.
: > "$tmp/re.list"
rc=0; out=$(rjudge) || rc=$?
[ "$rc" -eq 1 ] || fail "an unconfirmed difference passed ($rc): $out"
grep -qx 'FLAKY f/a.feature :: two (base: alone none, in its feature none, listed red; here green)' <<< "$out" \
    || fail "unconfirmed difference not named: $out"
# A slice with no difference needs no re-confirmation.
list "$tmp/slice.list" "red:f/a.feature :: one" "red:f/a.feature :: two" "green:f/b.feature :: three"
rc=0; out=$(rjudge) || rc=$?
[ "$rc" -eq 0 ] || fail "an equal set failed beside an empty re-confirmation ($rc): $out"
rm -f "$tmp/re.list"

# The closeout, in a fixture repository: base is a commit, HEAD a slice on it.
r=$tmp/r
git init -q "$r"
cat > "$r/justfile" <<'J'
build:
    @echo "ok   build (stub) 0.1s"
lint:
    @[ "${STUB_LINT:-0}" = 0 ] || { echo "FAIL lint: stub (full log: x)" >&2; exit 1; }; echo "ok   lint (stub) 0.1s"
test:
    @cp "$STUB_LIST" "$GHERKIN_RATCHET"; [ "${STUB_TEST_RC:-0}" = 0 ] || { echo "FAIL test: tests failed: 1 failed (full log: x)" >&2; exit "$STUB_TEST_RC"; }; echo "ok   test (stub) 0.1s"
mutants-batch base="":
    @echo "ok   mutants-batch (base=[{{base}}] ratchet=[${GHERKIN_RATCHET:-}] judged=[${GHERKIN_RATCHET_BASE:-}]) 0.1s"
lint-deps:
    @echo "ok   lint-deps (stub) 0.1s"
lint-os:
    @echo "ok   lint-os (stub) 0.1s"
J
git -C "$r" add justfile
git -C "$r" -c user.name=t -c user.email=t@t commit -qm base
fork=$(git -C "$r" rev-parse HEAD)
git -C "$r" branch integration
echo x > "$r/x"
git -C "$r" add x
git -C "$r" -c user.name=t -c user.email=t@t commit -qm slice
export GATES_STAGED_CACHE=$tmp/gates-cache
closeout() { (cd "$r" && env RATCHET_GATES="build lint test mutants-batch" RATCHET_BASE_LIST="$tmp/base.list" \
    STUB_LIST="$tmp/slice.list" "$@" "$here/closeout-ratchet.sh" integration 2>&1); }

# A subset passes; mutants-batch is given the fork point and the list's path.
list "$tmp/slice.list" "red:f/a.feature :: one" "green:f/a.feature :: two" "green:f/b.feature :: three"
rc=0; out=$(closeout) || rc=$?
[ "$rc" -eq 0 ] || fail "closeout of a subset exited $rc: $out"
grep -q '^ok   ratchet (base 2 red; here 1 red, 1 turned green)' <<< "$out" || fail "no ratchet line: $out"
grep -q "base=\[$fork\] ratchet=\[\] judged=\[$tmp/base.list\]" <<< "$out" || fail "mutants-batch args: $out"
grep -q '^ok   closeout-ratchet (4 gates; base [0-9a-f]\{7\}: 2 red, 1 turned green) ' <<< "$out" || fail "summary: $out"

# A failure outside the feature runner fails, though the list is a subset:
# another test (the test gate's own exit) ...
rc=0; out=$(closeout STUB_TEST_RC=100) || rc=$?
[ "$rc" -eq 1 ] || fail "a failing test outside the runner passed ($rc): $out"
grep -q '^ok   ratchet ' <<< "$out" || fail "the list was not judged beside the failure: $out"
grep -q '^FAIL closeout-ratchet: 1 failed: test ' <<< "$out" || fail "summary of another test: $out"
# ... and another gate.
rc=0; out=$(closeout STUB_LINT=1) || rc=$?
[ "$rc" -eq 1 ] || fail "a failing gate passed ($rc): $out"
grep -q '^FAIL closeout-ratchet: 1 failed: lint ' <<< "$out" || fail "summary of another gate: $out"
grep -q '^ok   mutants-batch' <<< "$out" || fail "a gate after the failing one did not run: $out"

# A new red fails the closeout.
list "$tmp/slice.list" "red:f/a.feature :: one" "red:f/b.feature :: three"
rc=0; out=$(closeout) || rc=$?
[ "$rc" -eq 1 ] || fail "closeout with a broken green passed: $out"
grep -q '^FAIL closeout-ratchet: 1 failed: ratchet ' <<< "$out" || fail "summary of a new red: $out"

# The closeout re-confirms each difference on the base (RATCHET_RECONFIRM, the
# seam for `ratchet-base.sh --reconfirm`: given the base, the ids, the
# output) and judges by what it found.
cat > "$tmp/reconfirm" <<'R'
#!/usr/bin/env bash
cp "$2" "$STUB_IDS_SEEN"
cp "$STUB_RECONFIRMED" "$3"
R
chmod +x "$tmp/reconfirm"
list "$tmp/slice.list" "red:f/a.feature :: one" "red:f/a.feature :: two" "red:f/b.feature :: three"
printf 'red\tgreen\tf/b.feature :: three\n' > "$tmp/reconfirmed"
rc=0; out=$(closeout RATCHET_RECONFIRM="$tmp/reconfirm" STUB_IDS_SEEN="$tmp/ids" STUB_RECONFIRMED="$tmp/reconfirmed") || rc=$?
[ "$rc" -eq 1 ] || fail "closeout of a flaky difference passed: $out"
grep -qx 'f/b.feature :: three' "$tmp/ids" || fail "the difference was not re-confirmed: $(cat "$tmp/ids")"
[ "$(grep -c '' "$tmp/ids")" -eq 1 ] || fail "more than the difference re-confirmed: $(cat "$tmp/ids")"
grep -q '^FLAKY f/b.feature :: three ' <<< "$out" || fail "closeout: flaky not named: $out"
# A flaky re-confirmation is not cached: the next land runs it again.
cache=$r/target/ratchet/$(git -C "$r" rev-parse integration).reconfirm
[ ! -e "$cache" ] || fail "a flaky re-confirmation was cached: $(cat "$cache")"
printf 'green\tgreen\tf/b.feature :: three\n' > "$tmp/reconfirmed"
rc=0; out=$(closeout RATCHET_RECONFIRM="$tmp/reconfirm" STUB_IDS_SEEN="$tmp/ids" STUB_RECONFIRMED="$tmp/reconfirmed") || rc=$?
[ "$rc" -eq 1 ] || fail "closeout of a reproduced new red passed: $out"
grep -q '^FAIL ratchet: 1 new red scenarios: 1 broke green, 0 added red (' <<< "$out" || fail "closeout reproduced: $out"

# A reproduced re-confirmation is cached by the base's sha, and a later land
# on the same base runs it no more: the cached colours are judged as found.
[ "$(cat "$cache" 2>/dev/null)" = "$(printf 'green\tgreen\tf/b.feature :: three')" ] \
    || fail "the reproduced re-confirmation was not cached: $(cat "$cache" 2>/dev/null)"
printf '#!/usr/bin/env bash\nexit 1\n' > "$tmp/reconfirm-fails"
chmod +x "$tmp/reconfirm-fails"
rc=0; out=$(closeout RATCHET_RECONFIRM="$tmp/reconfirm-fails") || rc=$?
[ "$rc" -eq 1 ] || fail "a cached reproduced new red passed: $out"
! grep -q 'could not re-confirm' <<< "$out" || fail "a cached id was re-confirmed again: $out"
grep -q '^FAIL ratchet: 1 new red scenarios: 1 broke green, 0 added red (' <<< "$out" || fail "cached verdict: $out"
grep -q '^note ratchet: 1 of the 1 scenarios whose colour differs were re-confirmed on ' <<< "$out" || fail "cache note: $out"
# With a second difference only the one not cached is re-confirmed, and its
# colours join the cache.
list "$tmp/slice.list" "red:f/a.feature :: one" "green:f/a.feature :: two" "red:f/b.feature :: three"
printf 'red\tred\tf/a.feature :: two\n' > "$tmp/reconfirmed"
rc=0; out=$(closeout RATCHET_RECONFIRM="$tmp/reconfirm" STUB_IDS_SEEN="$tmp/ids" STUB_RECONFIRMED="$tmp/reconfirmed") || rc=$?
[ "$(cat "$tmp/ids")" = 'f/a.feature :: two' ] || fail "the cached id was re-confirmed beside the new one: $(cat "$tmp/ids")"
grep -q '^FAIL ratchet: 1 new red scenarios: 1 broke green, 0 added red (' <<< "$out" || fail "mixed verdict: $out"
grep -q $'^red\tred\tf/a.feature :: two$' "$cache" || fail "the new re-confirmation was not cached: $(cat "$cache")"
grep -q $'^green\tgreen\tf/b.feature :: three$' "$cache" || fail "the cache lost its earlier line: $(cat "$cache")"
# A run killed during its re-confirmation leaves no cache: the next one
# re-confirms again.
rm -f "$cache" "$cache.failures"
cat > "$tmp/reconfirm-killed" <<'R'
#!/usr/bin/env bash
cp "$STUB_RECONFIRMED" "$3"
kill -KILL "$PPID"
R
chmod +x "$tmp/reconfirm-killed"
list "$tmp/slice.list" "red:f/a.feature :: one" "red:f/a.feature :: two" "red:f/b.feature :: three"
printf 'green\tgreen\tf/b.feature :: three\n' > "$tmp/reconfirmed"
{ closeout RATCHET_RECONFIRM="$tmp/reconfirm-killed" STUB_RECONFIRMED="$tmp/reconfirmed"; } > /dev/null 2>&1 || true
[ ! -e "$cache" ] || fail "an interrupted re-confirmation was cached: $(cat "$cache")"
rm -f "$tmp/ids"
rc=0; out=$(closeout RATCHET_RECONFIRM="$tmp/reconfirm" STUB_IDS_SEEN="$tmp/ids" STUB_RECONFIRMED="$tmp/reconfirmed") || rc=$?
[ "$(cat "$tmp/ids" 2>/dev/null)" = 'f/b.feature :: three' ] || fail "after an interrupted run the id was not re-confirmed: $out"
rm -f "$cache" "$cache.failures"
# A re-confirmation that cannot run fails the ratchet, saying so.
rc=0; out=$(closeout RATCHET_RECONFIRM="$tmp/reconfirm-fails") || rc=$?
[ "$rc" -eq 1 ] || fail "a failed re-confirmation passed: $out"
grep -q '^FAIL ratchet: the base could not re-confirm' <<< "$out" || fail "failed re-confirmation line: $out"

# On main, the base has no red scenario: the ratchet is the plain closeout.
list "$tmp/base.list" "green:f/a.feature :: one" "green:f/b.feature :: three"
list "$tmp/slice.list" "green:f/a.feature :: one" "green:f/b.feature :: three"
rc=0; out=$(closeout) || rc=$?
[ "$rc" -eq 0 ] || fail "an all-green slice on an all-green base failed: $out"
grep -q "base=\[$fork\] ratchet=\[\] judged=\[\]" <<< "$out" || fail "mutants-batch on an all-green base was given a ratchet: $out"
list "$tmp/slice.list" "red:f/a.feature :: one" "green:f/b.feature :: three"
rc=0; out=$(closeout) || rc=$?
[ "$rc" -eq 1 ] || fail "a red scenario passed on an all-green base: $out"

# Usage: no base is exit 2.
rc=0; out=$( (cd "$r" && "$here/closeout-ratchet.sh" 2>&1) ) || rc=$?
[ "$rc" -eq 2 ] || fail "no base exited $rc: $out"

# The landing gate: no mutation, the ratchet judged as the closeout judges it.
land() { (cd "$r" && env RATCHET_BASE_LIST="$tmp/base.list" STUB_LIST="$tmp/slice.list" "$@" \
    "$here/closeout-ratchet.sh" --no-mutation integration 2>&1); }
list "$tmp/base.list" "red:f/a.feature :: one" "red:f/a.feature :: two" "green:f/b.feature :: three"
list "$tmp/slice.list" "red:f/a.feature :: one" "green:f/a.feature :: two" "green:f/b.feature :: three"
rc=0; out=$(land) || rc=$?
[ "$rc" -eq 0 ] || fail "land-ratchet of a subset exited $rc: $out"
! grep -q 'mutants-batch' <<< "$out" || fail "land-ratchet ran mutation: $out"
grep -q '^ok   lint-deps ' <<< "$out" || fail "land-ratchet skipped lint-deps: $out"
grep -q '^ok   lint-os ' <<< "$out" || fail "land-ratchet skipped lint-os: $out"
grep -q '^ok   ratchet (base 2 red; here 1 red, 1 turned green)' <<< "$out" || fail "land-ratchet: no ratchet line: $out"
grep -q '^ok   land-ratchet (5 gates; base [0-9a-f]\{7\}: 2 red, 1 turned green) ' <<< "$out" || fail "land-ratchet summary: $out"
# Even a gate list naming mutants-batch runs none.
rc=0; out=$(land RATCHET_GATES="build test mutants-batch") || rc=$?
[ "$rc" -eq 0 ] || fail "land-ratchet with mutants-batch named exited $rc: $out"
! grep -q 'mutants-batch' <<< "$out" || fail "land-ratchet ran a named mutants-batch: $out"
grep -q '^ok   land-ratchet (2 gates; ' <<< "$out" || fail "land-ratchet summary of a named list: $out"
# A new red fails it, as it fails the closeout.
list "$tmp/slice.list" "red:f/a.feature :: one" "red:f/b.feature :: three"
rc=0; out=$(land) || rc=$?
[ "$rc" -eq 1 ] || fail "land-ratchet with a broken green passed: $out"
grep -q '^FAIL ratchet: 1 new red scenarios: 1 broke green, 0 added red ' <<< "$out" || fail "land-ratchet new red line: $out"
grep -q '^FAIL land-ratchet: 1 failed: ratchet ' <<< "$out" || fail "land-ratchet summary of a new red: $out"
# Its usage is its own.
rc=0; out=$( (cd "$r" && "$here/closeout-ratchet.sh" --no-mutation 2>&1) ) || rc=$?
[ "$rc" -eq 2 ] || fail "land-ratchet with no base exited $rc: $out"
grep -q '^FAIL land-ratchet: usage: land-ratchet <base>' <<< "$out" || fail "land-ratchet usage: $out"
# The end of a landing sweeps what it grew, pass or fail, and keeps the
# cached lists (the sweep's own cases: land-sweep.selftest.sh).
grow() {
    mkdir -p "$r/target/ratchet-base/debug" "$r/target/debug/incremental/c-1" "$r/target/ratchet"
    echo x > "$r/target/ratchet-base/debug/f"; echo x > "$r/target/debug/incremental/c-1/f"
    echo red > "$r/target/ratchet/keep.list"
}
grow
list "$tmp/slice.list" "red:f/a.feature :: one" "green:f/a.feature :: two" "green:f/b.feature :: three"
rc=0; out=$(land) || rc=$?
[ "$rc" -eq 0 ] || fail "a swept landing exited $rc: $out"
grep -q '^ok   sweep (ratchet-base) freed ' <<< "$out" || fail "no ratchet-base sweep line: $out"
grep -q '^ok   sweep (incremental, 1 dirs) freed ' <<< "$out" || fail "no incremental sweep line: $out"
[ ! -e "$r/target/ratchet-base" ] || fail "target/ratchet-base is still there after a landing"
[ ! -e "$r/target/debug/incremental" ] || fail "the incremental directory is still there after a landing"
[ -s "$r/target/ratchet/keep.list" ] || fail "the landing swept target/ratchet's cached list"
grow
list "$tmp/slice.list" "red:f/a.feature :: one" "red:f/b.feature :: three"
rc=0; out=$(land) || rc=$?
[ "$rc" -eq 1 ] || fail "a failed swept landing exited $rc, not its verdict 1: $out"
[ ! -e "$r/target/ratchet-base" ] || fail "target/ratchet-base is still there after a failed landing"
# A sweep that fails is said, and the verdict is the landing's.
list "$tmp/slice.list" "red:f/a.feature :: one" "green:f/a.feature :: two" "green:f/b.feature :: three"
printf '#!/bin/sh\necho "FAIL sweep (stub): broke"; exit 7\n' > "$tmp/bad-sweep"; chmod +x "$tmp/bad-sweep"
rc=0; out=$(land RATCHET_SWEEP="$tmp/bad-sweep") || rc=$?
[ "$rc" -eq 0 ] || fail "a failing sweep changed the verdict to $rc: $out"
grep -q '^FAIL sweep (stub): broke' <<< "$out" || fail "a failing sweep was not said: $out"
echo "ok   closeout-ratchet.selftest"
