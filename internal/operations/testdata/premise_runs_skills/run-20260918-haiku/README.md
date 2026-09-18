# run-20260918-haiku

Selector: `claude --print --model claude-haiku-4-5-20251001`, bare (no ctxloom context),
89 situations in three foreground chunks of 30/30/29 per condition, each call capped at 100s.
The prompt files hold the listing and instruction plus the FIRST chunk's situations; the
other two chunks differ only in which situations follow.

Two conditions, identical listing (the engine's own `- name: description - when_to_use`
shape, 250-char cap per entry, noun tags appended):
  engine    the Skill tool's real instruction, verbatim from claude-code-src
  ctxloom   the three measured properties from premise-selection.md

    condition  recall  precision  false-fire  exact-set   recall(invoked)  recall(missed)
    engine     0.678   0.983      0/17        65/89       0.763            0.612
    ctxloom    0.655   1.000      0/17        63/89       0.789            0.551

Score with:  python3 ../score_premises.py ../../premise_corpus_skills_v0.yaml answers_<cond>.txt
Per skill:   python3 ../per_skill.py ../../premise_corpus_skills_v0.yaml engine=answers_engine.txt ctxloom=answers_ctxloom.txt

Run by the coordinator after the trial agent's backgrounded selectors never returned
(a leaf agent receives no completion notification). Same corpus, same scorer.
