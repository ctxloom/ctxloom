#!/usr/bin/env python3
"""Skill-mates hook in the loop: build step-2 prompts, then score.

Step 1 is FIXED to the listing-condition control answers (the 4/15 ceiling,
../run-20260918-sonnet-linkpointer/answers_control.txt). Step 2: for every
situation where a step-1 skill has a link-group mate step 1 did not pick, the
model is told those skills ran (their bodies, from the corpus) and asked what
it invokes next. Two pairs of arms; within a pair only the hook's line differs:
  body / hook   -- asked explicitly which further skills to invoke now
  nbody / nhook -- asked only for the next action (invoke, or a step done by hand),
                   which is all a real session gets after a skill completes
"hook" arms carry claude.SkillMatesContext's exact line.
Link groups are an ORACLE: every consequent pair the corpus expects is a group,
the hook's best case. No shipped bundle declares any of them.

  loop.py build            write prompts/<arm>_<sid>.txt
  loop.py score <arm>...   merge step 2 into step 1, print both-found etc.
"""
import os, sys, yaml
HERE = os.path.dirname(os.path.abspath(__file__))
CORPUS = os.path.join(HERE, "../../premise_corpus_skills_v0.yaml")
CTL = os.path.join(HERE, "../run-20260918-sonnet-linkpointer")
d = yaml.safe_load(open(CORPUS))
sits = {s["id"]: s for s in d["situations"]}
order = [s["id"] for s in d["situations"]]
expect = {k: set(v.get("expect") or []) for k, v in sits.items()}
pairs = {frozenset(e) for e in expect.values() if len(e) == 2}
mates = {}
for p in pairs:
    a, b = sorted(p)
    mates.setdefault(a, set()).add(b); mates.setdefault(b, set()).add(a)

def parse(path):
    got = {}
    for line in open(path):
        if ":" not in line: continue
        sid, rest = line.split(":", 1); sid = sid.strip()
        if sid not in sits: continue
        r = rest.strip()
        got[sid] = set() if r.upper().startswith("NONE") else {n.strip().strip("`.") for n in r.split(",") if n.strip()}
    return got

step1 = parse(os.path.join(CTL, "answers_control.txt"))
listing = open(os.path.join(CTL, "prompt_control_admit.txt")).read().split("\n\nNow consider")[0]

def hook_fires(sid):
    picked = step1.get(sid, set())
    return {x: sorted(mates.get(x, set()) - picked) for x in sorted(picked) if mates.get(x, set()) - picked}

def build():
    os.makedirs(os.path.join(HERE, "prompts"), exist_ok=True)
    for sid in order:
        fired = hook_fires(sid)
        if not fired: continue
        done = sorted(step1[sid])
        for arm in ("body", "hook", "nbody", "nhook"):
            p = [listing, "", "The moment: " + sits[sid]["intent"], ""]
            for x in done:
                p += [f"You invoked the skill `{x}` for this moment. Its instructions were loaded and you have just finished following them:",
                      "<skill-body>", d["fragments"][x]["content"].strip(), "</skill-body>", ""]
            if arm in ("hook", "nhook"):
                for x, ms in fired.items():
                    p += ["PostToolUse hook additional context: Skill " + x + " completed; linked skills not yet invoked this session: " + ", ".join(ms)]
                p.append("")
            if arm in ("body", "hook"):
                p += [f"Skills already invoked for this moment: {', '.join(done)}. Decide which further skills from the list above, if any, you invoke NOW for this same moment. Invoke one only if it applies; several may.",
                      "Answer with ONE line only: the skill names comma-separated, or NONE."]
            else:
                p += ["What is your next action? Reply with ONE line: `invoke: <skill>[, <skill>]` if your next action is to invoke skills from the list, otherwise `next: <your next step>`."]
            open(os.path.join(HERE, "prompts", f"{arm}_{sid}.txt"), "w").write("\n".join(p) + "\n")

def line_answer(line):
    """The skill set one line states, or None when the line is prose."""
    line = line.strip().strip("`*")
    head, _, tail = line.partition(":")
    if head.lower() == "next": return set()
    if head.lower() == "invoke": line = tail
    toks = [t for t in (t.strip().strip("`*.:") for t in line.replace("Answer", "").split(",")) if t]
    if toks == ["NONE"]: return set()
    return set(toks) if toks and all(t in d["fragments"] for t in toks) else None

def answer(text):
    """The first line that is ONLY skill names or NONE; prose lines are skipped."""
    found = (line_answer(l) for l in text.splitlines())
    return next((a for a in found if a is not None), set())

def merged(arm):
    final = {sid: set(step1.get(sid, set())) for sid in order}
    out = os.path.join(HERE, os.environ.get("OUT", "out"))
    for sid in filter(hook_fires, order):
        final[sid] |= answer(open(os.path.join(out, f"{arm}_{sid}.txt")).read())
    return final

def count(pred):
    return sum(1 for s in order if pred(s))

def score(arm):
    final = merged(arm)
    both = [s for s in order if len(expect[s]) == 2 and expect[s] <= final[s]]
    tp = sum(len(final[s] & expect[s]) for s in order)
    fp = sum(len(final[s] - expect[s]) for s in order)
    fn = sum(len(expect[s] - final[s]) for s in order)
    print(f"{arm:8} both-found {len(both)}/{count(lambda s: len(expect[s]) == 2)} {both}  "
          f"recall {tp/(tp+fn):.3f} precision {tp/(tp+fp):.3f} "
          f"false-fire {count(lambda s: not expect[s] and final[s])}/{count(lambda s: not expect[s])} "
          f"exact {count(lambda s: final[s] == expect[s])}/{len(order)} "
          f"single-exact {count(lambda s: len(expect[s]) == 1 and final[s] == expect[s])}/{count(lambda s: len(expect[s]) == 1)} fp {fp}")
    with open(os.path.join(HERE, f"answers_{arm}{os.environ.get('SUFFIX', '')}.txt"), "w") as w:
        for s in order: w.write(f"{s}: {', '.join(sorted(final[s])) or 'NONE'}\n")

if __name__ == "__main__":
    if sys.argv[1] == "build": build()
    elif sys.argv[1] == "fires":
        for s in order:
            if hook_fires(s): print(s, sorted(expect[s]), sorted(step1.get(s, [])), hook_fires(s))
    else:
        for a in sys.argv[2:]: score(a)
