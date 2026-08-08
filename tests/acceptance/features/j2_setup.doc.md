<!--
J2 narration companion (PROTOTYPE — see docs/living-docs-plan.md).

This file is prose ONLY. It never restates what the Gherkin already says
business-readably, and it carries no assertions of its own — the .feature
file next to it is still the single source of truth for what J2 promises.
What lives here is the connective tissue a terse Given/When/Then cannot
carry: why the steps are ordered the way they are, what a reader would
otherwise have to reverse-engineer from the code.

The generator (scripts/living-docs-prototype/gen_doc_page.py) splits this
file on three kinds of HTML-comment marker pairs: one wrapping the opening
prose, one per scenario (carrying that scenario's exact name so it can be
matched against the .feature file), and one wrapping the closing prose. See
that script's own module docstring for the literal marker syntax — it is
deliberately not reproduced here verbatim, so this explanatory comment can
never be mistaken by the parser for a real marker.

A scenario with no matching marker block still renders (Gherkin + captured
evidence, if any) — narration is additive, never required.
-->

<!-- doc:intro -->
Every engineer's coding assistant is a blank slate until something fills it
in. Left alone, that happens by accident: whatever the assistant picked up
from this repo's files, whatever the developer happened to paste in, whatever
the model already assumed. Two engineers on the same team end up with two
different assistants, and neither knows the standards the other is quietly
working from.

ctxloom's first job is to close that gap on purpose. A project names the
sources it wants — a developer's own repository of fragments and skills, the
company's shared one — and `ctxloom init` wires them into the project's
configuration. But naming a source is not the same as trusting it: a bundle
can carry a shell command that runs inside the developer's own harness (see
[A prompt is executable code](/security/prompts-are-code/)), so nothing
reaches the assistant on the strength of an address alone. It reaches the
assistant because a key the developer trusts signed it — the developer's own
key for their own work, the company's key for the company's.

This journey proves that chain end to end, in five moves: setup wires the
sources in; a restart — not the setup session itself — is what actually hands
their content to a running assistant; unsigned or untrusted content is held
back rather than delivered; and a human reviews what was held, item by item,
before anything crosses that line.
<!-- /doc:intro -->

<!-- doc:scenario: After setup, trusted sources are part of the configuration -->
This scenario is deliberately narrow: it proves the **posture**, not
**delivery**. After Alice runs setup and adds both repositories as sources,
`ctxloom config show` and a materialized profile already reflect that
her personal repository is signed with her own key and her company's is
signed with a key she trusts. That is enough for both to be exposed — the
[three-state gate](/security/trust-states/) allows content the moment a
trusted signature covers its exact bytes, with no separate "turn it on" step.

What this scenario does *not* claim is that a live, running assistant has
already seen this content — a session's context is fixed at the moment it
launches. That is the next scenario's job.
<!-- /doc:scenario -->

<!-- doc:scenario: Setup configures the agents, then a restart delivers their context -->
The discovery session that walks Alice through setup is, itself, a running
assistant — and it cannot see what it is in the middle of installing. It
composes her agents' profiles from both sources' fragments as configuration
*to be written*, but a session's assembled context is fixed at launch; you
cannot hand a running assistant a fragment file it will only start
respecting after this conversation ends. So setup's last act (`ctxloom
init`'s `offerSessionRelaunch`) is to offer a **restart**: exit this session,
launch a fresh one against the configuration that was just written.

That two-phase shape — *configure, then restart to deliver* — is the whole
point of this scenario. The captured evidence below is the mock engine's own
record of what it received on that fresh launch: both the personal and
company markers, present because the restarted process resolved the same
composed profile from scratch, not because anything was injected after the
fact.
<!-- /doc:scenario -->

<!-- doc:scenario: Content ctxloom cannot verify is held, not delivered -->
Unsigned content and content signed by a key Alice hasn't chosen to trust are
not two different problems — to the gate, they are the identical case. Both
resolve to an empty verified signer, and an empty verified signer is
withheld. There is no fourth, in-between state for "signed, but by someone I
don't yet trust": either a trusted key's signature verifies over these exact
bytes, or the content is pending, full stop.

Nothing about this fails loudly. Alice's assistant simply never receives the
held marker; the only signal is one aggregate, content-free line telling her
something is waiting on her review.
<!-- /doc:scenario -->

<!-- doc:scenario: Alice reviews held content and decides item by item -->
Held is not stuck — it is a queue with a name and a command. `ctxloom review
--list` shows Alice exactly what is pending and which remote each item came
from, so "something is awaiting review" becomes a specific, inspectable
list. From there the decision is per item, not per source: she can approve
the first and reject the second even though both arrived the same way,
because trust in this model is never "I trust this repository" — it is "I
approve *this exact content*."

Approving and rejecting are not mirror images of the same action. An
approval is a countersignature over content Alice explicitly reviewed — if
that content changes even one byte, the approval no longer covers it and the
item falls back to pending. A rejection is stickier by design: it is recorded
against the ref (so it survives the content changing under it) *and* against
the content with the ref stripped out (so the same bytes stay rejected even
if they resurface renamed, or from a different remote entirely).
<!-- /doc:scenario -->

<!-- doc:outro -->
J2 stops at the trust boundary: what reaches a real assistant, and why. What
a company's or a developer's source can additionally *contribute* to the
setup conversation itself — not just fragments the assistant reads later, but
onboarding steps the interview asks about right now — is a companion journey,
"Sources and companions shape how a project is set up"
(`j3_source_augmentation.feature`; not yet published as its own page).

For the full trust model this journey exercises only a slice of, see
[Trust states and the gate](/security/trust-states/) and
[Review and trust](/concepts/review-and-trust/).
<!-- /doc:outro -->
