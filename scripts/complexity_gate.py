#!/usr/bin/env python3
"""Ratchet gate for `just complexity-check`: fail only on NEW cyclomatic-complexity
violations, not the pre-existing 282 (see .complexity-baseline.txt).

Why this exists: `lizard -C 10 .` alone reports every function over CCN 10 and exits
non-zero if there is at least one. Baselined against "just don't have MORE than N",
a bare count is gameable — fix one violation, introduce a worse one elsewhere, and the
total is unchanged. This script tracks each violation by IDENTITY (not a count), and
also tracks each identity's CCN, so both "a new function crossed the line" and "an
already-violating function got worse" fail the gate; only fixing violations (or leaving
them exactly as they are) keeps it green.

Identity: (file, long_name, occurrence). `long_name` is lizard's parameter/receiver
signature string (e.g. "(llmRenameUpgrade)Apply root * yaml . Node") and disambiguates
same-named methods on different receivers, which bare function names collide on. It is
NOT the same as the source line range, which is deliberately excluded from identity:
line numbers drift on any unrelated edit above a function (a comment, an import, a
sibling function growing), and keying on them would make the ratchet fail on pure
line-shift noise. `occurrence` is a 1-based index (by ascending start line) among rows
sharing the same (file, long_name) — needed because a handful of anonymous closures
share both file and signature (e.g. three `func(ctx context.Context, ...)` closures in
one file); dropping it would silently alias unrelated violations onto the same identity.

Two modes:
  generate   Print a fresh baseline (current violations) to stdout. Used only by
             `just complexity-baseline-update`, never by the gate itself.
  check      Compare current violations to the committed baseline. Exit 0 if every
             current violation is either absent from the baseline-turned-fixed set or
             matches an existing identity at no worse a CCN; exit 1 and name the
             offender(s) otherwise.

Known blind spots (do not try to fix here; see .complexity-baseline.txt and the task
notes this script was written against):

  * lizard's Go parser does not see a func-literal assigned inside a composite literal
    (e.g. every `&cobra.Command{RunE: func(...) {...}}` in internal/cli) as a function.
    It only walks the closures nested *inside* that literal. Every cobra command body in
    this repo is invisible to this gate for that reason — the 282-function baseline is
    a floor on known debt, not the true count. If someone later extracts a RunE body
    into a named function (a good refactor), lizard sees it for the first time and this
    script has no way to distinguish "newly visible, pre-existing complexity" from
    "newly written, actually-new complexity" — both look like a fresh identity. That
    refactor will require `just complexity-baseline-update` plus a human look at the
    diff, same as the CCN-worsened case below; it is not this gate's job to guess.

  * If a violation's occurrence index shifts (a same-signature sibling gets inserted
    earlier in the file), this script can report a spurious new+fixed pair instead of
    "unchanged". This is the cost of using an index instead of no distinguisher at all;
    it fails safe (loud, not silent) and is rare in practice (3 of 282 current entries
    share a (file, long_name) key at all).
"""
from __future__ import annotations

import csv
import io
import subprocess
import sys
from dataclasses import dataclass

LIZARD_EXCLUDES = ["-x", "*.pb.go", "-x", "*/website/*"]
CCN_THRESHOLD = "10"
BASELINE_PATH = ".complexity-baseline.txt"

BASELINE_HEADER = """\
# .complexity-baseline.txt — ratchet baseline for `just complexity-check`.
#
# This is DEBT, not an approved state. It exists so the complexity gate can fail on
# NEW violations without also failing CI on the ~282 that already exist. Nobody signed
# off on these 282 functions being fine; they are just not today's job.
#
# The true complexity debt is HIGHER than what's listed here: lizard's Go parser
# cannot see a func-literal assigned inside a composite literal (every cobra
# `RunE: func(...) {...}` in internal/cli), so those command bodies are invisible to
# this baseline entirely. See scripts/complexity_gate.py's module docstring.
#
# Format (pipe-separated, one violation per line):
#   file|long_name|occurrence|ccn|function_name|start-end
# Only the first four fields (file, long_name, occurrence, ccn) are load-bearing
# identity + threshold state for the gate. `function_name` and `start-end` are here
# purely so a human grepping this file can find the offender; they are NOT compared.
#
# Regenerate with `just complexity-baseline-update` — never by hand, never as a side
# effect of running the gate. Review the diff before committing: a shrinking baseline
# means violations were fixed (good, expected); a GROWING baseline means you are
# knowingly accepting new or worsened debt, and that decision belongs in the commit
# message, not silently in this file.
"""


@dataclass(frozen=True)
class Violation:
    file: str
    long_name: str
    occurrence: int
    ccn: int
    function_name: str
    start: str
    end: str

    @property
    def identity(self) -> tuple[str, str, int]:
        return (self.file, self.long_name, self.occurrence)

    def baseline_line(self) -> str:
        return "|".join(
            [
                self.file,
                self.long_name,
                str(self.occurrence),
                str(self.ccn),
                self.function_name,
                f"{self.start}-{self.end}",
            ]
        )


def run_lizard_csv() -> str:
    cmd = ["lizard", *LIZARD_EXCLUDES, "-C", CCN_THRESHOLD, "--csv", "."]
    # lizard's own exit code reflects "any violation found", which is expected and not
    # an error for our purposes — we do our own pass/fail below. Only a genuine
    # inability to run it (missing binary, parse crash producing no rows) should abort.
    proc = subprocess.run(cmd, capture_output=True, text=True)
    if not proc.stdout.strip():
        sys.stderr.write("complexity_gate: lizard produced no output\n")
        sys.stderr.write(proc.stderr)
        sys.exit(2)
    return proc.stdout


