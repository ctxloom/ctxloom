<!--
Trust-surface matrix narration companion.

Prose ONLY. It never restates what the Gherkin already says business-readably,
and it carries no assertions of its own — trust_surface.feature next to it is
the single source of truth for what this reference proves. What lives here is
the connective tissue a terse Given/When/Then cannot carry.

Unlike J2-J4's narration, this one has no persona and no arc to connect —
that absence is deliberate (see the feature file's own header). The connective
tissue here is instead: why the question is worth asking exhaustively, why it
took two different starting fixtures to ask it honestly, and what a reader
should walk away able to say with confidence.

Marker convention: an opening prose block, one block per scenario keyed to
that scenario's exact name (a Scenario Outline's marker uses the Outline's own
name once — it is not repeated per Examples row), and a closing block — the
same three HTML-comment marker pairs the generator splits j2_setup.doc.md on.
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
being delivered — signed by a publisher this project trusts, exactly J15's own
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
just the two J15 already covered. A trusted publisher's signature is real
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
no `trust.ItemKind` for it, and `ctxloom trust accept`/`trust reject` cannot even
parse a `#profiles/<name>` selector. That is not an oversight this page papers
over; it is a real property of the review model, stated plainly instead of
quietly assumed. What a reviewer actually approves or denies is always one of
the profile's INGREDIENTS — its fragments, its skills, its MCP servers, its
hooks — never the profile label wrapping them. This scenario drives the exact
CLI command a reviewer would try and shows the refusal, in the tool's own
words, rather than describing what would happen.
<!-- /doc:scenario -->

<!-- doc:scenario: A rejection binds bytes, not identity — it survives a rename or move -->
Every rejection scenario above proves the same thing at the same address: reject
this ref, and this ref goes dark. None of them answer the sharper question a
real publisher relationship eventually asks: what happens when the REJECTED
thing shows back up somewhere else? ctxloom's own design answers that a
rejection is of bytes, not of provenance — a countersigned content-reject
covers the payload wherever it appears, deliberately omitting the ref so a
renamed or moved copy cannot simply outrun it. That claim had never been
exercised. This scenario rejects a fragment, then has the publisher
legitimately republish the identical bytes under a new name — re-signed, so a
broken content check would let it straight back in through the trusted-signer
step exactly as if nothing had ever been rejected. The marker still never
reaches the assistant. The content check, not the ref check, is what is
actually doing the work here.
<!-- /doc:scenario -->

<!-- doc:scenario: Approving a fragment shipped with both a raw and a distilled form covers both, not just the one form checked first -->
`ctxloom review`'s countersignature covers the exact bytes a human looked at —
and a fragment can present TWO different sets of bytes, raw and distilled,
depending on project config. Nothing above ever gave a reviewed item a second
form to have an opinion about, so the rule that an approval is scoped to a
FORM, not just a ref, had a rule with nothing testing it. This fragment ships
both a raw and a distilled form from the start; approving it signs both,
and flipping which one the project prefers flips which bytes the assistant
actually receives — proving the approval travels with the content, not with
whichever form happened to get checked at review time.
<!-- /doc:scenario -->

<!-- doc:scenario: Approving a fragment while it has only a raw form does not silently cover a distilled form added later -->
The dangerous direction of the same rule: an approval must never silently
grow to cover bytes nobody ever reviewed. A fragment approved while it had
only a raw form is later given a distilled form by its publisher — the raw
bytes are untouched, only a new form is added. Project config prefers
distilled by default, so materializing now reaches for bytes that exist for
the first time and that nobody has ever countersigned. The fragment goes
completely dark rather than serving content on the strength of an approval
that was never about it — and its review state reports "pending," the honest
label for "somebody needs to look at this," not "rejected" or a leftover
"accepted" from before the new form existed.
<!-- /doc:scenario -->

<!-- doc:scenario: A rejected item's review state is labeled "rejected," not silently "pending" -->
Everything on this page up to now asks "did the bytes get through" — a real
reviewer also asks "what does the tool say happened," and those are different
claims. `ctxloom review`/`fragment list --format json`'s state field is what a
human actually reads to know an item was decided at all, and nothing checked
that a rejected item's label matches its behavior. This scenario rejects a
fragment and reads its state back through the JSON listing, confirming it says
"rejected" — not "pending," which would tell a reviewer a decision is still
outstanding when one has already been made.
<!-- /doc:scenario -->

<!-- doc:scenario: A retracted bundle's items are labeled "rejected," not silently "pending" -->
The other half of the same label claim, and it is a genuinely separate line of
code: a rejection and a retraction are two different decision-function steps
that happen to render through the identical three-state label, and either half
of that rendering could go missing without a single payload assertion on this
page noticing — withholding would still work, only the label would lie. This
scenario has the publisher retract the whole bundle instead of a human
rejecting one item, and checks the same state field lands on "rejected," never
"pending."
<!-- /doc:scenario -->

