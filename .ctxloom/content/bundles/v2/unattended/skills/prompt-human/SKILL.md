---
name: prompt-human
description: Find everything blocked on a human decision, then put each one to them as an interactive question carrying far more context than usual — because they know the project but are running several threads at once and have no idea which one this is. Use whenever the human ASKS ABOUT pending decisions or TELLS YOU TO ACT ON them — the verb does not matter and these examples are not exhaustive. Asked as a question — "what's pending", "anything for me?", "what needs deciding", "what's blocked on me". Given as an instruction, which is just as common — "raise the human decisions", "run the decisions by me", "surface what needs deciding", "go through the open questions", "ask me", "check with me". Treat ANY phrasing naming decisions, open questions, or what is blocked on the human as a match, however it is worded. Also use before ending a work session, cutting a release, or handing off.
---

# Putting decisions to a human

Work stalls silently when a decision nobody asked for sits inside an agent
report, a deferral line, or your own reasoning. This skill finds those and puts
them in front of the human in a form they can answer without reconstructing the
context first.

The bar is not "list open questions." It is: **the human can decide correctly
having forgotten everything about this particular thread.**

That last qualifier is the whole design. They know the project — telling them
what a lockfile is wastes their attention and reads as condescension. What they
do not have is *situational* knowledge, because they are running several threads
at once: other repos, other agents, a release, a conversation from four hours
ago. When your question lands they have no idea which thread it belongs to.

So: **explain the situation, never the project.**

---

## 1. Sweep — all of these, not just the conversation

- **This session's context.** Anything you deferred, worked around, or resolved
  by picking a default. If you wrote "I'll assume", "for now", "pending", or
  "left tagged" — that is a candidate.
- **Agent reports.** Every DEFERRALS section, every "STOP AND REPORT" that
  fired, every "I did not decide X". Sub-agents are told to escalate rather than
  choose; those escalations die with the report unless surfaced.
- **The task log.** Filter for the decision tag, plus terms like "decide" and
  "open question". Read the BODIES — a task titled for a symptom often carries
  the decision in its last paragraph.
- **Design docs and plans.** "Open decisions" sections and `*.plan.md` files
  accumulate unanswered questions by design.
- **The code and tests.** Feature-file comments saying a row stays red pending a
  decision; load-bearing comments that pose a question and never answer it.

---

## 2. Validate the row before you treat it as a question

**The row is not the truth. The tree is.** A task outlives the code it
describes, and a stale one does not look stale — it looks exactly like live
work, because it was live when it was written.

So before a row becomes a question, establish that it still describes reality:

- **Do the symbols it cites still exist?** `grep` them. A row whose subject was
  deleted is not a decision, it is an archaeology find.
- **Has the fix already landed?** Read the call site it names. `git log -S
  '<symbol>'` says when something appeared or vanished.
- **Was it already ruled?** The ruling is often in the row's own later
  paragraphs, under a heading the first line does not hint at.
- **Is there a more advanced duplicate?** Two rows on one question drift, and
  whichever you read first looks complete.

Reaching "already done", "already ruled", "duplicate" or "moot" is a
**SUCCESSFUL** outcome of this skill, not a wasted pass. Close, defer or absorb
the row, record what you verified and how, and move on without asking.

### Validity is not the same as "is anything still open"

Do not turn this step into "does the row contain an unanswered question". That
test is wrong in a way that quietly DESTROYS the queue, and it was run at scale
once before anyone noticed.

The `human` tag does not mean "needs a decision". It means the row lands in the
queue a PERSON WORKS FROM — work that trips an unattended stop condition. Those
are two different sets, and the second is much larger. A row can have every
question settled and still be human-only: prompt, skill, fragment and profile
text are the human's voice; signing keys, dependency changes, schema and on-disk
format changes, cross-repo moves, anything needing credentials or a live
multi-process run, and anything whose only evidence is a person looking at it
all qualify with no open question anywhere in the text.

So when this step finds a row's decision already taken, that settles whether to
ASK about it. It does not settle whether the row belongs in the human queue.
Judge that against the stop conditions themselves — they live in the `unattended`
skill — and leave the tag alone unless the remaining WORK could genuinely run
with nobody watching.

Measured: applying the wrong test to this project's backlog stripped the tag
from 28 rows in one pass, on rows whose remaining work included editing signed
bundle text and rebuilding a container image. It was caught by the human, not by
the sweep. When in doubt the answer is to leave the tag on: a needless row costs
one glance, and a dropped one costs the work.

### Why this is a numbered step and not advice

Measured in one session working this backlog: of the rows examined, FOUR were
invalid, each in a different way — one duplicated a further-along row; one
turned on a package that had been deleted wholesale, so none of its forks could
be answered at all; one had already been ruled and carried a stale tag; and one
had ALREADY BEEN FIXED in the tree, with the commit sitting in the log. A full
question had been drafted around that last one before anybody checked.

The cost of skipping this is not merely wasted attention, which would be
recoverable. It is that **a ruling taken on a false premise gets written down as
a decision** — and then it is authoritative, it outlives the confusion that
produced it, and the next reader has no way to tell it apart from a sound one.
An answer to a question that should never have been asked is worse than no
answer.

---

## 3. Filter — most things are NOT the human's call

Ask only where **different answers produce materially different work**. Decide
the rest yourself and say what you decided.

Genuinely theirs:

