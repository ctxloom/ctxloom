# run-20260918-sonnet-linkpointer — a second NEGATIVE result

Hypothesis (the human's): paired skills need not be merged into one line. Each keeps its
own when_to_use, and a link group contributes a short pointer -- "(often relevant
alongside: unattended)" -- so the model sees the association at the moment it sees either
member and sequences them itself.

Two conditions from the SAME premises, so the pointer is the only variable. Both use the
premises TRIMMED to fit the engine's 250-char line with the pointer intact; the corpus
premises as authored were 390-663 chars and would all have been truncated before the
pointer was reached. The judge sees the FULL listing, then judges one skill.

    condition                         recall  precision  false-fire  exact   single  both-found
    baseline: premise alone, untrimmed 0.747   0.985      0/17        72/89   53/57   2/15
    control: full listing, no pointer  0.759   0.957      2/17        66/89   48/57   4/15
    pointer: full listing, with hint   0.690   0.938      3/17        62/89   46/57   2/15

Refuted, in the wrong direction on every axis. The pointer LOWERED both-found (4 -> 2),
lowered single-skill recall, and added a false fire: the model read "often relevant
alongside" as licence to fire the mate on moments where it did not apply, while still
not firing it on the pairs. Separately, showing the judge the full listing at all --
control vs baseline -- is what cost the 0/17 false-fire cleanliness; premise-alone
judgement is the more precise shape.

Across five conditions now (haiku menu, sonnet per-premise, boolean, listing-control,
pointer), both-found on the 15 two-skill situations has never exceeded 4. The consequent
skill's condition is genuinely false at the moment the human speaks, and no text in the
listing changes what the moment contains.
