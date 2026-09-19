# run-20260918-sonnet-perpremise

The fragment trial's EXACT method and model, applied to the skills corpus, so the two
trials are comparable: `claude --print --model claude-sonnet-5`, ONE call per premise
("consider this premise ON ITS OWN, go through every situation"), 89 situations, foreground,
100s cap per call. raw_<skill>.txt is each call's answer; answers.txt is the per-situation
form the scorer reads.

    condition                    recall  precision  false-fire  exact   invoked  missed
    haiku, menu (engine instr)   0.678   0.983      0/17        65/89   0.763    0.612
    sonnet, per-premise          0.747   0.985      0/17        72/89   0.737    0.755
    fragment trial, same method  0.93    --         --          --      (its corpus had no missed set)

Run because the human asked why this trial's ~0.68 sat so far below the fragment trial's
~0.93. Three named differences: MODEL (that was sonnet, this had been haiku), METHOD (that
was per-premise, this had been the engine's real menu shape), and CORPUS (that had no
"should have fired" set; this trial's missed set is half its situations). Matching model
and method closes ~7 points. On the invoked set -- the only set the fragment corpus had --
skills score 0.74-0.79 against fragments' 0.93 under identical conditions. The remainder is
the two kinds behaving differently, not a trial artifact.
