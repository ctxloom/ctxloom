<!--
J001500 narration companion (rendered by scripts/gendocs/livingdocs).

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
the exact bytes a pull installs, and that tampering is caught loudly rather than
degraded quietly.
<!-- /doc:intro -->

<!-- doc:scenario: Alice references a bundle from the company repo and its guidance reaches her assistant -->
Before any of the adversarial cases, the happy path: the reference mechanic
itself. Alice does not fork or copy the company's bundle — she *references* it
from her own project, pulling one specific bundle out of another repository's
history, and its guidance flows to her assistant.
<!-- /doc:scenario -->

<!-- doc:scenario: Content Mallory altered after it was signed is refused, loudly -->
This is the case that separates a real signature check from a decorative one. A
trusted key genuinely signed the *original* bundle — but Mallory changed the
bytes afterward. A naive system that trusted the repository, or trusted a
remembered "this bundle is fine" verdict, would ship her edit. ctxloom re-derives
the exact bytes it is about to expose and checks the signature over *those*, so
the altered content fails verification.

A signature that is *present but does not verify* is tampering, and it is
refused **loudly**: Alice is warned that the content's signature does not
verify, because a broken signature on content that claims to be signed is a
security event, not a to-do item.
<!-- /doc:scenario -->

<!-- doc:scenario: A trusted company's MCP server and hook reach the assistant's configuration -->
Trust is not only about prose. A bundle can ship **executables** — MCP servers
the assistant can call, and hooks that run on events in the harness — and these
are the highest-stakes thing a bundle carries, because they run code. This
scenario proves the delivery side: the company's MCP server and hook reach the
engine's *generated configuration*, not just its context.
<!-- /doc:scenario -->

<!-- doc:outro -->
Taken together, J001500 is the integrity half of the model: the content came
from the repository Alice added, unchanged. See the
[threat model](/security/threat-model/) for what ctxloom explicitly does *not*
defend against.
<!-- /doc:outro -->
