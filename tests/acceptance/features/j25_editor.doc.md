<!--
J25 narration companion (j25_editor.feature) — FLOWS-UNIFIED.md's U10.

The shortest companion in the set, and deliberately so: most of this journey
cannot be verified without a live editor, and the honest response to that is a
short file that says which rows are unverifiable, not a long one that implies
otherwise. Every claim traces to a named scenario or is marked as unverified.
-->

<!-- doc:intro -->
Dana lives in Zed and is not going to adopt a terminal workflow because a
context tool would prefer it.

That is not stubbornness, it is most of the market. If ctxloom only works for
people who run their assistant in a terminal, then "one signed context tree
driving all of them" quietly means "all of the ones I drive the way I like" —
and the differentiator shrinks to the size of its author's habits.

The editor door is `ctxloom acp` serving over stdio: the editor spawns it, they
speak the Agent Client Protocol, and the promise is that it is the SAME door as
a terminal run. Same binding, same trust gates, same capture. `acp list` prints
the block Dana pastes into her editor's config to make that happen.
<!-- /doc:intro -->

## What this journey can and cannot see

Most of Dana's arc needs a live editor talking to a long-running stdio server.
That is not hermetically assertable here, and scenarios that passed without it
would prove nothing about whether Zed can actually drive ctxloom.

So three rows are recorded as unverifiable rather than faked: door equivalence
under a real editor, `acp client` driving a real outbound turn, and the
antigravity row, which is prose-degraded by construction and has nothing to
assert but the loss itself. The live verify stays open and manual. The whole
surface is EXPERIMENTAL and this file does not pretend otherwise.

What IS assertable is the configuration edge — everything Dana touches before
an editor is involved. Which turns out to be where the problems are.

## Three scenarios, and the one that passes

`acp list` emits a block that genuinely parses as JSON and genuinely names the
command an editor will run. That is the green one, and the assertion is
deliberately about parseability rather than substrings, because a prose listing
describing the agent would contain all the same words and none of the same
usefulness — a difference that would surface only when Dana pastes it and her
editor's config stops loading.

Then the two reds, which are the same failure at two scales.

**A flag accepted and discarded.** The bare `ctxloom acp` parent registers
`--agent`, `--llm`, `--profile` and `--workspace`, and does nothing with them.
Dana binds an agent, sees exit 0, and gets a session carrying none of that
agent's context.

A flag that is REJECTED teaches the user their command was wrong. A flag that
is ACCEPTED and ignored teaches them it was right. She will conclude that
ctxloom does not deliver context to editors, and she will be reasoning
correctly from everything she is able to observe. That is the worst property a
bug can have: it does not merely fail, it teaches a false model, and the false
model is unfalsifiable from the outside. Filed as task `broken-sage`.

**A door that looks equivalent and is not.** A generic ACP agent onboarded as
configuration inherits no materialized native surface — P1 is ABSENT BY DESIGN
for generic acp — no hooks, and no history. Which is to say none of the three
things that distinguish ctxloom from pointing the editor straight at the vendor.

That may well be the correct engineering outcome; the protocol carries what it
carries, and no amount of wanting changes that. What is not correct is that
nobody is told. Everything reports success. Dana gets a door that looks like the
terminal one, delivers a fraction of it, and says nothing about the difference.

<!-- doc:outro -->
Both reds are the same shape as findings in two other journeys, which is what
makes them worth fixing rather than filing. U3's new hire silently receives the
team bundle without the team's guardrails because opencode has no hook
mechanism. U12's team silently loses a hook event on engine switch day. Dana
silently gets a context-free session through a door that advertises
equivalence.

Three journeys, three personas, one missing report: **what does this binding
not carry?** The product already knows the answer in every case — it has to,
because the code branches on exactly that matrix to function. It knows, and
never says.

For Dana specifically the stakes are higher than for the others, because she is
the persona most likely to conclude the product does not work and stop. The
terminal users hit these gaps mid-workflow, with enough context to file a bug.
She hits them on day one, with none.

Three scenarios, all `@wip` with untag conditions in the feature file. One
passes.
<!-- /doc:outro -->
