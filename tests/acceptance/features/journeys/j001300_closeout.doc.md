<!--
J001300 narration companion (j001300_closeout.feature) — FLOWS-UNIFIED.md's U11.

This companion was written when the journey was ENTIRELY unbuilt, every
scenario red against a surface that did not exist. Where the prose argues the
design rather than reporting a measurement it says so.

The one thing here that IS measured: three of these scenarios initially passed
green, purely because the commands they drive do not exist, so "nothing was
destroyed" was trivially true. That is recorded below because it is the most
transferable lesson in the file.
-->

<!-- doc:intro -->
This journey was written before its code, and the reason that was worth doing
is that a close-out flow is defined almost entirely by what it REFUSES to do —
and refusals are the part that is hardest to add afterwards, because by then
someone has already shipped the version that just deletes things.

The trigger is ordinary to the point of boredom. The feature shipped Friday.
Monday there are stale worktrees, several holding commits nobody merged;
thousands of session directories; and the only durable record of what the team
actually learned is in one person's head, where it will remain.

The lessons half is the one that matters strategically. A session produces
knowledge, the knowledge becomes authored content, the content shapes the next
session — and skip that leg and ctxloom is a delivery pipeline with an open
loop. It can hand an assistant everything the team has ever written down, and
nothing that last week discovered. Boundary B10 in the product's own audit says
this plainly: lessons die in the transcript. There is no path from what a
session concluded to what the next session is told.

The debris half is what makes anyone actually run it.
<!-- /doc:intro -->

## The measurement that is not about the product

Three of these scenarios passed on the first run.

Not because the behaviour was right — because the verbs they named did not
exist yet, so the CLI rejected the invocation before doing anything, and
"nothing was destroyed", "the worktree is untouched" and "it did not report
success" were all trivially true. Three green ticks, zero coverage, in the
scenarios specifically written to protect against data loss.

This is the characteristic failure of safety tests, and it is worse than a
missing test because it reports coverage where there is none. The fix is a
guard every negative assertion now runs through first: if the last invocation
did not reach a command that exists, the scenario fails and says so, rather
than crediting a missing feature with a safety property.

The general rule, which applies well beyond this file: **an assertion that
something did not happen is only meaningful if something had the opportunity
to happen.** Any test asserting absence needs to prove the code path ran.

## The design being specified

### Two populations of worktree, two verbs, and one deliberate absence

ctxloom's own scratch worktrees live inside a specific harp's directory —
`~/.ctxloom/sessions/<harp>/work/ctxloom-wt-*` — so the noun is settled by
layout rather than by taste: they are session-scoped by construction, and
`session` is where they belong.

For those, ctxloom may reap, and the safety semantics are not invented here.
`isolation.ReapOrphanedWorktrees` already treats "I cannot prove who owned
this" identically to "someone is still using this: never touch it", spares
uncommitted work in place, and removes only genuinely clean trees. The
`session worktrees` sub-noun is the leaf for them: its bare form, `list`,
reports the verdict each checkout would get, and `purge <session-name> --yes`
removes the ones it can prove are safe. It drives the same classifier and
remover as everything else that reaps — the startup sweep a `ctxloom run`
makes (`operations.SweepOrphanedWorktrees`, which speaks only when it removed
something), and `session sweep` and `clean` (`operations.SweepSessions`),
which triage a session's worktrees before reclaiming it — with a report
attached. Reuse, not reimplementation, which is why the scenarios assert the
reaper's existing outcome vocabulary of reaped / spared / skipped.

Long-lived worktrees a human made live outside the sessions root and are
invisible to that finder by construction. They get a doctor REPORT and no
reaping verb at all, and **the absence of that verb is a decision, not an
oversight**. One scenario exists purely so that a later well-meaning change
which starts listing foreign trees under a verb that also reaps has to fail a
test before it can ship. Done still means merged AND removed AND branch
deleted; for trees ctxloom did not create, its job is to make the undone state
visible and a human's job is to act.

This project has lost work to a force-removed worktree before. That is why the
WIP assertion reads the file's own bytes off disk rather than checking that a
directory still exists.

### A session has three content classes and they are not alike

