#!/usr/bin/env python3
"""Per-skill recall/precision across several answer files, so a skill's authored
premise can be compared against its existing description on the same rows.

Usage: per_skill.py <corpus.yaml> <label=answers.txt> [label=answers.txt ...]
"""
import sys, yaml, collections
d = yaml.safe_load(open(sys.argv[1]))
sits = {s["id"]: set(s.get("expect") or []) for s in d["situations"]}
src = {s["id"]: s.get("source") for s in d["situations"]}
names = list(d["fragments"])
runs = {}
for arg in sys.argv[2:]:
    label, path = arg.split("=", 1)
    got = {}
    for line in open(path):
        if ":" not in line: continue
        sid, rest = line.split(":", 1); sid = sid.strip()
        if sid not in sits: continue
        got[sid] = set() if rest.strip().upper() == "NONE" else {n.strip() for n in rest.split(",") if n.strip()}
    runs[label] = got
hdr = f"{'skill':15} {'exp':>3} " + " ".join(f"{l:>14}" for l in runs)
print(hdr); print("-" * len(hdr))
for n in names:
    exp_rows = [sid for sid, e in sits.items() if n in e]
    cells = []
    for label, got in runs.items():
        tp = sum(1 for sid in exp_rows if n in got.get(sid, set()))
        fp = sum(1 for sid, g in got.items() if n in g and n not in sits[sid])
        cells.append(f"{tp:>2}/{len(exp_rows):<2} +{fp:<2} fp ")
    print(f"{n:15} {len(exp_rows):>3} " + " ".join(f"{c:>14}" for c in cells))
print()
print("recall split by situation source (invoked / missed), per arm:")
for label, got in runs.items():
    parts = []
    for s_ in ("invoked", "missed"):
        tp = fn = 0
        for sid, e in sits.items():
            if src[sid] != s_: continue
            g = got.get(sid, set()); tp += len(g & e); fn += len(e - g)
        parts.append(f"{s_} {tp/(tp+fn):.3f}" if tp + fn else f"{s_} n/a")
    ff = sum(1 for sid, e in sits.items() if not e and got.get(sid)); nn = sum(1 for e in sits.values() if not e)
    print(f"  {label:14} {'  '.join(parts)}   false-fire {ff}/{nn}")
