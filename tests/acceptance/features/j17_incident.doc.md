<!--
J17 narration companion.

Prose ONLY. It never restates what the Gherkin already says business-readably,
and it carries no assertions of its own — j17_incident.feature next to it is the
single source of truth for what J17 promises.

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
[J15](/journeys/j15-corporate-signed/) — going over them again here would look
like extra coverage while adding none. J17 keeps only the two things J15 cannot
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

The reason this deserves its own scenario rather than a footnote to J15: ctxloom
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

<!-- doc:scenario: ctxloom's own publisher key is visible, and can be locally distrusted even though it cannot be deleted -->
Now the honest part — updated, because the dishonest part got fixed.

During an incident, the reflex is to revoke every key that might be involved.
For a company's own signing key, ctxloom supports that fully — J15 proves it, and
one revocation withdraws everything that key ever signed. **ctxloom's own
publisher key** used to be the one key that reflex could not touch at all, in
two separate ways: nothing revoked it, and — worse — nothing even showed it to
you. `signer show`/`signer list` never surfaced the embedded principal, and the
one comment that tried to explain why claimed the embedded root was "empty
today" — written the day before a release key was actually embedded into it,
and never updated since. An operator auditing "whom do I trust to publish?"
didn't just find a key they couldn't remove; they never learned it was there
to worry about.

Both halves are fixed now. Visibility first: `signer show`/`signer list`
enumerate the embedded root like any other trust-root location, tagged
`embedded` so it reads honestly as "compiled into this binary," not as an
ordinary on-disk entry. Then revocation: this key's bytes are still compiled
into the ctxloom binary, and nothing this CLI does can delete them — shipping
a new binary remains the only way to change what's actually IN it, and that
has not changed. But `signer remove` aimed at the embedded principal is no
longer a no-op that reports "no entry for" and walks away. It now writes a
real, local record — this machine (or this project, with `--project`) no
longer trusts that key — and every subsequent trust decision honors it. The
listing keeps showing the key (visibility doesn't regress just because you
acted on it) but now tags it **locally distrusted**, and content signed only
by that key from here on is withheld, exactly as if the key had never been
embedded in the first place.

This is the honest shape of the guarantee: you cannot un-ship a compiled-in
key, but you are no longer stuck trusting whatever it signs just because you
run the binary that ships with it. If you don't want to trust ctxloom's release
key at all, you no longer have to find that out by accident — you can see it,
and you can turn it off.
<!-- /doc:scenario -->

<!-- doc:outro -->
Two scenarios, and that is the right number. The first proves the guarantee that
actually matters in an incident: a retraction reaches everyone who already has
the content, on their own next routine sync, with a reason. The second proves
the guarantee that used to be missing from ctxloom's own documentation: the
embedded release key is visible, and — while it can never be deleted from the
binary — it can be locally distrusted, with real effect, the moment you decide
you don't want it.

For the rest of the trust model — signing, review, tampering, key revocation, and
what ctxloom explicitly does not defend against — see
[J15](/journeys/j15-corporate-signed/),
[Trust states and the gate](/security/trust-states/), and the
[threat model](/security/threat-model/).
<!-- /doc:outro -->