<!-- doc:scenario: A corrupted approvals store denies everything, rather than silently un-rejecting previously withheld content -->
An approvals store this process cannot read denies EVERYTHING — on the theory
that an unreadable store might be hiding a rejection, and rejection is
supposed to be supreme. This scenario proves that end to end: reject a
fragment, confirm it is withheld, corrupt the store, and Alice's next session
refuses to start, telling her plainly that the approvals store is the problem.
The previously-rejected fragment does not reappear, and neither does anything
else — this is deny-all, not "deny that one item".

**It used to be the opposite, which is why the scenario is worded as a
contrast.** The guard existed in the code but was never reached: every real
caller built its own record store and handed `EffectiveTrust` something
already non-nil, so the one preamble check that would fail closed never ran.
The store's read path then treated an I/O error exactly like "nothing was ever
recorded here", and a previously rejected item silently stopped being
rejected — no warning, exit code 0. The scenario is kept in that shape so a
regression reads as a reversal rather than as a missing assertion.
<!-- /doc:scenario -->

<!-- doc:scenario: An approved item's review state is labeled "accepted," not left at "pending" -->
The mirror of the two label scenarios above, on the allow side: approving an
item has to move its state to "accepted," not merely let its bytes flow while
the tool still reports it as awaiting review. A user who has decided about
something needs the tool to say a decision was made — a payload that reaches
the assistant while the listing still reads "pending" is the same lie as a
withheld item that reads "pending," pointed the other way.
<!-- /doc:scenario -->

<!-- doc:scenario: Rejecting a raw-only item records a content block for exactly that form -->
Everything above reads the served payload. This pair reads instead what the
tool actually WROTE when it rejected something — the decision, not its
downstream effect. A rejection blocks the item's content per form the item
currently has, so a raw-only fragment should be blocked in exactly its raw
form, no more. A phantom block recorded for a form the item does not have is
not harmless: it is the tool reporting it protected bytes it never saw, and no
scenario that only checks the materialized surface would ever notice the
difference.
<!-- /doc:scenario -->

<!-- doc:scenario: Rejecting an item that ships both forms records a content block for both -->
The other half of the recorded-decision check, and the one that catches the
opposite failure: an item that genuinely ships both a raw and a distilled form
must have BOTH blocked, not just whichever one the write path happened to reach
first. Read together with the raw-only case, this pins the recorded rejection
to exactly the forms the item has — no missing block that would let a form slip
through, no phantom block that would misreport coverage.
<!-- /doc:scenario -->

<!-- doc:scenario: A source reference that cannot be parsed is refused, never treated as local -->
Locality is not a cosmetic label on this page — it is a decision. Local content
is auto-allowed at the top of the cascade, ahead of any review, so the question
"is this ref local?" is load-bearing. A reference that carries a scheme marker,
and was therefore plainly meant as a remote/canonical ref, but does not parse
as one, must be REFUSED outright — never quietly downgraded to "a bare local
bundle name," which would walk it straight past the gate. This scenario drives
exactly such a malformed ref and confirms the tool fails closed, in its own
words, rather than guessing local.
<!-- /doc:scenario -->

<!-- doc:scenario: An unreadable lockfile withholds remote content, rather than silently un-retracting it -->
Retraction is the only control on this page that is not the reader's own
decision. Everything else here — approve, reject — is something a human did
about bytes they looked at. A retraction is the PUBLISHER saying "withdraw
this," and the reason they usually say it is that the content turned out to be
harmful. That record lives in one place on disk, `.ctxloom/lock.yaml`, learned
at the last sync that had the network in hand.

Which raises a question the rest of this page never asks: what happens when
that file cannot be read? "I cannot read the retraction record" and "nothing
is retracted" are different statements, and for a long time the tool collapsed
them into the second one — so a corrupt lockfile quietly served the withdrawn
bundle again, exit 0, no warning. A control that switches itself off when its
own state goes missing is not a control.

This scenario proves the posture is now the other one. It delivers the
publisher's fragment normally, corrupts the lockfile, and asks for another
session: the content is withheld and the session refuses to start. The refusal
is deliberately talkative, because this is a fault nobody can diagnose from
what they typed — nothing about `ctxloom run` mentions a lockfile — so it
names the file, says what it withheld and why, and names the recovery. It can
afford to say "the file is left intact" because a companion guard makes that
true: nothing overwrites an unparseable lockfile, so the holds and retractions
inside it are still there to read by hand.

The boundary this must NOT trip is the ordinary one: a project with no
lockfile at all has no pins and legitimately nothing retracted, and it keeps
working untouched. Absent is not corrupt. That case is pinned in the unit
suite rather than here, because it is the ABSENCE of behaviour and there is no
scenario to watch.
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
page deliberately leaves to J15 rather than re-proving — tamper-after-signing
and untrusted-publisher content — see
[Skills my company has validated](/journeys/j15-corporate-signed/).
<!-- /doc:outro -->
