<!--
J7 narration companion (PROTOTYPE — see docs/living-docs-plan.md).

Prose ONLY. It never restates what the Gherkin already says business-readably,
and it carries no assertions of its own — j7_team_authoring.feature next to it
is the single source of truth for what J7 promises. What lives here is the
connective tissue a terse Given/When/Then cannot carry.

Marker convention: an opening prose block, one block per scenario keyed to
that scenario's exact name, and a closing block — the same three HTML-comment
marker pairs the generator splits j2_setup.doc.md on. A scenario with no
matching block still renders (Gherkin + captured evidence); narration is
additive, never required.
-->

<!-- doc:intro -->
A team's standards are only real if every teammate's assistant actually follows
them. Writing the convention down in a wiki does nothing for the assistant that
never reads the wiki; pasting it into one engineer's prompt does nothing for the
other five. The standard that lives in one place and reaches everyone by hand is
the standard that quietly drifts, one copy at a time, until no two assistants
agree on how the team works.

ctxloom's answer is to make the standard part of the project itself. Carol, the
team lead, authors a skill or a fragment *in the repository* — the same repo
everyone already clones and pulls. When Bob pulls, his assistant gains it. No
one exports a prompt, no one re-pastes anything, and — this is the part that
separates J7 from every remote-source journey — no one reviews it. The project
is the team's own; content authored inside it is **first-party** and trusted on
that basis alone. There is nothing to sign and nothing to approve, because the
team already owns what it wrote (see [Review and trust](/concepts/review-and-trust/)
for the first-party exemption, and [A prompt is executable code](/security/prompts-are-code/)
for why *remote* content does not get that pass).

This journey proves the authoring loop end to end: a skill authored in-project
reaches a teammate untouched by review; verbose guidance distilled once is
delivered in its compact form; and an edit propagates so the new version
arrives and the stale one is gone — the case the whole suite exists to catch,
because "the change silently didn't arrive" is the failure that erodes trust in
the whole mechanism.
<!-- /doc:intro -->

<!-- doc:scenario: Carol authors a skill and a teammate gains it -->
This is the core loop, stripped to its essentials. Carol writes a
`conventional-commits` skill into the team's project and commits it; Bob pulls;
Bob's assistant can invoke it. The load-bearing clause is the last one — *it
reached him without any review*. Compare this with J2, where a remote source's
content is held at the gate until a human approves it. Here there is no gate to
clear, because the content is local to a project the team owns: the trust
resolver's **local** rule allows first-party content outright, ahead of any
signing or approval check. The team's own repository is not a stranger handing
you a prompt; it is the team, and you already trust the team.
<!-- /doc:scenario -->

<!-- doc:scenario: Carol distills verbose guidance and teammates receive the compact form -->
Standards want to be thorough; context windows want them short. Distillation
resolves that tension at authoring time rather than at read time: Carol writes
the full, explanatory version once, distills it into a compact form, and commits
**both**. A teammate running with distilled context enabled receives the compact
guidance; the verbose original stays in the repo as the human-readable source of
truth but never spends the teammate's tokens.

The captured evidence here exercises the real distill machinery — the bundle is
parsed, the distiller is invoked, the compact form is saved and content-hashed —
not a canned string. (Distillation is not fragment-only: the same
compact-form-served behavior holds for skills; a fragment is used here as the
common case.) For the mechanism itself, see [Distillation](/guides/distillation/).
<!-- /doc:scenario -->

<!-- doc:scenario: Carol changes a skill and the change reaches teammates, not the old version -->
Authoring once is the easy half; keeping everyone current is the half that
actually earns trust. A propagation mechanism that delivers the *new* content
but leaves the *old* content lingering somewhere is worse than none — the team
would be split between two versions of a standard and not know it. So this
scenario asserts both directions at once: after Carol edits the skill and Bob
pulls again, Bob's assistant has the updated version **and no longer has the
previous one**. The stale copy is not merely superseded in some index; it is
gone from what the assistant sees. This is the "does the new content actually
arrive, cleanly" case the suite is built around.
<!-- /doc:scenario -->

<!-- doc:scenario: Carol's own active assistant picks up the change she just made -->
This scenario is deliberately **not** part of the green run — it is marked
`@future`, and you will see it below without captured evidence. Whether Carol's
*own already-running* assistant reflects an edit she just made, without any
restart, is engine-dependent: some engines live-reload their context, others fix
it at launch and need a fresh session (the same two-phase shape J2's restart
scenario makes explicit). Greening this honestly means pinning that behavior per
engine first, so it is tracked rather than faked. The Gherkin stays here as the
recorded intent; the empty proof is the honest state of it today.
<!-- /doc:scenario -->

<!-- doc:outro -->
J7 is the first-party half of the trust model: content the team authors in its
own project, trusted because the team owns it, propagating to every teammate
without ceremony. The moment content comes from *outside* that boundary — a
personal repo, a company repo, a stranger's bundle — the ceremony returns, and
that is the subject of the other journeys: [Setting up ctxloom on a
project](/journeys/j2-setup/) for adding and reviewing remote sources, and
[Skills my company has validated](/journeys/j15-corporate-signed/) for the signed,
company-published case.
<!-- /doc:outro -->
