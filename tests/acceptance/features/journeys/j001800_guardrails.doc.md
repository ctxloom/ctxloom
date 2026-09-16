<!--
J001800 narration companion.

Prose ONLY. It never restates what the Gherkin already says business-readably,
and it carries no assertions of its own — j001800_guardrails.feature next to it is
the single source of truth for what J001800 promises.

Marker convention: an opening prose block, one block per scenario keyed to that
scenario's EXACT name, and a closing block.
-->

<!-- doc:intro -->
Every other journey in this suite is about delivery — getting the right
context to the right assistant, signed, trusted, current. This one is about
what happens after delivery succeeds, because delivery succeeding is not the
same thing as the problem being solved.

An LLM coding agent that has just been handed a fragment saying "run tests
through the task runner" will still, sometimes, type `go test` out of habit.
One that has just been told to check for an existing helper before writing a
new one will still, sometimes, write a fourth copy of it anyway. This is not a
hypothetical edge case picked to make a point — it is the single most
characteristic failure mode of an LLM coding agent, documented and re-observed
across this entire project. A fragment can only ever *ask*. It has no way to
notice the ask was ignored.

That gap is why ctxloom ships companions: **ltk**, which watches shell
commands and file edits and redirects the ones a project has rules for, and
**reprise**, which watches for reimplemented duplicates and catches them
mechanically at commit time. Neither is a feature of the LLM. Both are
ordinary, deterministic programs, doing the boring, reliable thing an LLM
cannot reliably do for itself: noticing when the ask didn't land.

ctxloom's own job stops one layer short of that. ctxloom delivers each
companion's guidance into the assembled context and wires its hook into the
assistant's real, generated configuration — it does not, and cannot, prove
that the companion's own mechanism actually fires during a live session, any
more than handing someone a rulebook proves they read it. That proof belongs
to each companion's own test suite. What belongs here, and had no acceptance
coverage anywhere in this project before this file existed, is the delivery
half: does the guidance actually reach the assistant, does the hook wiring
actually reach its generated settings, is what reaches it honest about what
the companion does — and does all of that keep working, without failing
anything, on a machine where a companion simply isn't installed.
<!-- /doc:intro -->

<!-- doc:scenario: Alice's assistant receives the task-runner and reuse-before-you-write guidance -->
This is the baseline claim: install both companions, start a session, and
check what actually reaches the assistant. Not "ctxloom does not crash" —
the assembled context genuinely carries ltk's real fragment (the exact
prose ltk itself ships, not a paraphrase) and reprise's reuse-discipline
guidance, and the generated engine configuration genuinely wires ltk's hook
onto every shell command and file edit, in the shape the real engine reads.

This is also, quietly, the more interesting half of the story: ctxloom
discovers these companions automatically. Nobody adds ltk or reprise to a
profile, references them in a bundle, or opts into anything. They are found
on PATH, their self-described loadout is verified against the same trust
gate a remote bundle goes through, and — signed and trusted — their guidance
simply arrives. That automatic reach is the whole value proposition of a
companion over a fragment that only asks a human to go install something.
<!-- /doc:scenario -->

<!-- doc:scenario: ltk's own guidance is honest about what it does — a redirect, not a block -->
This scenario exists because of a mistake this project already made once,
elsewhere: a website truthfulness audit caught this project overclaiming what
one of its own mechanisms actually does. ltk is exactly the kind of thing
that invites the same mistake, because "redirects your shell command" sounds,
if you say it carelessly, like a sandbox or a security boundary. It is not
one, and ltk's own fragment says so in plain words: it is **a cooperative
redirect, not a sandbox**. It returns a message and a suggestion. An agent
explicitly instructed to work around it can. For a real "never" boundary, the
fragment itself points at running the agent in a container instead.

ctxloom's job here is narrow and non-negotiable: deliver that sentence
intact. Not a stronger version of it, not a softened one — the same honest
self-description ltk itself ships. This scenario checks the assembled context
for exactly that sentence, verbatim.
<!-- /doc:scenario -->

<!-- doc:scenario: Bob, without either companion installed, still gets the team's context and nothing fails -->
Bob's machine is not a clone of Alice's. He may never have installed ltk or
reprise at all — plenty of real engineers haven't. The companion mechanism
has to be a pure enhancement, never a dependency: the team's own context still
has to reach him in full, and nothing about starting a session may fail just
because two entirely optional binaries are missing from his PATH.

This is the other direction of the same claim scenario 1 makes, and both
directions matter equally. A delivery mechanism that only works when every
tool happens to be installed is not a guardrail — it is a trap for whoever's
machine looks slightly different from the one it was built on.
<!-- /doc:scenario -->

<!-- doc:outro -->
Delivery and enforcement are two different jobs, done by two different kinds
of thing, and conflating them is exactly how a project ends up overselling
itself. ctxloom's job is to get the standard to the assistant, honestly
described, whether or not the tools that enforce it happen to be on this
particular machine. The companions' job is to notice, mechanically, the
moment guidance alone stops being enough. Neither one is a substitute for the
other, and this journey is the proof that ctxloom holds up its own half of
that bargain.

For the trust mechanism every companion's loadout is verified through before
any of its content reaches an assistant, see
[J000200](/journeys/j000200-setup/) and the
[trust-surface reference](/journeys/trust-surface/).
<!-- /doc:outro -->
