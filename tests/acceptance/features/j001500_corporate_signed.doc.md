<!--
J001500 narration companion (PROTOTYPE — see docs/living-docs-plan.md).

Prose ONLY. It never restates what the Gherkin already says business-readably,
and it carries no assertions of its own — j001500_corporate_signed.feature next to it
is the single source of truth for what J001500 promises. What lives here is the
connective tissue a terse Given/When/Then cannot carry.

Marker convention: an opening prose block, one block per scenario keyed to that
scenario's exact name, and a closing block — the same three HTML-comment marker
pairs the generator splits j000200_setup.doc.md on. A scenario with no matching block
still renders (Gherkin + captured evidence); narration is additive.

NOTE: this feature has a Background (Trent's company publishes a signed
"secure-coding" bundle; Alice trusts the company key). godog runs those
Background steps before every scenario, so they appear as the first rows of each
scenario's captured step→output grid — the shared setup is visible per scenario
without being restated here.
-->

<!-- doc:intro -->
The gap between "we wrote a standard" and "the standard is enforced" is
provenance. A company can publish a `secure-coding` bundle, but that is worthless
if anyone who can push to a repo can silently rewrite it — changing how every
engineer's assistant behaves, or slipping in an executable that runs on their
machines. What a company actually needs is a guarantee: what reached my assistant
came from who I think it did, unchanged, and only what I allowed.

Note what this is **not**. J001500 proves *provenance and integrity*, not *secrecy*.
ctxloom does not encrypt your context and there is no eavesdropper in this story
— no Eve trying to read the guidance. The adversary is **Mallory**, and she does
not want to read anything; she wants to *change what reaches the assistant*. Every
guarantee below is aimed at her: at making sure that a signature is checked over
the exact bytes about to be exposed, that tampering is caught loudly rather than
degraded quietly, that executables face the same gate as prose, and that a
publisher — or a whole compromised key — can be pulled back after the fact.

The trust primitive underneath is a signature over bytes, from a key you chose to
trust. Contrast this with [J000700](/journeys/j000700-team-authoring/), where content is
trusted for being *first-party* — authored in the team's own project. Here the
content is remote: it earns exposure only because the company's key signed it and
Alice trusts that key for publishing. The full model — the resolver, the
namespaces, the storage — is in [Trust states and the gate](/security/trust-states/),
[Review and trust](/concepts/review-and-trust/), and [Key management](/security/key-management/).
<!-- /doc:intro -->

<!-- doc:scenario: Alice references a bundle from the company repo and its guidance reaches her assistant -->
Before any of the adversarial cases, the happy path: the reference mechanic
itself. Alice does not fork or copy the company's bundle — she *references* it
from her own project, pulling one specific bundle out of another repository's
history. Its guidance flows to her assistant for exactly one reason, stated in
the Gherkin's `Then`: the company key signed it and Alice trusts that key. This
is the baseline every later scenario perturbs — tamper with the bytes, ship an
executable, retract the version, revoke the key — and watches the guarantee hold.
<!-- /doc:scenario -->

<!-- doc:scenario: Content Mallory altered after it was signed is refused, loudly -->
This is the case that separates a real signature check from a decorative one. A
trusted key genuinely signed the *original* bundle — but Mallory changed the
bytes afterward. A naive system that trusted the repository, or trusted a
remembered "this bundle is fine" verdict, would ship her edit. ctxloom re-derives
the exact bytes it is about to expose and checks the signature over *those*, so
the altered content fails verification.

Crucially, this is not J000200's benign "held for your review." A missing signature is
quiet — content simply waits. A signature that is *present but does not verify* is
tampering, and it is refused **loudly**: Alice is warned that the content's
signature does not verify, because a broken signature on content that claims to be
signed is a security event, not a to-do item.
<!-- /doc:scenario -->

