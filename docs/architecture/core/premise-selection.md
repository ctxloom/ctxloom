# Premise selection — conditional context, and what it costs

A profile's fragments are all loaded, every session, whether or not they apply. Most
do not: guidance about cutting a release is paid for while you are reading a file.
A **premise** makes a fragment conditional — it is withheld, its name and premise are
offered in an index, and an agent asks for it when the moment arrives.

**Measured on this project's own corpus: 92.9% of applicable guidance still reaches the
agent, for 4.5x less context.** Loading everything costs ~17.1k tokens; selection costs
~3.8k.

That 4.5x is the whole-context figure and it is the honest one. The premised portion
alone shrinks 12.5x, but three fragments carry no premise and are paid regardless, so
quoting the larger number would hide a fixed cost the user still pays.

## What it is worth, and where it stops being worth it

The mechanism exists to reduce context, so the reduction is the constraint. At an
average 2.0 fragments selected per moment against 27 premised, there is a great deal of
headroom: selection could grow several-fold and still pay. Precision is therefore a
**floor to watch, not a target to maximise** — see the ruling below.

## The loop

1. `premiseFilter.withhold` holds back any fragment carrying a premise and records an
   index row. A fragment with NO premise is always loaded: absence asserts that it
   applies unconditionally, and that is what keeps the mechanism additive — a corpus
   authoring no premises withholds nothing and assembles the exact bytes it did before.
   `TestAssembleContext_PremiselessCorpusIsByteIdentical` pins it.
2. `RenderPremiseIndex` renders the offer: each fragment's **qualified reference** and
   its premise, plus the instruction for deciding between them.
3. The agent selects, and asks for what it chose — over MCP `assemble_context`, or over
   the CLI with `ctxloom fragment premises` followed by `ctxloom fragment show <ref>`.
4. `newPremiseFilter(explicit)` takes the selection. An explicit ask always loads: the
   ask IS the selection, and a premise that could veto it would stop the loop closing.

The index is **pulled, not pushed**, and that is the design. Assembled context is
delivered once at launch into the session's system prompt; in-process subagents inherit
it wholesale and ctxloom does not mediate them, so a pushed index can be neither
tailored nor delivered to the children doing much of the work. A command any agent can
run needs none of that, and it asks at the moment it has something to match — the only
time the answer means anything.

## The three properties in the prompt, and why they are there

`RenderPremiseIndex`'s wording is load-bearing. Against 59 situations mined from 86 real
session transcripts and labelled by a pass that had never seen the premises:

| property | without it | with it |
|---|---|---|
| **judge each premise on its own** | 0.49 recall — a menu makes premises compete, and one fragment is returned for ~93% of moments | 0.76 |
| **borderline resolves toward including** | 0.76 | 0.83 |
| **match the imminent action, not the context** | 0.43 — a 25k-token window dilutes the moment | 0.93 |

`TestRenderPremiseIndex_KeepsTheThreeMeasuredProperties` asserts each one with the
failure it prevents; seven hand mutations against the instruction all die.

## What was measured and did NOT work

Recorded because each is a plausible idea someone will otherwise retry:

- **Telling the model context is costly, or that selecting nothing is often right.** No
  effect on recall, slightly negative. Written to prevent over-selection; the measured
  failure was always under-selection.
- **Broadening the premises.** Thirteen widened, no recall change. What did help was
  disambiguating two premises that OVERLAPPED (`turn-gates` and `green-is-not-passing`
  competed for the same moments) and narrowing one that fired where nothing wanted it.
- **Noun tags presented as subject-matter hints.** No effect — but that instruction said
  the premise decides applicability, which excluded tags from the decision. Presented as
  valid grounds for inclusion in their own right, they lift recall 0.76 → 0.83, with two
  variables moved at once (see the limits below).
- **Giving the model more context.** A 25k-token window of real history in place of a
  one-line intent HALVED recall and doubled selections. Reframing it explicitly as
  background, with the imminent action named, tightened selection but dropped recall
  further to 0.43. The moment is the signal; the span around it is noise.

## Invariants

- A fragment with no premise is always loaded. Absence is an assertion, not an omission.
- The index hands out **qualified references** (`bundle#fragments/name`), never bare
  names. `Catalog.ResolveFragmentAsk` resolves a bare ask that matches several bundles to
  the first in List order with only a warning — and `general` is defined in seventeen
  code-review bundles, so a bare ask for one lens silently delivers another.
- The selection crossing into `newPremiseFilter` is a plain `[]string` of names. That is
  the stochastic boundary: everything on both sides is deterministic, so no test needs a
  model. Do not widen it to carry ordering, confidence or content from the model.
- Selection quality is never a CI gate. Thresholding a model's judgement produces a red
  that flips on temperature rather than on a defect.

## Ruling: over-select rather than under-select

The two errors are not equal. An over-offered fragment costs context; a withheld one is
never learned to exist, cannot be asked for, and the agent proceeds without guidance it
was meant to have. Scoring therefore leads with **F2** (recall weighted twice), not F1 —
F1 ranks a run that offered less above one that found more.

## Divergence — what the numbers do not cover

- **Every selection run had the fragment bodies in its context.** ctxloom delivers
  assembled context into the session's system prompt and subagents inherit it; a probe
  answering from context alone, with no tool calls, quoted three fragment headings
  verbatim. Comparisons between arms survive (both carried it); absolute figures are not
  production estimates.
- **Situations are one line each**, which is thinner than a real moment. This biases the
  opposite way, and the two biases are of unmeasured relative size.
- **Per-fragment verdicts are unreliable.** 17 of 27 fragments have one or two situations
  expecting them, so "this premise never fires" is often n=1.
- **The tags result moved two variables together** — tags elevated to valid grounds, and
  the tie-break made permissive. The separating arm has not been run.
- The fixture, the runs and the scorer live in `internal/operations/testdata/premise_runs`
  and `scripts/score_premises.py`. The measurement is reproducible; the numbers above are
  not re-derived on read.
