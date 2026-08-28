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

## Reframing does not rescue the wide window

The first wide-window prompt presented the history as a session and asked which
guidance applied, which left open whether the model was matching the whole span
rather than the final action. Re-run with the framing corrected: the history is
BACKGROUND, assume you are that agent mid-task, the [ABOUT TO DO] line is the
action you are selecting for, work finished earlier is done.

                       recall   prec     F2   drops  mean sel  cost
  one-line intent       0.929  0.542  0.812   1/14     2.00     8%
  25k window            0.571  0.148  0.364   6/14     4.50    20%
  25k window REFRAMED   0.429  0.182  0.337   8/14     2.75    13%

The framing did what it appeared to row by row -- selection tightened sharply
(4.50 fragments to 2.75, 20% of corpus to 13%) and precision rose. It paid for
that in true positives: S29 lost close-the-turn, which it had previously found,
and drops rose from 6 to 8. Under the over-select ruling that is the wrong trade
twice, and it remains far below the one-line arm either way.

CAVEAT THAT DOES NOT CHANGE IT: S14 never read its prompt (zero tool calls, and
it answered with a name outside the vocabulary entirely -- a skill from its own
harness context). It scores as a miss. Excluding it, recall is ~0.46, still less
than half the one-line arm.

SO THE DILUTION FINDING SURVIVES ITS BEST CHALLENGE. The problem is not that the
model was pointed at the wrong part of the input. A large context degrades
matching for the imminent action, and saying "this is background, match the last
line" does not recover what the extra material costs.

ONE HARNESS ARTEFACT WORTH KNOWING before anyone reuses this design: telling a
model to "assume you are this agent" makes WHICH agent ambiguous when the model
already is one. S14 answered from its own situation instead of the supplied one.
In production that ambiguity does not exist -- the agent genuinely is the one at
that moment -- so this is a cost of testing the framing by proxy, not a property
of the framing itself.
