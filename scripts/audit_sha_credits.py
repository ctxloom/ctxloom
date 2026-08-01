#!/usr/bin/env python3
"""Audit the findings index: does each adjudicated row's cited commit actually name it?

Written 2026-07-30 after wave 34 found U085-F10 marked RESOLVED against `4c0ceb8b`,
which is U089-F10's commit in a different file. The defect U085-F10 named was still
live; wave 34 hit it independently while fixing U085-F11.

A row marked RESOLVED against someone else's commit is worse than an open row: it is
indistinguishable from work that was done, so nobody looks again.

Run from the repo root:
    python3 scripts/audit_sha_credits.py [path/to/findings-index.md]

Three passes, because they answer different questions:

  Pass 1 — "does the CITED commit name this row?"  Loud, ~116 hits, mostly benign:
           subjects abbreviate ranges (`U079-F01..F05`, `U016-F01/02/03`), and a merge
           commit carries a fix whose real message is on the merged branch.

  Pass 2 — "does ANY commit anywhere name this row?"  This is the one to read. A row
           no commit mentions at all is either (a) recorded by a bookkeeping commit,
           which is CORRECT for ESCALATED rows per template section 10's two-step, or
           (b) credited to an unrelated commit, which is the U085-F10 bug.

  Pass 3 — "does the CITED commit's body carry a negation cue near this row's own ID?"
           Catches the shape Pass 2 cannot: a commit that DISCLAIMS a fix names the
           finding ID exactly like one that delivers it, so "is this ID named
           ANYWHERE" (Pass 2) passes it clean. Pass 1's "named in the SUBJECT" catches
           some of this shape too, but not all — the disclaiming mention can share a
           commit whose subject legitimately lists OTHER rows the commit did fix.
           Window: 2 lines of context either side of the line carrying the ID mention
           (a 5-line window total), chosen because the motivating case shares one line
           ("Did NOT also fix profileLoader(cfg) ... (U085-F10/F11...)"). A wider
           window (checked at ±5 lines during the 2026-07-31 sweep) roughly doubles the
           hit count by pulling in unrelated paragraphs — not worth the noise.

Neither Pass 1 nor Pass 2 can be fully automated away: distinguishing a real mis-credit
from a benign hit needs a human to look at whether the cited commit's DIFF plausibly
covers the row's claim. Pass 3 needs the same triage, and more of it — negation cues are
common in commit prose that has nothing to do with a disclaimed fix (e.g. "X does not
exist" describing the ORIGINAL bug, right before the same paragraph fixes it). The
script narrows thousands of rows to a few dozen worth that look, across all three
passes; none of the three passes' output is a defect list on its own.

DO NOT READ THIS SCRIPT'S OUTPUT AS A DEFECT LIST. Pass 2 reported 51 rows before it
learned to expand abbreviated runs (see FINDING_RUN); 40 of those 51 were the regex
failing to see a commit that DID name the row, and the remaining 11 were ESCALATED rows
correctly citing their wave-verdict commit. Zero were the U085-F10 bug. Every hit is a
candidate for a human look, never a finding. The 2026-07-31 sweep ran Pass 3 over all
2128 cited rows, got 101 hits, and after full manual triage kept exactly 2 as real
mis-credits (U024-F14, U090-F14) — the other 99 were negations about the ORIGINAL bug
(fixed in the same paragraph), legitimate REFUTED/PARTIAL explanations, or legitimate
ESCALATED bookkeeping commits.

Known limits:

  1. A `..` range is trusted WHOLE. `U091-F04..F22` asserts that commit named all 19
     rows; if it only fixed some, this script will not notice. Enumerated lists
     (`U052-F02/F03/F07`) carry no such risk.

  2. Pass 3's negation-cue list is a heuristic, not a parser: it will flag rows where a
     cue word appears near the ID for reasons unrelated to a disclaimed fix (most
     commonly, the negation describes the ORIGINAL defect the same paragraph goes on to
     fix). It will also miss a disclaimer phrased without any of the listed cues, or one
     that sits more than 2 lines from the ID. Every Pass 3 hit still needs a human to
     read the commit body and, when in doubt, `git show --stat <sha>` before deciding.
"""
import re
import subprocess
import sys
from collections import Counter, defaultdict

