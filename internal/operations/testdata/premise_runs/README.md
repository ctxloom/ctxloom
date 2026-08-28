# Selection runs

Raw answers from selection runs against premise_corpus_v1, scored by
scripts/score_premises.py against premise_situations_mined.yaml.

Each run saw ONLY the premise index -- 27 names and their premises, no bodies --
which is what production shows a model. The ground truth was written by a pass
that never saw the premises. Neither side saw the other.

Model: claude sonnet, three identical runs, 2026-08-27. Recorded because a
number without its model and date stops being comparable the moment either
changes.

CAVEAT ON THE SHAPE OF THE RUN: all 59 situations were presented together,
whereas production judges one moment at a time. A model that can see the whole
set may calibrate against its neighbours in a way it cannot in real use, so
these are an OPTIMISTIC bound rather than a faithful estimate.

NOT A GATE. Selection is stochastic; thresholding it in CI would produce a red
that flips on temperature rather than on a defect.

## Result, three runs

              A       B       C
  precision   0.738   0.769   0.775
  recall      0.492   0.476   0.492
  F1          0.590   0.588   0.602
  silent drop 0.508   0.524   0.508
  mean sel    0.71    0.66    0.68     (ground truth expects 1.07)
  false fire  0.071   0.071   0.000
  exact set   28/59   28/59   29/59

48 of 59 rows (81%) identical across all three; recall spans 0.476-0.492. The
variance question is settled: this is systematic, not sampling noise.

## What it says

UNDER-selection, not over-selection. False fire is near zero and mean selected
sits BELOW mean expected. Half of all applicable guidance is never offered, and
guidance never offered is guidance the agent never learns exists.

Strongest evidence, by number of opportunities:
  green-is-not-passing   dropped 18 of 24  (75%)
  turn-gates             dropped 16 of 30  (53%)
  delegation             dropped  8 of 12  (67%)
  problem-solving        dropped  7 of 12  (58%)
  prompt-authoring       dropped  5 of  6  (83%)
  preexisting-ownership  dropped  5 of  6  (83%)

Eight more were never offered at all, but each had only one or two chances in
this fixture, so individually that is weak evidence; collectively it is a
pattern worth acting on, and more situations would sharpen it.

coordination-tools is the only premise firing where nothing expects it, and is
also the fragment the blind labeller never chose in 59 situations. Two
independent signals agree it is mis-premised or already covered by delegation.

## Per-premise protocol

Each premise judged ALONE against all 59 situations, so no two premises ever
compete for one slot. Same corpus (v2), same ground truth.

                list-selection (best)   per-premise
  recall            0.571                 0.762
  silent drop       0.429                 0.238
  F1                0.673                 0.706
  precision         0.818                 0.658
  false fire        0.071                 0.286
  mean selected     0.75                  1.24   (expected 1.07)

The protocol, not the premises, was the binding constraint. List-selection
returns roughly one fragment per moment in every condition tried -- two prompts,
two corpora -- because presenting a menu makes premises compete. Removing the
competition lifts recall by 55% and halves silent drop, and costs precision and
false fire.

## What this separates, which nothing else could

Running each premise alone sorts the misses into two classes that look identical
under list-selection:

  COMPETITION CASUALTIES -- fire immediately once alone, so the premise was
  never the problem: ltk (0 -> 5 situations), worktree-isolation (0 -> 2),
  sequential-thinking-usage (0 -> 3), config-hierarchy, fail-loud-launch,
  isolation-axes (0 -> 1 each).

  UNFINDABLE -- still zero with no competition, so the premise itself is at
  fault: unchecked-bindings, prototype, coordination-tools.

The second class shares one property. Their trigger conditions on information
the MOMENT DOES NOT CARRY. unchecked-bindings fires on "writing prose that names
something specific", which is invisible from "I'm recording a decision" and only
becomes true mid-sentence. prototype fires on being TEMPTED to add a shim, and a
temptation is not in a statement of what you are about to do. Contrast
worktree-lifecycle at 0.85, whose every trigger word is present in the moment as
stated.

So: a premise must key on something OBSERVABLE IN THE MOMENT AS THE AGENT WOULD
STATE IT, not on a property that becomes visible only once the work is underway.
Word overlap with the situations supports it -- 0.243 for hits against 0.107 for
misses -- but is not the mechanism: design-review-checkpoints and close-the-turn
hit at ZERO lexical overlap. Observability, not vocabulary.