- **Machine-written** — transcripts, native history, scratch, diagnostics, in
  the session dir. Purgeable bulk, and the reason anyone runs this at all.
- **The record** — the session's index entry. Cheap; nothing takes it. A
  purged session stays in the index MARKED purged, because a session that
  vanishes from the index is indistinguishable from one that never existed.
- **Human-authored** — the session's output dir (plan files, and any summary
  an older ctxloom left there) and authored files anywhere: design notes,
  write-ups. Never negotiable; no destroyer reaches the output dir.

The third class carries a further requirement that is easy to miss: authored
files are not merely preserved, they are NAMED in the report. A file that is
kept but never mentioned is a file nobody will ever file, which over a long
enough window is the same outcome as deleting it, only slower.

### The unclassified middle (B13)

The session dir holds machine state only, classified row by row
(`paths.HarpMembers`); a containerized run reaches only the rows marked
Mounted. Readable outputs — plans among them — belong in the session's output
dir, which a container reaches at `/ctxloom/out`.

An authored file left at the session dir's TOP LEVEL is classified by no row:
**a containerized agent writing a design note there writes into
container-ephemeral space and loses it on exit**, and on the host it sits where
no human looks. The doctor check that notices the condition, and names the
output dir as the fix, is one of the scenarios here.

### The confirmation line

Cleanup that acts without confirmation on destructive steps is a defect;
cleanup that only reports is theatre. The line drawn between them:

- inspect always — read-only default on every leaf;
- additive writes proceed on plain apply, WITH payload assertions: "wrote 0
  fragments" is exit-nonzero, never success;
- destruction confirms against evidence rendered in the same invocation —
  `--yes` applies only what THIS invocation's plan showed, and no config key
  may pre-answer it;
- the refusal list is hard-coded: no force-remove, no `-D`, no dirty or
  unmerged or unowned trees, no live session, no destroying a transcript its
  retention keeps, no touching vendor stores.

Several scenarios here enforce individual lines of that list directly, which is
the point of writing it as a feature file rather than a paragraph.

### The lessons skill is a security boundary, not a convenience

No verb turns a session into authored content yet, so this leg has no
scenario; what it will have to honour is settled here. A distributable lessons
skill sounds like a small ergonomic feature and is not. It reads whole session
transcripts — the most sensitive artifact ctxloom holds — and decides what gets
written back into checked-in, team-distributed fragments. A hostile one is
simultaneously a context-exfiltration primitive and a context-poisoning
primitive, and it is a strictly higher-value target than any ordinary
fragment.

That is the argument for delivering it as a skill through the same pipeline as
every other skill rather than through a config key naming a file path: the
pipeline is where a skill is withheld (`operations.GetSkill` returns
`errs.ErrSkillWithheld`), and the requirement is that a withheld lessons skill
fails loud rather than falling back to an embedded prompt. `bundle distill`
already draws that line for its own prompt: `cli.loadDistillPrompt` refuses a
withheld `distill` command instead of silently substituting the built-in
default.

<!-- doc:outro -->
The one command that ties the legs together is deliberately not a new
top-level verb. `cleanup` as a command was raised during design and settled
against, by a convention this repo argued once and does not re-litigate: the
CLI is noun-verb, and a verb-first exception optimizes for a CLI a third this
size. So the sweep lives under the noun it tidies — `ctxloom session sweep` —
and it is deterministic: a fixed table decides each session, and every action
goes through the leaf that owns it, with that leaf's refusals intact. An
earlier design reached it through `run -r`, which is LLM-driven; a routine
whose whole value is what it refuses to do cannot have those refusals depend
on a model's reading of them.

The property that makes this worth building rather than merely worth having is
the dogfooding one. Because the lessons skill is bundle content, a team ships
its house extraction style exactly the way it ships any other context —
versioned, pinned. ctxloom's own behaviour flows through ctxloom's
own supply chain. That is the strongest available argument that the supply
chain is worth anything: the product uses it on itself, for the thing it cares
about most.

No scenario can be turned green by weakening an assertion; the guard added
after the first run makes sure of that.
<!-- /doc:outro -->
