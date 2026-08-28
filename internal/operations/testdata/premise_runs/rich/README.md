# Rich-context arm: 25k-token windows (NEGATIVE RESULT)

Hypothesis: a one-line intent is too thin to match against, and a real agent has
far more context, so matching would improve with a realistic window.

Method: for 12 situations that carry a verbatim example line and ground truth,
the transcript was replayed and the ~100,000 characters (~25k tokens) PRECEDING
that exact moment captured -- assistant prose, user turns and prior tool calls,
ending with the action about to be taken. One agent per window, the full 27-entry
tagged index, the permissive instruction that performed best elsewhere.

Scored against the SAME 12 rows under the one-line input:

                   recall   prec     F2   drops  mean sel  cost
  one-line intent   0.929  0.542  0.812   1/14     2.00     8% of corpus
  25k window        0.571  0.148  0.364   6/14     4.50    20% of corpus

WORSE ON EVERY AXIS. It selected more than twice as much and found less. S27 lost
both its expected fragments while choosing five others; S30 lost both while
choosing seven.

MECHANISM, INFERRED: dilution. A 25k window holds an hour of work, and the model
matches against the whole span rather than the imminent action at its end,
picking up fragments relevant to earlier activity and losing the next step. The
one-line intent is not a thin proxy for the moment; it IS the moment, stated
cleanly.

CAVEAT, AND IT CUTS ONE WAY ONLY: ground truth was labelled against the
one-liners, so PRECISION is scored unfairly here -- a fragment genuinely relevant
to something inside the window counts as a false positive. That does not touch
RECALL. The window contains the moment, the expected fragments still apply, and
losing 6 of 14 that were found from a single line is degradation, not artifact.

CONSEQUENCE FOR THE DESIGN: the production callback should ask the agent what it
is ABOUT TO DO and match on that, not ship it a context window. Cheap to obtain,
cheap to send, and it is what the mechanism was already shaped around.
