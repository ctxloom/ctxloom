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