ROW = re.compile(
    r"\|\s*(U\d+-F\d+)\s*\|\s*\*\*(RESOLVED|REFUTED|PARTIAL|ESCALATED)\*\*\s*`([0-9a-f]{7,40})`"
)
FINDING_ID = re.compile(r"U\d+-F\d+")

# Commit subjects abbreviate. Two forms occur in this repo's history:
#   a range      `U079-F01..F05`      -> F01 F02 F03 F04 F05
#   a slash list `U068-F01/F02/F03`   -> F01 F02 F03   (the `F` is often dropped:
#                                        `U016-F01/02/03`, and the run may wrap a line)
# Reading these literally is what made the audit report 29 false "no commit names this
# row" hits: the commit DOES name the row, just not in longhand. A continuation that
# starts a new unit (`U070-F01/U071-F01`) is NOT a continuation — it is caught as two
# whole IDs by FINDING_ID, so the run pattern deliberately requires `F?\d+`, never `U`.
FINDING_RUN = re.compile(r"(U\d+)-F(\d+)((?:\s*(?:\.\.|/)\s*F?\d+)+)")
CONTINUATION = re.compile(r"(\.\.|/)\s*F?(\d+)")

# Negation cues seen in this repo's history near a DISCLAIMED finding ID. Not an
# exhaustive grammar — see "Known limits" #2 above.
NEGATION_CUES = [
    "did not", "does not", "won't", "will not", "not one of this batch",
    "already-tracked", "already tracked", "out of scope", "deferred",
    "sibling finding", "not fixed here", "not fixed by this",
    "a separate", "is separate", "that is separate",
    "not this", "unrelated to", "not covered by this",
    "not addressed here", "left unfixed", "not in scope",
]

PASS3_WINDOW = 2  # lines of context before/after the ID's own line


def expand_runs(text):
    """Every finding ID an abbreviated run stands for, longhand."""
    out = set()
    for unit, first, tail in FINDING_RUN.findall(text):
        width = len(first)
        prev = int(first)
        out.add(f"{unit}-F{first}")
        for op, num in CONTINUATION.findall(tail):
            n = int(num)
            if op == "..":
                # `..` is inclusive and must ascend; a descending range is a typo, and
                # silently "expanding" it to nothing is exactly the class of quiet miss
                # this script exists to catch, so say so.
                if n < prev:
                    print(f"  ! ignoring descending range {unit}-F{prev:0{width}d}..F{num}")
                    continue
                for i in range(prev + 1, n + 1):
                    out.add(f"{unit}-F{i:0{width}d}")
            else:
                out.add(f"{unit}-F{n:0{width}d}")
            prev = n
    return out


def expand_runs_with_span(text):
    """Like expand_runs, but yields (id, start, end) so Pass 3 can locate WHERE in the
    text an abbreviated run names a given id."""
    out = []
    for m in FINDING_RUN.finditer(text):
        unit, first, tail = m.group(1), m.group(2), m.group(3)
        width = len(first)
        prev = int(first)
        ids = [f"{unit}-F{first}"]
        for op, num in CONTINUATION.findall(tail):
            n = int(num)
            if op == "..":
                if n < prev:
                    continue
                for i in range(prev + 1, n + 1):
                    ids.append(f"{unit}-F{i:0{width}d}")
            else:
                ids.append(f"{unit}-F{n:0{width}d}")
            prev = n
        for rid in ids:
            out.append((rid, m.start(), m.end()))
    return out


def load_rows(path):
    rows = []
    for line in open(path, errors="replace"):
        m = ROW.match(line)
        if m:
            rows.append(m.groups())
    return rows


def subject(sha):
    r = subprocess.run(
        ["git", "log", "-1", "--format=%s", sha], capture_output=True, text=True
    )
    return r.stdout.strip() if r.returncode == 0 else "<UNRESOLVABLE SHA>"


