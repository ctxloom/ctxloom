<!--
J8 narration companion.

Prose ONLY. It never restates what the Gherkin already says business-readably,
and it carries no assertions of its own — j8_onboarding.feature next to it is
the single source of truth for what J8 promises. What lives here is the
connective tissue a terse Given/When/Then cannot carry.

Marker convention: an opening prose block, one block per scenario keyed to
that scenario's exact name (a Scenario Outline's marker uses the Outline's own
name once — it is not repeated per Examples row), and a closing block — the
same three HTML-comment marker pairs the generator splits j2_setup.doc.md on.
-->

<!-- doc:intro -->
A new engineer's first day is where "we all use the same standards" is either
true or a story the team tells itself. Without something that travels with the
project, onboarding means a checklist: install these tools, paste this prompt,
configure that setting — and the checklist drifts the moment someone skips a
step, or nobody wrote one down in the first place. Two new hires end up with
two differently-configured assistants, and neither looks wrong from the
inside — nobody notices until their assistant behaves differently from
everyone else's on something that matters.

J8's claim is that cloning alone should be the *whole* onboarding: `git clone`,
open an assistant, and the team's standard is already there. Nothing to
install, nothing to paste, nothing to remember to configure. That claim only
holds if several much easier things to get wrong hold underneath it. Bob has
to receive the SAME context as the rest of the team — the versions they
pinned, not whatever an upstream happens to be serving the morning he joins —
or "we all use the same standard" is a hope, not a fact. His own machine is
not a clone of anyone else's, so whatever guidance depends on a companion tool
he hasn't installed yet has to degrade cleanly rather than break his very
first session. And a fresh machine must never become a quiet way around the
trust gate: what the project itself authored reaches Bob immediately, but
anything the project only *references* from elsewhere is still gated on Bob
himself trusting that publisher — exactly as if he had met it any other way.
Onboarding is not a special, looser tier of trust.
<!-- /doc:intro -->

<!-- doc:scenario: Bob clones the project and his assistant already has the team's context -->
This is the whole journey in one line, and everything else here is either
reinforcing this claim or drawing its edges. Bob does nothing but clone the
project and start a session — no init interview, no copied prompt, no setup
document to follow — and the team's standardized context is already present
when his assistant opens.
<!-- /doc:scenario -->

<!-- doc:scenario: Bob receives the versions the team pinned, not the latest ones -->
"We all use the same standard" only means something if everyone is actually
running the same version of it. Without a pin, Bob's very first pull could
resolve a dependency to whatever its upstream has moved on to since the rest
of the team last synced, and his assistant would diverge from theirs on day
one — silently, with nothing to signal it happened. This scenario proves the
lockfile's pin holds even on a completely fresh clone with no local cache to
fall back on: Bob receives exactly the version the team froze, not whatever
happens to be newest that morning.
<!-- /doc:scenario -->

<!-- doc:scenario: Content from a publisher Bob has not trusted is held, even on a fresh clone -->
Onboarding must never quietly become a loophole around the trust gate. The
project's own context is first-party — the team wrote it, so it reaches Bob
unconditionally, exactly as J7 already established for anyone on the team. But
the instant the project *references* a bundle published by someone else, that
reference is only a pointer until Bob personally trusts the key behind it, and
a brand-new machine changes nothing about that: the content is held for his
review precisely as it would be for anyone else meeting an untrusted publisher.

This is also the first moment a new hire meets "held for your review," and it
is worth being precise about what that phrase actually decides — approval and
denial mean something different for a paragraph of guidance than for an MCP
server or a hook that runs a command. See
[The trust surface](/journeys/trust-surface/) for the exhaustive approve/deny
table across fragments, skills, MCP servers, and hooks (and why a bundle
*profile* is not on that table at all) — a new hire hitting this hold can see
exactly what he is being asked to decide.
<!-- /doc:scenario -->

<!-- doc:scenario: Once Bob trusts the company key, the held content reaches him -->
The gate has to open the ordinary way too, or the previous scenario would only
prove that content gets stuck, not that trust is the actual lever. Bob makes
his own trust decision here — the same kind of decision Alice or Carol would
make anywhere else in this project — and the company's content reaches him the
moment he makes it. Nothing about being new changes what unlocks the content;
only trust does.
<!-- /doc:scenario -->

<!-- doc:scenario: Companion-dependent guidance reaches Bob only if he has the companion -->
No two new hires' machines look alike, and a setup that only works on a
machine identical to Alice's is not really onboarding — it is luck. Bob will
have some of the team's companion tools installed and not others, and whatever
guidance depends on a missing companion has to disappear cleanly rather than
break anything: a setup that fails on an absent optional tool is one a new
hire cannot get past. This scenario runs the same background twice — companion
present, companion absent — and checks both halves of the contrast in one
place: the companion-independent context always arrives, the
companion-dependent guidance arrives only when it can actually be used, and
nothing ever fails either way.
<!-- /doc:scenario -->

<!-- doc:scenario: Bob's engine is not Alice's and the team's context still reaches him natively -->
The new hire most likely to break "cloning is the onboarding" is precisely the
one who is not on Alice's own assistant. If the team's standard only actually
reached claude-code, the claim would be quietly false for anyone whose company
standardized on a different engine — and a brand-new hire is exactly the
person least equipped to notice that or work around it. This outline proves
the team's context lands in each of three engines' own native surface
(claude-code's `CLAUDE.md`, kiro's steering file, antigravity's `AGENTS.md`),
reusing the same per-engine proof [J4](/journeys/j4-multi-engine/) already
built for materialization generally, rather than re-deriving a second copy of
it here.

It deliberately does not claim a fourth row. codex never receives a
materialized context on this delivery path at all today — a confirmed gap J4
documents on its own — so asserting a codex row here would claim coverage
that does not exist. And like J4, this proves only that Bob's engine-native
file was *written* correctly; it does not prove his engine has read it.
<!-- /doc:scenario -->

<!-- doc:outro -->
Taken together, "cloning is the onboarding" is not one claim but five holding
at once: nothing to configure, the same pinned versions everyone else has, the
trust gate surviving a fresh machine without weakening it, graceful
degradation across whatever companions Bob happens to have, and the team's
context landing in each of three engines' own native surface regardless of
which one Bob actually uses. J8 builds directly on
[J7](/journeys/j7-team-authoring/)'s first-party authoring,
[J15](/journeys/j15-corporate-signed/)'s trust gate, and
[J4](/journeys/j4-multi-engine/)'s engine-native materialization — onboarding
is where all three have to hold at once, on a machine nobody set up by hand.
<!-- /doc:outro -->
