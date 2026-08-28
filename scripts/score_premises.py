#!/usr/bin/env python3
"""Score a premise-selection run against committed ground truth.

Usage: score_premises.py <situations.yaml> <answers.txt> [corpus.yaml] [premised_corpus.yaml]
answers.txt: one "S01: name, name" per line, or "S01: NONE".

Open-world by construction. Ground truth names only what SHOULD be selected;
anything else selected is a false positive whether or not anyone anticipated it.
There is deliberately no list of forbidden fragments: enumerating the negatives
you can imagine guarantees you never measure the ones you cannot.
"""
import sys, yaml

sits = {s["id"]: s for s in yaml.safe_load(open(sys.argv[1]))["situations"]}

# Fragments carrying NO premise always load, so selection never decides them and
# they must not score. They stay in the LABELLER's vocabulary on purpose --
# removing them would tell a blind labeller which fragments are unpremised, which
# is premise information leaking into ground truth by omission.
ALWAYS_LOAD = set()
if len(sys.argv) > 4:
    ALWAYS_LOAD = {n for n, v in yaml.safe_load(open(sys.argv[4]))["fragments"].items()
                   if not (v.get("premise") or "").strip()}
    if ALWAYS_LOAD:
        print(f"ignoring {len(ALWAYS_LOAD)} always-load fragment(s): "
              f"{', '.join(sorted(ALWAYS_LOAD))}\n")
corpus_n = None
if len(sys.argv) > 3:
    corpus_n = len(yaml.safe_load(open(sys.argv[3]))["fragments"])

got = {}
for line in open(sys.argv[2]):
    line = line.strip()
    if not line or ":" not in line or line.upper().startswith("NOTE"):
        continue
    sid, rest = line.split(":", 1)
    sid = sid.strip()
    if sid not in sits:
        continue
    got[sid] = set() if rest.strip().upper() == "NONE" else {
        n.strip() for n in rest.split(",") if n.strip()}

missing = sorted(set(sits) - set(got))
if missing:
    print(f"!! unanswered: {', '.join(missing)} — scoring the {len(got)} answered\n")

tp = fp = fn = 0
sel_counts, exp_counts, exact = [], [], 0
empty_rows = empty_fired = 0
rows = []
for sid in sorted(got):
    exp = set(sits[sid].get("expect") or []) - ALWAYS_LOAD
    sel = got[sid] - ALWAYS_LOAD
    tp += len(sel & exp); fp += len(sel - exp); fn += len(exp - sel)
    sel_counts.append(len(sel)); exp_counts.append(len(exp))
    if sel == exp: exact += 1
    if not exp:
        empty_rows += 1
        if sel: empty_fired += 1
    note = []
    if exp - sel: note.append("DROP:" + ",".join(sorted(exp - sel)))
    if sel - exp: note.append("EXTRA:" + ",".join(sorted(sel - exp)))
    rows.append((sid, len(sel), sorted(sel), " ".join(note)))

def r(n, d): return n / d if d else 0.0
prec, rec = r(tp, tp + fp), r(tp, tp + fn)
f1 = r(2 * prec * rec, prec + rec)
# F2 weights RECALL twice precision. It is the headline here because the project
# has ruled it would rather over-select than under-select: an over-offered
# fragment costs context, a dropped one is never learned to exist. F1 is kept
# only as the neutral reference; ranking on it contradicts the ruling.
f2 = r(5 * prec * rec, 4 * prec + rec)

print(f"{'id':6} {'n':>2}  {'selected':44} notes")
for sid, n, sel, note in rows:
    print(f"{sid:6} {n:>2}  {', '.join(sel) or '-':44} {note}")

print(f"""
precision        {prec:.3f}   of what it selected, how much belonged
recall           {rec:.3f}   of what belonged, how much it found
F1               {f1:.3f}   (neutral reference)\nF2               {f2:.3f}   HEADLINE — recall weighted 2x, per the\n                         over-select-rather-than-under ruling
silent drop      {r(fn, tp+fn):.3f}   ({fn} expected fragments never offered — the
                         expensive error: the agent never learns they existed)
mean selected    {r(sum(sel_counts), len(sel_counts)):.2f}""" +
      (f" of {corpus_n}" if corpus_n else "") + f"""
mean expected    {r(sum(exp_counts), len(exp_counts)):.2f}   (widening shows up as selected drifting
                         above expected while precision falls)
false fire       {r(empty_fired, empty_rows):.3f}   ({empty_fired} of {empty_rows} nothing-applies situations
                         selected something anyway — the open-world
                         over-selection signal, no negative list needed)
exact set        {exact} of {len(rows)}""")
