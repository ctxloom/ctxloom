<!--
J001700 narration companion.

Prose ONLY. It never restates what the Gherkin already says business-readably,
and it carries no assertions of its own — j001700_incident.feature next to it is the
single source of truth for what J001700 promises.

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

This journey is deliberately short: it keeps the one lock on this door that
ctxloom **cannot** open.
<!-- /doc:intro -->

<!-- doc:scenario: ctxloom's own publisher key is visible, and can be locally distrusted even though it cannot be deleted -->
Now the honest part — updated, because the dishonest part got fixed.

During an incident, the reflex is to revoke every key that might be involved.
**ctxloom's own publisher key** used to be the one key that reflex could not touch at all, in
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
has not changed. But `signer untrust` aimed at the embedded principal is no
longer a no-op that reports "no entry for" and walks away. It now writes a
real, local record — this machine (or this project, with `--project`) no
longer trusts that key — and every subsequent signature verification honors
it. The listing keeps showing the key (visibility doesn't regress just because
you acted on it) but now tags it **locally distrusted**.

This is the honest shape of the guarantee: you cannot un-ship a compiled-in
key, but you are no longer stuck trusting whatever it signs just because you
run the binary that ships with it. If you don't want to trust ctxloom's release
key at all, you no longer have to find that out by accident — you can see it,
and you can turn it off.
<!-- /doc:scenario -->

<!-- doc:outro -->
The embedded release key is visible, and — while it can never be deleted from
the binary — it can be locally distrusted the moment you decide you don't want
it. For what ctxloom explicitly does not defend against, see the
[threat model](/security/threat-model/).
<!-- /doc:outro -->