def parse_violations(csv_text: str) -> list[Violation]:
    reader = csv.reader(io.StringIO(csv_text))
    rows = []
    for row in reader:
        if not row or len(row) < 11:
            continue
        try:
            ccn = int(row[1])
        except ValueError:
            continue
        if ccn <= int(CCN_THRESHOLD):
            continue
        rows.append(
            {
                "file": row[6],
                "function_name": row[7],
                "long_name": row[8],
                "start": row[9],
                "end": row[10],
                "ccn": ccn,
            }
        )

    # Assign occurrence index per (file, long_name), ordered by start line ascending,
    # so it is deterministic regardless of the order lizard emits rows in.
    rows.sort(key=lambda r: (r["file"], r["long_name"], int(r["start"])))
    counters: dict[tuple[str, str], int] = {}
    violations = []
    for r in rows:
        key = (r["file"], r["long_name"])
        counters[key] = counters.get(key, 0) + 1
        violations.append(
            Violation(
                file=r["file"],
                long_name=r["long_name"],
                occurrence=counters[key],
                ccn=r["ccn"],
                function_name=r["function_name"],
                start=r["start"],
                end=r["end"],
            )
        )
    return violations


def load_baseline(path: str) -> dict[tuple[str, str, int], Violation]:
    baseline: dict[tuple[str, str, int], Violation] = {}
    try:
        with open(path, encoding="utf-8") as f:
            lines = f.readlines()
    except FileNotFoundError:
        sys.stderr.write(f"complexity_gate: baseline file {path} not found\n")
        sys.exit(2)
    for line in lines:
        line = line.rstrip("\n")
        if not line or line.startswith("#"):
            continue
        parts = line.split("|")
        if len(parts) != 6:
            sys.stderr.write(f"complexity_gate: malformed baseline line: {line!r}\n")
            sys.exit(2)
        file, long_name, occurrence, ccn, function_name, span = parts
        start, _, end = span.partition("-")
        v = Violation(
            file=file,
            long_name=long_name,
            occurrence=int(occurrence),
            ccn=int(ccn),
            function_name=function_name,
            start=start,
            end=end,
        )
        baseline[v.identity] = v
    return baseline


def cmd_generate() -> int:
    violations = parse_violations(run_lizard_csv())
    violations.sort(key=lambda v: (v.file, v.function_name, v.occurrence))
    sys.stdout.write(BASELINE_HEADER)
    for v in violations:
        print(v.baseline_line())
    return 0


def diff_against_baseline(
    current: list[Violation], baseline: dict[tuple[str, str, int], Violation]
) -> tuple[list[Violation], list[tuple[Violation, Violation]], list[Violation]]:
    new_violations = []
    worsened = []
    for v in current:
        base = baseline.get(v.identity)
        if base is None:
            new_violations.append(v)
        elif v.ccn > base.ccn:
            worsened.append((v, base))
    current_ids = {v.identity for v in current}
    fixed = [b for ident, b in baseline.items() if ident not in current_ids]
    return new_violations, worsened, fixed


def print_offenders(label: str, items, line_fmt) -> None:
    if not items:
        return
    print(f"\n{len(items)} {label}:")
    for item in items:
        print(line_fmt(item))


def cmd_check() -> int:
    current = parse_violations(run_lizard_csv())
    baseline = load_baseline(BASELINE_PATH)
    new_violations, worsened, fixed = diff_against_baseline(current, baseline)

    print(f"complexity ratchet: {len(current)} current violations, "
          f"{len(baseline)} baselined, {len(fixed)} fixed since baseline")

    print_offenders(
        f"NEW violation(s) not in {BASELINE_PATH}",
        sorted(new_violations, key=lambda v: (v.file, v.function_name)),
        lambda v: f"  NEW  ccn={v.ccn:<4} {v.file}:{v.start}-{v.end} "
        f"{v.function_name or '<anonymous>'} [{v.long_name.strip()}]",
    )
    print_offenders(
        "baselined violation(s) got WORSE",
        sorted(worsened, key=lambda pair: (pair[0].file, pair[0].function_name)),
        lambda pair: f"  WORSE ccn={pair[1].ccn}->{pair[0].ccn} {pair[0].file}:"
        f"{pair[0].start}-{pair[0].end} {pair[0].function_name or '<anonymous>'} "
        f"[{pair[0].long_name.strip()}]",
    )

    if new_violations or worsened:
        print(
            f"\ncomplexity ratchet: FAIL — {len(new_violations)} new, {len(worsened)} "
            "worsened. Fix them, or if this is deliberate accepted debt, run "
            "`just complexity-baseline-update` and explain why in the commit."
        )
        return 1

    print("complexity ratchet: PASS — no new or worsened violations")
    return 0


def main(argv: list[str]) -> int:
    if len(argv) != 2 or argv[1] not in ("generate", "check"):
        sys.stderr.write("usage: complexity_gate.py {generate|check}\n")
        return 2
    if argv[1] == "generate":
        return cmd_generate()
    return cmd_check()


if __name__ == "__main__":
    sys.exit(main(sys.argv))
