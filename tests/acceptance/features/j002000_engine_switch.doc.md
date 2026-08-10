<!--
J002000 narration companion (j002000_engine_switch.feature) — FLOWS-UNIFIED.md's U12.

Short, because the journey is short and its finding is a single sentence. Every
claim traces to a named scenario in the sibling feature file; the validation
behaviour described under "Two typos" was measured while wiring it, and differs
from what was previously reported. Both typos are now caught — see the update
at the end of that section.
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

Everything U12 needs exists. `agent edit --llm` swaps the binding. Profiles
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

## Two typos, two different silences (now both caught)

On migration day every engine name in play is unfamiliar. Typing one wrong is
the likeliest mistake anyone will make, and both commands that take one used
to handle it badly, in different ways.

`ctxloom agent edit dev --llm bogus-engine` used to exit 0 and write the
nonexistent engine into the binding. Nothing validated the name at the moment
it became the team's configuration, so the failure would have surfaced later,
somewhere else, as whatever a missing engine happens to look like downstream —
and by then the connection back to a typo in an unrelated command is gone.
`operations.SetAgent`'s `validateAgentAxes` now checks the engine against
`operations.AvailableLLMNames` before the write, so a typo'd edit is refused,
not persisted.

`ctxloom manage install --engine bogus-engine` was previously reported as
exiting 0. Measured against an already-initialized project, it did not: it
exited non-zero saying `.ctxloom already exists, and the engine is only
recorded while scaffolding it`, and pointed at `ctxloom llm default`.

That refusal was incidental, and the distinction mattered. It rejected the
invocation for a reason having nothing to do with the engine name — feed it a
perfectly valid engine and the message was identical. So the one thing it
never told a user typing an unfamiliar engine name was that they typed it
wrong. The error fired about directory state, on the day the user is thinking
about engines.

`cli.checkEngineKnown` (`internal/cli/manage.go`) now runs BEFORE that
already-exists check, so the diagnosis is about the argument even when
`.ctxloom` already exists. Its roster is `backends.List()`, not
`operations.AvailableLLMNames`: unlike the agent-binding case above, this flag
ends up as the TYPE of a real `{type: engine}` LM config entry
(`operations.engineRegistry`/`fallbackRegistry`) when scaffolding, not a
resolved label, so a project's own declared LLM labels are not valid answers
here. Because the roster is `backends.List()` — a property of the binary, not
of a project — the check needs no config and applies identically whether or
not `.ctxloom` exists yet.

## What she gave up

The last scenario asks the question a migrating team actually asks, which is
not "did it work" — they can see that — but "what did I just lose?"

On this exact engine pair the answer is concrete and verified: codex has no
`session_end` hook. A team moving from claude-code to codex loses a hook event
they may well have built on, and — until this was wired — no surface anywhere
mentioned it, before or after. `agent show <name>` now names it directly:
`NOT carried: hooks (1 session_end) — codex has no session-end event`, printed
alongside the resolved engine rather than in a separate pass, so scanning only
the top of the command's output cannot be mistaken for the whole story.

<!-- doc:outro -->
This was the same missing capability that appears in U3 wearing different
clothes. There, a new hire's opencode-using deskmate silently receives the team
bundle without the team's guardrails, and nothing tells either of them. Here, a
whole team silently lost a hook event. Both are the absence of one report:
what does this engine binding NOT carry?

That report closed two journeys' worth of findings at once, and needed no new
protocol work — the per-engine capability matrix was already known to the
product, since the code has to branch on it to function. It was known
internally and never said out loud until `backends.UncarriedSurfaces` (already
proven by `profile materialize`, whiny-exclusive) was wired to `agent show`
too, gaining a PER-EVENT declaration (`agentDescriptor.unsupportedHookKinds`)
alongside its existing whole-mechanism one, so a backend that has hooks in
general but lacks one specific native event — codex's case — is reportable and
not just a backend with none at all.

Five scenarios, all passing: the swap itself, the survival of history across
it, the capability-loss report above, and both engine-name validations — the
agent-edit binding and `manage install`'s scaffold-time flag.
<!-- /doc:outro -->
