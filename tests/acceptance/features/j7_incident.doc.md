<!--
J7 narration companion.

Prose ONLY. It never restates what the Gherkin already says business-readably,
and it carries no assertions of its own — j7_incident.feature next to it is the
single source of truth for what J7 promises.

Marker convention: an opening prose block, one block per scenario keyed to that
scenario's EXACT name, and a closing block.
-->

<!-- doc:intro -->
Somebody ships a bad skill. It tells engineers' assistants to do the wrong
thing — a deploy step that corrupts data, a "security" idiom that is anything
but. Publishing it took one push. Now it is on an unknown number of machines,
and every one of those assistants is confidently repeating it.

The only question that matters in the next hour is: **can you actually pull it
back?** Not "can you delete it from the repo" — anyone can do that, and it
changes nothing for the developers who already have it. Can you make it stop
reaching the people who *already installed it*, without knowing who they are,
without them having to do anything unusual, and without waiting for them to
notice a Slack message?

That is the whole of this journey, and it is deliberately short. Publisher
retraction, key revocation, and a user's rejection outranking a trusted
publisher are all proven already in
[J3](/journeys/j3-corporate-signed/) — going over them again here would look
like extra coverage while adding none. J7 keeps only the two things J3 cannot
say: what happens across **more than one developer**, and the one lock on this
door that ctxloom **cannot** open.
<!-- /doc:intro -->

<!-- doc:scenario: A retracted bundle stops reaching every developer who already installed it, not just one -->
"Already installed" is where retraction systems usually die, and it is a
multi-machine fact by nature — a single developer's story cannot expose it. So
this scenario has two: Carol and Bob, on genuinely separate checkouts, each of
whom pulled and installed the bundle independently before anything went wrong.

Then Trent retracts it. Neither Carol nor Bob does anything special — there is
no "acknowledge the incident" command, no re-onboarding, no cache to clear. Each
of them simply runs their next ordinary sync, the same one they would have run
anyway, and on that sync each is told the bundle was retracted *and why*, in the
publisher's own words. Their assistants stop receiving it. Independently. Without
coordination.

The reason this deserves its own scenario rather than a footnote to J3: ctxloom
got this wrong once. Retraction was evaluated only inside a fresh pull, and an
already-installed reference never pulls again on an ordinary sync — so a
retraction was a silent no-op against precisely the population that already had
the bad content. The fix re-evaluates retraction for already-installed
references and records the verdict locally, so the trust gate withholds the
content at the very next exposure without any network call of its own. This
scenario exists to make sure that stays fixed, and it has been verified to fail
when the mechanism is disabled — a security test that has never been seen to
fail is not a security test.
<!-- /doc:scenario -->

<!-- doc:scenario: Nothing can revoke ctxloom's own publisher key — not even the signer command aimed straight at it -->
Now the honest part.

During an incident, the reflex is to revoke every key that might be involved.
For a company's own signing key, ctxloom supports that fully — J3 proves it, and
one revocation withdraws everything that key ever signed. But there is one key
that reflex cannot touch: **ctxloom's own publisher key**.

That key is compiled into the ctxloom binary, and the trust root unconditionally
includes it alongside your on-disk stores. `ctxloom signer remove` only ever
edits those on-disk stores. Aim it directly at the embedded principal and it
does exactly what this scenario shows: reports that no such entry existed, and
changes nothing. Content signed by that key remains trusted afterward. **There
is no CLI path that revokes it. Revoking it for real requires shipping a new
ctxloom binary.**

It gets one degree worse. `signer show` and `signer list` never surface that
key at all — the embedded root is deliberately not listed. So an operator
auditing "whom do I trust to publish?" does not merely find a key they cannot
remove; they never see that it is there. The `signer list` implementation still
carries a comment saying the embedded root is "empty today," which was true the
day it was written and stopped being true when the release key was embedded.

We are not fixing that here and we are not going to imply it is smaller than it
is. This is what trusting the ctxloom binary actually means, stated plainly:
**you are trusting whatever that key signs, for as long as you run that binary.**
If that is not a trade you want to make, the honest remedy is not a flag — it is
building ctxloom yourself with a trust root you control. A reviewer would find
this on their own within an hour. Better that they find we said it first.
<!-- /doc:scenario -->

<!-- doc:outro -->
Two scenarios, and that is the right number. The first proves the guarantee that
actually matters in an incident: a retraction reaches everyone who already has
the content, on their own next routine sync, with a reason. The second names the
one place that guarantee does not reach, in our own documentation, before anyone
else gets to name it for us.

For the rest of the trust model — signing, review, tampering, key revocation, and
what ctxloom explicitly does not defend against — see
[J3](/journeys/j3-corporate-signed/),
[Trust states and the gate](/security/trust-states/), and the
[threat model](/security/threat-model/).
<!-- /doc:outro -->
