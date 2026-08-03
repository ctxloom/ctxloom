<!--
J24 narration companion (j24_engine_switch.feature) — FLOWS-UNIFIED.md's U12.

Short, because the journey is short and its finding is a single sentence. Every
claim traces to a named scenario in the sibling feature file; the validation
behaviour described under "Two typos" was measured while wiring it, and differs
from what was previously reported.
-->

<!-- doc:intro -->
A pricing change, a policy change, a vendor doing something nobody liked, and
the team is moving to a different engine.

This is the day ctxloom's premise gets tested, because the premise is that the
context is yours. The fragments, the profiles, the trust decisions and the
history belong to the team rather than to whichever assistant was fashionable
when they were written. If that is true, switching engines is a Tuesday. If it
is not, it is a migration project, and the tool that promised portability is
one of the things being migrated.

The good news is in the first scenario, and it is real: the engine changes
under the binding and the composed context does not. Byte-for-byte, on either
side of the swap, delivered into each engine's own native idiom. The team's
guidance is not rewritten, re-authored or re-approved. That is the capability
working exactly as advertised, and it is not a small thing to have built.
<!-- /doc:intro -->

## The finding is the absence of a flow

Everything U12 needs exists. `agent edit --engine` swaps the binding. Profiles
are engine-neutral by construction. Each engine's native surfaces are written
from one composed context. The canonical transcript keeps the history readable
across the switch, and history recorded under the old engine is still listed
after it — the second green scenario here, cheap to assert and the property
most likely to be broken silently by an unrelated change, since nobody looks
for old sessions until they need one.

What does not exist is any narration of ORDER or VERIFICATION. Nothing tells a
team what to do first, what to check afterwards, or what they just gave up.
Nothing is structural here for the mainstream engines, which is precisely the
finding: **the portability story is sellable and untold.** It is the rare gap
that is pure upside, because the hard part is already paid for.

## Two typos, two different silences

On migration day every engine name in play is unfamiliar. Typing one wrong is
the likeliest mistake anyone will make, and both commands that take one handle
it badly, in different ways.

`ctxloom agent edit dev --engine bogus-engine` exits 0 and writes the
nonexistent engine into the binding. Nothing validates the name at the moment
it becomes the team's configuration. The failure surfaces later, somewhere
else, as whatever a missing engine happens to look like downstream — and by
then the connection back to a typo in an unrelated command is gone.

`ctxloom manage install --engine bogus-engine` was previously reported as
exiting 0. Measured here against an already-initialized project, it does not:
it exits non-zero saying `.ctxloom already exists, and the engine is only
recorded while scaffolding it`, and points at `ctxloom llm default`.

That refusal is incidental, and the distinction matters. It rejects the
invocation for a reason having nothing to do with the engine name — feed it a
perfectly valid engine and you get the identical message. So the one thing it
never tells a user typing an unfamiliar engine name is that they typed it
wrong. The error that fires is about directory state, on the day the user is
thinking about engines.

## What she gave up

The last scenario asks the question a migrating team actually asks, which is
not "did it work" — they can see that — but "what did I just lose?"

On this exact engine pair the answer is concrete and verified: codex has no
`session_end` hook. A team moving from claude-code to codex loses a hook event
they may well have built on, and no surface anywhere mentions it, before or
after. They will find out when something stops happening, on a day they are no
longer thinking about the migration.

<!-- doc:outro -->
This is the same missing capability that appears in U3 wearing different
clothes. There, a new hire's opencode-using deskmate silently receives the team
bundle without the team's guardrails, and nothing tells either of them. Here, a
whole team silently loses a hook event. Both are the absence of one report:
what does this engine binding NOT carry?

That report would close two journeys' worth of findings at once, and it needs
no new protocol work — the per-engine capability matrix is already known to the
product, since the code has to branch on it to function. It is known
internally and never said out loud.

Five scenarios, all `@wip` with untag conditions in the feature file. Two pass:
the swap itself, and the survival of history across it. Which is to say the
expensive half works and the cheap half was never written.
<!-- /doc:outro -->