def body(sha, cache={}):
    if sha in cache:
        return cache[sha]
    r = subprocess.run(
        ["git", "log", "-1", "--format=%B", sha], capture_output=True, text=True
    )
    b = r.stdout if r.returncode == 0 else None
    cache[sha] = b
    return b


def find_negation_window(rid, commit_body):
    """Return (window_text, cue) if a negation cue sits within PASS3_WINDOW lines of
    an occurrence of rid in commit_body (literal or via an abbreviated run), else None."""
    lines = commit_body.split("\n")
    offsets = [0]
    for l in lines:
        offsets.append(offsets[-1] + len(l) + 1)

    hit_lines = set()
    for i, l in enumerate(lines):
        if rid in l:
            hit_lines.add(i)
    for found_rid, start, _end in expand_runs_with_span(commit_body):
        if found_rid != rid:
            continue
        for i in range(len(lines)):
            if offsets[i] <= start < offsets[i + 1]:
                hit_lines.add(i)
                break

    for i in hit_lines:
        lo = max(0, i - PASS3_WINDOW)
        hi = min(len(lines), i + PASS3_WINDOW + 1)
        window_text = "\n".join(lines[lo:hi])
        low = window_text.lower()
        for cue in NEGATION_CUES:
            if cue in low:
                return window_text, cue
    return None


def pass3_negation_sweep(rows):
    by_sha = defaultdict(list)
    for rid, status, sha in rows:
        by_sha[sha].append((rid, status))

    hits = []
    for sha, entries in by_sha.items():
        b = body(sha)
        if b is None:
            continue
        for rid, status in entries:
            res = find_negation_window(rid, b)
            if res:
                window_text, cue = res
                hits.append((rid, status, sha, cue, window_text))
    return hits


def main():
    path = sys.argv[1] if len(sys.argv) > 1 else "docs/architecture/findings-index.md"
    rows = load_rows(path)
    print(f"rows carrying a cited sha: {len(rows)}")

    # One `git log` over every commit message beats one subprocess per row.
    log = subprocess.run(
        ["git", "log", "--all", "--format=%H%x00%B%x01"], capture_output=True, text=True
    ).stdout
    named = set()
    longhand_only = set()
    for chunk in log.split("\x01"):
        named.update(FINDING_ID.findall(chunk))
        longhand_only.update(FINDING_ID.findall(chunk))
        named.update(expand_runs(chunk))
    print(f"distinct finding IDs named in any commit message: {len(named)}")
    print(
        f"  ({len(named) - len(longhand_only)} of those are named only by an "
        f"abbreviated range or slash-list)"
    )

    orphans = [(rid, st, sha) for rid, st, sha in rows if rid not in named]
    print(f"\n*** rows no commit anywhere names: {len(orphans)} ***")
    print("by status:", dict(Counter(st for _, st, _ in orphans)))
    print()
    for rid, status, sha in orphans:
        print(f"  {rid:12s} {status:10s} cites {sha}  -> {subject(sha)[:85]}")

    print(
        "\nTriage: an ESCALATED row citing a `chore(findings-index): apply wave-N verdicts`\n"
        "commit is CORRECT (section 10's two-step). A RESOLVED row citing a commit for a\n"
        "DIFFERENT unit is the bug — check whether that commit's diff could possibly have\n"
        "fixed this row's claim."
    )

    print(f"\n--- Pass 3: negation-proximity sweep (window ±{PASS3_WINDOW} lines) ---")
    hits = pass3_negation_sweep(rows)
    print(f"*** candidate disclaim-shape hits: {len(hits)} ***\n")
    for rid, status, sha, cue, window_text in hits:
        print(f"  {rid:12s} {status:10s} `{sha}` (cue: {cue!r})")
    print(
        "\nTriage: read each hit's full commit body (`git log -1 --format=%B <sha>`) and,\n"
        "if still unsure, `git show --stat <sha>`. A hit is only a real mis-credit if the\n"
        "commit plausibly could NOT have closed the row's specific claim. Most hits are the\n"
        "negation describing the ORIGINAL bug in the same paragraph that goes on to fix it."
    )


if __name__ == "__main__":
    main()
