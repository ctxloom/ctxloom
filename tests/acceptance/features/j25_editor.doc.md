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

## Three scenarios

`acp list` emits a block that genuinely parses as JSON and genuinely names the
command an editor will run. That is the green one, and the assertion is
deliberately about parseability rather than substrings, because a prose listing
describing the agent would contain all the same words and none of the same
usefulness — a difference that would surface only when Dana pastes it and her
editor's config stops loading.

Then two findings that were the same failure at two scales, both now fixed.

**A flag accepted and discarded.** The bare `ctxloom acp` parent registered
`--agent`, `--llm`, `--profile` and `--workspace`, and did nothing with them.
Dana bound an agent, saw exit 0, and got a session carrying none of that
agent's context.

A flag that is REJECTED teaches the user their command was wrong. A flag that
is ACCEPTED and ignored teaches them it was right. She would have concluded
that ctxloom does not deliver context to editors, and she would have been
reasoning correctly from everything she was able to observe. That is the worst
property a bug can have: it does not merely fail, it teaches a false model, and
the false model is unfalsifiable from the outside. Filed as task `broken-sage`,
fixed by removing the bare command's flag registration — cobra's own
unknown-flag refusal now catches it before anything is silently discarded.

**A door that looked equivalent and was not.** A generic ACP agent onboarded as
configuration inherits no materialized native surface — P1 is ABSENT BY DESIGN
for generic acp — no hooks, and no history. Which is to say none of the three
things that distinguish ctxloom from pointing the editor straight at the vendor.

That is the correct engineering outcome; the protocol carries what it carries,
and no amount of wanting changes that. What was not correct is that nobody was
told. `acp list` now says so directly — a standing note naming the generic
`acp` engine's structural loss (no hooks, no history), printed whether or not a
binding uses it yet, since the whole point is telling Dana BEFORE she
configures one (trusting-ambiguity).

<!-- doc:outro -->
Both fixes closed the same shape of gap found in two other journeys. U3's new
hire silently received the team bundle without the team's guardrails because
opencode has no hook mechanism. U12's team silently lost a hook event on
engine switch day. Dana was silently getting a context-free session through a
door that advertised equivalence.

Three journeys, three personas, one missing report: **what does this binding
not carry?** The product already knew the answer in every case — it has to,
because the code branches on exactly that matrix to function. It knew, and
never said — until `agent show` (U12) and `acp list` (here) were wired to say
it.

For Dana specifically the stakes were higher than for the others, because she
is the persona most likely to conclude the product does not work and stop. The
terminal users hit these gaps mid-workflow, with enough context to file a bug.
She hit them on day one, with none.

Three scenarios. All pass now.
<!-- /doc:outro -->