<!-- doc:scenario: A trusted company's MCP server and hook reach the assistant's configuration -->
Trust is not only about prose. A bundle can ship **executables** — MCP servers
the assistant can call, and hooks that run on events in the harness — and these
are the highest-stakes thing a bundle carries, because they run code. This
scenario proves the delivery side: a trusted publisher's MCP server and hook
reach the engine's *generated configuration*, not just its context. They pass
through a dedicated executable trust gate that makes the same decision, from the
same trusted-key signature, as the content gate does for prose — so trusting the
company to write your guidance and trusting it to wire an MCP server are one
decision, made deliberately, not two with different rigor.
<!-- /doc:scenario -->

<!-- doc:scenario: A rejected executable is withheld even from a trusted company -->
The counterweight to the previous scenario: a trusted signature is permission,
never obligation. Alice can reject a single executable — here, the hook — and her
rejection outranks the company's trusted signature on it. When she starts a
session, the MCP server (which she did not reject) still appears, but the hook is
absent because she rejected it. Rejection beating a trusted publisher, on the
very item with the most blast radius, is the structural expression of "signed
does not mean safe": a signature authenticates who wrote something; it never
overrides a human's refusal to run it.
<!-- /doc:scenario -->

<!-- doc:scenario: Trent retracts a bundle and it stops reaching engineers on the next sync -->
Publishers make mistakes, and the question is whether they can take one back. A
signature, once made, is valid forever — so revocation cannot mean "un-sign." It
means the publisher records that a specific version is withdrawn, and engineers
stop receiving it on their next sync, with a notice.

This is a **working guarantee**, and it is worth being precise about why, because
it was not always. Retraction is evaluated at exposure time against a *local*
record — ctxloom never dials the network to decide whether to show you content —
and that local record is now written both on a fresh pull and, the part that had
been missing, when re-syncing refs that were *already installed*. Previously
retraction had no effect on content that had already been distributed through any
CLI path: the very case that matters most. That product gap was fixed, and this
scenario is the proof that Trent's retraction now actually reaches an engineer who
already had the bundle — Alice is told the content was retracted, and her
assistant no longer receives it.
<!-- /doc:scenario -->

<!-- doc:scenario: When the company key is compromised, revoking it stops all of its content at once -->
Retracting one version is the routine case; a stolen key is the emergency. If the
company's signing key is compromised, retracting bundles one at a time is far too
slow — the attacker can sign anything. Revoking trust in the *key* is the blunt,
correct instrument: it invalidates everything that key ever signed, in one move.

What happens to that content is the elegant part. It does not error or vanish
into a special "revoked" limbo; it simply falls back to the path any unsigned,
untrusted content takes — held for Alice's review, *as if it had never been
signed*. The signature stops counting the instant the key is no longer trusted,
and the content lands exactly where content from a stranger would. One withdrawal
of trust, and every bundle that key signed is back behind the gate at once.
<!-- /doc:scenario -->

<!-- doc:scenario: Recording a team-wide review decision requires a signing key -->
The last scenario guards the trust system against forging *its own records*. A
decision Alice records only for herself can fall back to an unsigned marker — it
is her call, on her machine. But a decision written into the **committable,
team-inherited store** — the one a teammate or CI will inherit as "the team
approved this" — must be signed, because an unsigned team decision is a decision
anyone who can write a file could forge. So with no signing key available, ctxloom
does not degrade to an unsigned team record; it **refuses**, and nothing is
written. A team-wide "we approved this" that cannot be attributed to a real key is
not a weaker approval — it is not an approval at all.
<!-- /doc:scenario -->

<!-- doc:outro -->
Taken together, J001500 is the enforcement half of the trust model: not "we have a
standard" but "the standard came from who we trust, unchanged, only what we
allowed — and we can pull it back." It builds on the first-party trust of
[J000700](/journeys/j000700-team-authoring/) and the add-and-review flow of
[J000200](/journeys/j000200-setup/), and it is the concrete exercise of the model laid out
in [Trust states and the gate](/security/trust-states/), the
[threat model](/security/threat-model/) (including what ctxloom explicitly does
*not* defend against), and [Key management](/security/key-management/).
<!-- /doc:outro -->
