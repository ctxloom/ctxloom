# run-20260918-sonnet-boolean — a NEGATIVE result

Hypothesis: the pair misses (both skills found in only 2 of 15 two-skill situations) come
from premises written as DISJUNCTIONS ("asked what is pending, or blocked, or ... any
wording"), which a per-premise judge half-fires. Rewrite each as ONE discrete boolean
condition (corpus_bool.yaml), rescore under the same method and model as
run-20260918-sonnet-perpremise.

    condition              recall  precision  false-fire  exact   single-skill  both-found
    disjunctive premises   0.747   0.985      0/17        72/89   53/57         2/15
    boolean premises       0.667   0.983      0/17        64/89   47/57         1/15

Refuted, and it cost recall: the human's actual words the disjunctions carried ("preflight",
"good night", "close out") were doing real matching work on the single-skill set, and
removing them lost six of those. The pairs did not move. The pair problem is not wording:
judged alone against "lets admit the 2s and preflight them", the `unattended` premise is
correctly FALSE at that moment -- the queue is not admitted yet. The second skill's
condition becomes true only AFTER the first runs, and no per-moment judgement of a
premise can see that.

The three calls that timed out at 100s on the first pass (closeout, unattended,
check-triggers) completed in 48-74s on retry with a 280s cap; that was latency, not a
result, and the retried answers are the ones scored.
