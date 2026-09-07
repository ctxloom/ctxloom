---
description: Find everything blocked on a human decision and put each one to the human as an interactive question that loads their context — they know the project, but are running several threads and do not know which one this is.
tags:
  - default
  - decisions
  - human
  - coordinator
---
Invoke the `prompt-human` skill and follow it.

Sweep for everything blocked on a human decision — this session's deferrals and
silent defaults, every agent report's DEFERRALS section, the task log, the
plans and design docs' open-decisions sections, and any comment or feature row
that poses a question it never answers.

Then filter hard. Most of what you find is not theirs: anything a convention or
an existing helper already answers, and anything you can settle by reading the
code, you settle yourself and mention. Go and read BEFORE you ask — a fork whose
answer is in the tree is your homework, not their decision. Be especially wary
of any option that proposes a NEW declaration; it is usually a duplicate of
state something already computes.

Put what genuinely remains through the interactive question tool, never prose,
one recommendation-first fork per decision, every option carrying its cost.

Load their context INTO each question. They know this project. What they do not
know is WHICH thread this is — name the task, say what it was doing, what state
it is in, and what changed to produce the question. Quote the measured fact, and
label VERIFIED against INFERRED.

Then record each ruling and its accepted cost where the work lives, and act on
it.

$ARGUMENTS