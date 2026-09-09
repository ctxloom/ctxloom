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

**The POINTER is pushed; the index is not.** An agent that never learns the catalog
exists never asks for it, and every fragment it would have selected stays invisible —
so the ctxloom MCP server's session instructions carry the selection guidance and name
the `ctxloom://fragments` resource. That is not a pushed index and does not reintroduce
what this section rules out: no premise, no name and no body travels with it, its size
does not grow with the corpus, and the agent still has to ask at the moment it has
something to match. `mcp.premiseCatalogInstruction` composes it from
`operations.PremiseSelectionInstruction` rather than restating it, so the measured
wording has exactly one source.

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

## Skills and premised fragments are not the same thing

Both put a short description in the model's context and load a body only when the model
judges it applies. The MECHANISM is identical, which is why mapping one onto the other
works at all. What they CARRY is not.

An Agent Skill is a **capability**: a procedure the model performs, often with an
executable payload (`SKILL.md` plus `scripts/`, `assets/`). `admit` reads "Admit work to
the nightly queue — roll the live queue tag forward, triage every candidate…". There are
steps, and doing them is the point.

A premised fragment is **conditional guidance**: knowledge that shapes how the model works
while it does something else. `config-hierarchy` reads "You are adding or changing a flag,
an environment variable, or a config key…". There is nothing to perform.

That difference is why the two are not interchangeable by default. A vendor's
skill-selection heuristic is tuned for "should I invoke this capability", and a premise
asks "does this guidance apply to what I am about to do". Those may or may not be the same
judgement — see the ruling below for why we have not measured which.

## Ruling: MCP resolution is preferred; skills are the fallback

Where ctxloom is IN THE LOOP, conditional fragments resolve over MCP. Where it is OUT OF
THE LOOP — a materialized surface, launched with ctxloom absent — premised fragments are
emitted as skill packages instead, because that is the only progressive disclosure
available there. `profile_materialize` is the only caller that emits them, and that is
deliberate: a live session must not receive the same fragment as both a skill and an index
entry, which would be double delivery under two different selection stories.

Three reasons the in-loop default is MCP, none of them taste:

- **The selection instruction we measured is ours.** The properties above were established
  against ctxloom's own index prompt. A vendor's skill heuristic is opaque and has not been
  measured here.
- **Freshness.** A materialized skill package is a COPY, gated when it was written. MCP
  reads live content through the pipeline, so an edited fragment — or a signature revoked
  after materialization — takes effect at once.
- **Observability.** ctxloom can see which fragments a session asked for. It cannot see
  which skills an engine chose to load, which costs the distillation loop its input.

The open question is whether skill-form descriptions preserve the recall the properties
above bought. It is NOT cheap to answer: the fixture, runs and scorer were deliberately
removed (see below), so settling it means recovering deleted files from history and
re-running. Until someone pays that, preferring the measured mechanism is the conservative
choice rather than a claim that skills select worse.

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
- **The measurement apparatus has been REMOVED, deliberately.** The fixture, the runs and
  the scorer are gone; the numbers above are the record of a decision that was made, not a
  claim you can re-derive from this tree. They are not re-derived on read, and they can no
  longer be re-derived at all.

  The decision they informed — premise-based context delivery becomes the default — was
  taken on them and is not being revisited by re-running the scorer. The apparatus was
  kept only long enough to answer that question, and a large unused measurement corpus is
  weight without a reader. Recovering it means recovering the deleted files from history
  and re-running, which is the accepted cost.

  Read the caveats above as permanent, then: they bound what these numbers ever supported,
  and nothing will sharpen them.
