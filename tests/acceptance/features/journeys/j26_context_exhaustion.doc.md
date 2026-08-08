# Running out of context, and getting the thread back

You are deep in a piece of work. The reasoning that got you here — the three
approaches you ruled out, the constraint you discovered on the second attempt,
the reason the obvious fix is wrong — lives in the conversation and nowhere
else. Then the context window fills up.

There are two ways out, and choosing between them is easier once you know they
are not the same operation.

## Compaction summarises in place

Your harness's own compaction rewrites the conversation: the assistant reads
what is still in the window and replaces it with a summary of itself. It
happens in the window, using the context that is already running short, and
what survives is what the assistant chose to keep while under exactly the
pressure that made you reach for the command.

That is often fine. It is fast, it needs nothing, and it keeps you working.

## Clearing throws the window away — and keeps the record

`/clear` empties the conversation. What it does not do is end the session. The
session stays alive under the same name, and its transcript is still on disk,
still growing.

That is what makes `/recover` possible. It reads that transcript in a **separate
process**, against the **whole record** rather than what fits in the window,
with a **fresh budget** — and hands back one bounded artifact. The agent that
was short of context never has to write its own summary.

The practical differences:

| | Compaction | `/clear` then `/recover` |
|---|---|---|
| Who summarises | the assistant, in the window | a separate process, out of band |
| What it can see | what is left in the window | the entire transcript |
| Budget | the one you have run out of | a fresh one |
| The original | gone from the conversation | still on disk |
| Repeatable | no | yes — ask again, or ask later |

The last row is the one people underestimate. After a compaction, the detail is
gone. After a clear, it is still on disk, so a recovery that came back thinner
than you wanted can simply be run again — and a session that keeps going can be
recovered again later, picking up everything that happened in between.

## ctxloom tells you when it can help

Your harness reports why a session started, and it distinguishes a clear from a
compaction. So after a `/clear`, ctxloom says — to you, not to the model:

> ctxloom: context cleared. Run /recover to bring your pre-clear context back.

After a compaction it says nothing, because there is nothing to bring back: the
summary is already in front of you.

It also stays quiet when a recovery would come back empty — a session that
recorded nothing, or one whose transcript was never written. An offer to restore
something that does not exist spends the one moment of attention you have on a
dead end.

## What comes back

A distillation, not a replay. The recovered artifact is bounded — it has to
fit in the window you just cleared, or it has solved nothing — so it carries the
conclusions and decisions rather than the full conversation. In a real recovery
of a long session, that has meant roughly 104,000 tokens of transcript coming
back as about 1,600 tokens of essence.

If you want the original detail, it is still there. That is the whole point of
the trade.

## Related

- Recovering an **earlier** session, rather than the one you are in, is
  `session search` / `session show` / `run --session` — see the archaeologist
  journey. `/clear` keeps the same session alive, which is why recovery after a
  clear reads the current session and not the previous one.
- Capture — how a session's transcript comes to exist at all, including for
  engines driven through their own interactive interface — is its own journey.
