<!--
J001300 narration companion (j001300_closeout.feature) — FLOWS-UNIFIED.md's U11.

This companion was written when the journey was ENTIRELY unbuilt: all fifteen
scenarios were red, every one against a surface that did not exist. Ten have
since gone green — `session worktrees`, `session purge` and doctor's new
checks shipped — and five remain red. Where the prose argues the design rather
than reporting a measurement it says so.

The one thing here that IS measured: three of these scenarios initially passed
green, purely because the commands they drive do not exist, so "nothing was
destroyed" was trivially true. That is recorded below because it is the most
transferable lesson in the file.
-->

<!-- doc:intro -->
Every other journey in this suite describes something ctxloom does. This one
describes something it does not do yet, and the reason it is worth writing
before the code is that a close-out flow is defined almost entirely by what it
REFUSES to do — and refusals are the part that is hardest to add afterwards,
because by then someone has already shipped the version that just deletes
things.

The trigger is ordinary to the point of boredom. The feature shipped Friday.
Monday there are stale worktrees, several holding commits nobody merged;
thousands of session directories; and the only durable record of what the team
actually learned is in one person's head, where it will remain.

The lessons half is the one that matters strategically. A session produces
knowledge, the knowledge becomes authored content, the content shapes the next
session — and skip that leg and ctxloom is a delivery pipeline with an open
loop. It can hand an assistant everything the team has ever written down, and
nothing that last week discovered. Boundary B10 in the product's own audit says
this plainly: lessons die in `essence.md`. There is no path from what a session
concluded to what the next session is told.

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
`~/.ctxloom/sessions/<harp>/ephemeral/ctxloom-wt-*` — so the noun is settled by
layout rather than by taste: they are session-scoped by construction, and
`session` is where they belong.

For those, ctxloom may reap, and the safety semantics are not invented here.
`isolation.ReapOrphanedWorktrees` already treats "I cannot prove who owned
this" identically to "someone is still using this: never touch it", spares
uncommitted work in place, and removes only genuinely clean trees. Today that
logic is reachable only from a startup sweep that reports nothing to anybody.
The proposed leaf drives the same code with a report attached — reuse, not
reimplementation, which is why the scenarios assert the reaper's existing
outcome vocabulary of reaped / spared / skipped.

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

### The harp directory has three content classes and they are not alike

- **Machine-written** — transcripts, ephemeral state, diagnostics. Purgeable
  bulk, and the reason anyone runs this at all.
- **Derived** — the distilled essence, the index entry. Cheap; kept by default.
  A purged session stays in the index MARKED purged, because a session that
  vanishes from the index is indistinguishable from one that never existed.
- **Human-authored** — plan files, design notes, write-ups. Never negotiable.

The third class carries a further requirement that is easy to miss: authored
files are not merely preserved, they are NAMED in the report. A file that is
kept but never mentioned is a file nobody will ever file, which over a long
enough window is the same outcome as deleting it, only slower.

### The unclassified middle (B13)

The split above is not clean on disk today, and the gap is verified rather than
suspected. A harp directory already has a declared durability contract:
`persist/` is mounted into containers and `ephemeral/` is deliberately not,
because `ephemeral/` is where the scratch worktrees live and dragging those
into a container is the opposite of what anyone wants.

Authored artifacts land in neither. The plan-stamping convention writes them at
the harp directory's TOP LEVEL — an unclassified middle with no declared
durability — so **a containerized agent writing a design note into its own
session directory writes into container-ephemeral space and loses it on exit**,
actively encouraged to do so by the convention. The recommended fix is to
extend the convention rather than widen the mount, and the doctor check that
notices the condition is one of the scenarios here.

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
  unmerged or unowned trees, no live session, no sweeping a session nobody
  ever summarised, no touching vendor stores.

Several scenarios here enforce individual lines of that list directly, which is
the point of writing it as a feature file rather than a paragraph.

### The lessons skill is a security boundary, not a convenience

A distillable, distributable lessons skill sounds like a small ergonomic
feature and is not. It reads whole session transcripts — the most sensitive
artifact ctxloom holds — and decides what gets written back into checked-in,
signed, team-distributed fragments. A hostile one is simultaneously a
context-exfiltration primitive and a context-poisoning primitive, and it is a
strictly higher-value target than any ordinary fragment.

That is the argument FOR routing it through `trust.KindSkill` rather than any
ad-hoc mechanism: a config key naming a file path would bypass trust entirely,
while a skill rides the same per-item, hash-bound choke as every other skill,
so a changed lessons skill re-enters review like any other content change.

The consumer half does not exist yet: `session distill` accepts neither
`--skill` nor `--to-bundle`, so there is nowhere for a trust decision to be
consulted. The signal it will have to honour is already built —
`operations.GetSkill` returns `errs.ErrSkillWithheld` out of the gated exposure
pipeline — and the requirement is that a withheld skill fails loud there rather
than falling back to the embedded prompt. That scenario is here.

The degrade is not hypothetical: ctxloom ships it once already, on a different
command. `cli.loadDistillPrompt` swallows every error from its lookup —
including "this content is withheld pending review" — and silently falls back
to the embedded prompt, so on `bundle distill` and `fragment distill` a trust
decision quietly changes which prompt runs and the user is never told. That is
tracked on its own; it is *not* on `session distill`'s path, which runs
`runSessionDistill` -> `compactEntry` -> `memory.NewCompactor`.

And the lessons output must be SIGNED in the same apply, which sounds like
belt-and-braces until you connect it to J001900: an edited-unsigned bundle is
silently withheld once a pin advances. A lessons flow ending unsigned would
manufacture the exact defect it exists to close, quietly, in the one flow whose
entire purpose is not losing knowledge.

<!-- doc:outro -->
The routine that ties the four legs together is deliberately not a new
top-level verb. `cleanup` as a command was raised during design and settled
against, by a convention this repo argued once and does not re-litigate: the
CLI is noun-verb, and a verb-first exception optimizes for a CLI a third this
size. So cleanup dissolves into noun-homed leaves, and the
one-thing-you-run affordance comes back through ctxloom's own mechanism — a
first-party signed bundle command reached by `run -r`, with the shipped
`check-triggers` command as the existing precedent.

The honest cost is stated where it belongs: `run -r` is LLM-driven, so the
routine is agentic rather than deterministic. Anyone wanting determinism
scripts the four leaves in shell, and ctxloom does not need to own that script.

The property that makes this worth building rather than merely worth having is
the dogfooding one. Because the lessons skill is bundle content, a team ships
its house extraction style exactly the way it ships any other context — signed,
reviewed, versioned, pinned. ctxloom's own behaviour flows through ctxloom's
own supply chain. That is the strongest available argument that the supply
chain is worth anything: the product uses it on itself, for the thing it cares
about most.

Five of the fifteen scenarios are still `@wip` and red — the lessons-extraction
group and the `cleanup` routine — and each carries its own untag condition in
the feature file. The other ten pass, and each says what closed it. None of the
fifteen can be turned green by weakening an assertion; the guard added after the
first run makes sure of that.
<!-- /doc:outro -->
