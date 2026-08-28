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