- adding, removing, or swapping a **dependency**
- changing a **contract**: exported signature, wire or config format, persisted
  file shape, or a trust outcome
- a **new CLI surface** — command, subcommand, flag, or exit-code semantics
- **breaking existing users**, even where the project permits it: *how* it
  breaks, and whether it breaks loudly, is a decision
- **scope against a release** — does this block the cut
- a **security posture**
- anything where you would otherwise be guessing at **intent** rather than at
  fact

Not theirs — do it, and mention it:

- anything a convention, an existing helper, or a nearby precedent already
  answers
- anything you can settle by reading the code or running a command

### Burn these before asking

- **Go and read.** A fork whose answer is in the tree is not a fork, it is your
  homework. Grep the symbol. Read the doc comment — in a codebase that states
  its rules there, the answer is frequently written verbatim.
- **Beware of proposing new mechanism.** An option that adds a declaration — a
  config key, a mode flag, a new field — is very often a duplicate of state
  something already computes. Ask "what already knows this?" before offering it.
  Two sources of the same truth can disagree; that is worse than no answer.
- **Check whether it was already ruled.** Search the task log and design docs
  for the same question under a different name. Re-asking a settled question
  spends trust as well as time.

---

## 4. Load their context INTO the question

Assume zero recall of anything you have not restated. Each question stands
alone, even if you asked a related one twenty minutes ago.

Open by re-establishing WHICH:

- which task, branch, file, or subsystem — by name
- what it was trying to do, in one sentence
- what state it is in right now
- what CHANGED that produced this question

Then the substance — as much of this as applies, because it is the difference
between a question answered in ten seconds and one deferred again:

- **The measured fact, quoted.** Not "the check seems wrong" but the actual
  output, the actual error, the actual line. Quote a doc comment when it is the
  thing that settles the argument.
- **VERIFIED versus INFERRED, explicitly.** Say which parts you ran and which
  you reasoned to. If you did not check something material, say so — "I have not
  confirmed X" is worth more than a confident guess, and it tells them how much
  to lean on the rest.
- **What was already tried or ruled out, with the reason.** Otherwise they will
  propose it, and you will both discover you agreed an hour ago.
- **Blast radius.** What else changes, who else is affected, what gets redone.
- **The cost of each option**, concretely. Not "more work" but "a schema change
  plus a resolution mechanism that does not exist yet."
- **The cost of NOT deciding.** What stays blocked, and whether it degrades.

Cite by SYMBOL (`package.Function`, a task ID, a scenario name) rather than
file:line. A stale symbol fails loudly the moment someone greps; a stale line
number quietly points at unrelated code and gets believed.

---

## 5. Ask with the question tool, not with prose

Prose questions get skimmed and answered vaguely. Use the interactive question
tool (`AskUserQuestion` or the harness equivalent), even for a single decision.
It forces a real fork and records the answer.

**Recommend, do not survey.** Your recommendation goes first, marked. A survey
with no recommendation hands the analysis back to the person who asked you to do
it. If you genuinely have no preference, say so and say why it is close.

**Every option carries its cost.** An option with no downside means you have not
understood it yet — find the cost or drop the option.

**Options must be mutually exclusive and real.** Two options where one is
obviously right is not a fork; it is a decision you should have made. If an
option is bad but plausible enough that they might pick it, include it and say
you would push back — that is more useful than hiding it.

**Put the pushback IN the option.** If you think a choice is wrong, the option's
own description should say so. They are entitled to overrule you, and they can
only do that if they can see what you think.

**Batch by context, not by count.** Several questions in one call is fine when
they share a situation and each is individually complete. Collapse dependents:
if X settles Y, ask X only. Two questions about genuinely different subsystems
in one breath will get one answered well and one answered carelessly.

---

## 6. After they answer

- **Record the decision where the work lives** — the task, the design doc, the
  commit message body. A ruling that exists only in a chat log gets
  re-litigated, and the next reader cannot find it.
- **Record the ACCEPTED COST too**, and what should trigger revisiting it. A
  decision recorded without its cost reads later as an oversight rather than a
  choice.
- **If the answer invalidates something you wrote earlier, strike it** — do not
  layer a correction on top. A task carrying a superseded ruling in its first
  paragraph will send the next person the wrong way.
- **If they correct your premise, repair the premise in the record**, not just
  the ruling. An answer to a badly framed question is still badly framed.
- **Record deferrals as deferrals.** If they defer, write down what is now
  unverified and what should revive it. Silence reads as "handled".
- **Act on it.** If the answer unblocks work, do the work. Do not report the
  answer back and stop.

---

## Failure modes this exists to prevent

- **The decision that never surfaced.** It sat in an agent's DEFERRALS section
  and the orchestrator summarised the agent's successes instead.
- **The context-free question.** "Should we use A or B?" about something they
  last saw four hours and three subsystems ago.
- **The question the code answered.** Their attention spent on your homework.
- **The stale question.** The row described work already done, already
  ruled, or turning on code since deleted — and the ruling it produced was
  then recorded as authoritative, indistinguishable from a sound one.
- **The invented fork.** A new config key offered as an option, when an existing
  assembler already carries the answer.
- **The false fork.** Two options where one is obviously right.
- **The survey.** Four options, no recommendation, no costs.
- **The silent default.** You picked one and moved on. If it was obvious, say it
  was obvious; if it was not, it was a decision and it needed asking.
- **The buried answer.** They ruled, you carried on, and nothing in the repo
  records what they decided or why.
