#!/usr/bin/env python3
"""Convert per-fragment answer lines ("<fragment>: S01, S02 | NONE") into the
scorer's per-situation form ("S01: fragment, fragment | NONE").

Usage: perfragment_to_answers.py <corpus.yaml> <perfragment.txt> > answers.txt
"""
import sys, re, yaml, collections
sits = [s["id"] for s in yaml.safe_load(open(sys.argv[1]))["situations"]]
frags = set(yaml.safe_load(open(sys.argv[1]))["fragments"])
sel = collections.defaultdict(set)
for line in open(sys.argv[2]):
    line = line.strip()
    if ":" not in line: continue
    name, rest = line.split(":", 1)
    name = name.strip().strip("`*-# ")
    if name not in frags: continue
    for sid in re.findall(r"\b[SMNC]\d{2}\b", rest):
        sel[sid].add(name)
for sid in sits:
    print(f"{sid}: {', '.join(sorted(sel[sid])) if sel[sid] else 'NONE'}")
