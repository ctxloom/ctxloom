<!--
Trust-surface matrix narration companion.

Prose ONLY. It never restates what the Gherkin already says business-readably,
and it carries no assertions of its own — trust_surface.feature next to it is
the single source of truth for what this reference proves. What lives here is
the connective tissue a terse Given/When/Then cannot carry.

Unlike J1-J5's narration, this one has no persona and no arc to connect —
that absence is deliberate (see the feature file's own header). The connective
tissue here is instead: why the question is worth asking exhaustively, why it
took two different starting fixtures to ask it honestly, and what a reader
should walk away able to say with confidence.

Marker convention: an opening prose block, one block per scenario keyed to
that scenario's exact name (a Scenario Outline's marker uses the Outline's own
name once — it is not repeated per Examples row), and a closing block — the
same three HTML-comment marker pairs the generator splits j1_setup.doc.md on.
-->

<!-- doc:intro -->
Every other journey in this suite asks "does the right thing happen for this
one persona, in this one story." This page asks a flatter, colder question,
on purpose: across everything a bundle can ship, what does "review" actually
decide, and where does it decide nothing at all? A security reviewer should be
able to read this one table and know precisely which of their approvals and
rejections are load-bearing.

The five things a bundle can ship are not interchangeable, and the table below
is ordered by how much damage each one can do if the gate is wrong. A **hook**
is a shell command the harness runs automatically when a tool call matches —
there is no model in the loop at all, so a hook that slips through is simply
arbitrary code execution. An **MCP server** is a binary the harness launches
and then hands the agent a tool surface to call into — one step removed from
a hook, but still a process running with the agent's privileges. A
**fragment** or **skill** is "only" prose, but it is prose injected straight
into an agent that already holds a shell, a filesystem, network access, and
whatever credentials the session has — treating that as low-stakes because it
is "just text" is the mistake, not a fact about the world.

Two Outlines, not one, is the load-bearing design choice on this page, and it
is worth being explicit about why: a denial test is only as good as its
ability to fail. Reject an item that was already going to be withheld anyway
(nothing signed it, nobody reviewed it) and the test passes whether or not
rejection does anything at all — the pending default was doing all the work.
So the REJECT outline starts every item from a state where it is already
being delivered — signed by a publisher this project trusts, exactly J3's own
Background — and rejection has to visibly turn that off. The APPROVE outline
runs the opposite way, starting from an unsigned bundle nobody has looked at,
so approving one item is the only thing that could possibly expose it.

Every row on both tables was broken on purpose to prove this: the mechanism
each row depends on was disabled, the row was watched to fail for exactly the
stated reason, and only then restored. That is not a formality — it is the
difference between a check that protects something and a check that has only
ever been seen passing.
<!-- /doc:intro -->

<!-- doc:scenario: Approving the shipped item is what makes it reach the assistant -->
Four rows, one mechanism: `ctxloom trust <ref>` is the single command that
turns a pending fragment, skill, MCP server, or hook into something the
assistant actually receives. Nothing here is item-specific — the same
countersigned acceptance record, read by the same `EffectiveTrust` decision
function, is what exposes a paragraph of guidance and what exposes a binary
the agent can invoke. Approving the wrong one is exactly as easy as approving
the right one, which is precisely why every kind needed its own proof rather
than a single example standing in for the rest.
<!-- /doc:scenario -->

<!-- doc:scenario: Rejecting the shipped item withholds it, even though a trusted publisher signed it -->
This is the sharpest row on the page, repeated for all four kinds instead of
just the two J3 already covered. A trusted publisher's signature is real
permission — the content/executable gate allows it by default — and a
rejection still beats it, every time, for a fragment and a skill exactly as
much as for an MCP server and a hook. That symmetry is the point: before this
page existed, "rejection beats a trusted signer" had only ever been checked
for the two executable kinds. The highest-consequence claim on the whole page
— that you can always say no, even to something signed by a key you chose to
trust — now has proof standing behind all four kinds it needs to cover, not
just two of them.
<!-- /doc:scenario -->

<!-- doc:scenario: A profile cannot be approved or denied — there is no gate to run it through -->
The honest gap, proven rather than asserted. A bundle can ship a profile —
a named composition of the bundle's own fragments, skills, MCP servers, and
hooks — but the profile itself carries no trust identity of its own: there is
no `trust.ItemKind` for it, and `ctxloom trust`/`ctxloom blacklist` cannot even
parse a `#profiles/<name>` selector. That is not an oversight this page papers
over; it is a real property of the review model, stated plainly instead of
quietly assumed. What a reviewer actually approves or denies is always one of
the profile's INGREDIENTS — its fragments, its skills, its MCP servers, its
hooks — never the profile label wrapping them. This scenario drives the exact
CLI command a reviewer would try and shows the refusal, in the tool's own
words, rather than describing what would happen.
<!-- /doc:scenario -->

<!-- doc:outro -->
Read as a single artifact, this page closes the gap the rest of the suite left
open: before it, "can this be rejected" had real proof for exactly two of five
kinds, on one engine, and "can an MCP server or a skill be rejected at all"
had none. Now every trust-addressable kind has been rejected on purpose, from
a state where rejection was the only thing standing between a trusted
publisher's content and the assistant, and approved on purpose, from a state
where approval was the only thing that could expose it. The one kind that
cannot be addressed this way — profiles — is named as exactly that, not
folded silently into the same table.

This is deliberately a one-engine proof: the executable trust gate sits
upstream of every engine writer (see the feature file's own citations), so a
second or third engine here would re-run the identical mechanism, not test a
different one. For the deeper mechanics — the decision function, the
acceptance/rejection stores, what a signature does and does not buy — see
[Trust states and the gate](/security/trust-states/) and
[Review and trust](/concepts/review-and-trust/). For the two scenarios this
page deliberately leaves to J3 rather than re-proving — tamper-after-signing
and untrusted-publisher content — see
[Skills my company has validated](/journeys/j3-corporate-signed/).
<!-- /doc:outro -->
