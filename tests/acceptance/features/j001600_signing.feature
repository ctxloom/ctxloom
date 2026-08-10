@doc
Feature: A signature somebody can check

  Signed, verifiable context is the thing ctxloom has that its competitors do
  not — and it is worth exactly as much as the artifacts it actually produces.
  Trent runs platform engineering at a company that decided its coding
  standards would be enforced, not merely published. He has an ordinary
  developer setup: an ed25519 key in his ssh-agent, and git already pointing at
  it because he signs his commits. He configures nothing ctxloom-specific. What
  he wants is that what reaches Alice's assistant provably came from him,
  unaltered — and that his teammates inherit that decision by cloning, not by
  each being told to run a command.

  # NOTE ON SCOPE: J001500 owns the ADVERSARY — tamper detection, retraction, key
  # revocation, rejection beating a trusted publisher. This journey owns the
  # PRODUCTION of the artifacts J001500 assumes: the signature itself, the trust
  # roots on disk, and the relocation that must carry both. Nothing here
  # re-proves tamper detection.
  #
  # Every signature assertion below reads the bundle bytes and the .sig sibling
  # FRESH OFF DISK and verifies them with internal/signing's own verifier —
  # never by trusting ctxloom's "signed by ..." success line. `ctxloom bundle
  # sign` had never produced a byte in an acceptance run before this file: an
  # empty .sig, a silently no-op --all, or a trust root written to the wrong
  # store would all have left every existing trust scenario green.

  Background:
    Given Trent's signing key is in his ssh-agent, and git already knows it is his signing key

  # The zero-config claim, stated by internal/signing/agentkey's own package
  # doc: anyone already signing commits with SSH needs no ctxloom setup at all.
  # The output must NAME which link of the discovery chain answered, so a
  # signature produced by an ambient developer agent could never be mistaken
  # for this one.
  Scenario: Trent signs a bundle with the key he already signs his commits with
    Given Trent's project publishes a bundle "secure-coding" carrying the fragment "tdd"
    When I run "ctxloom bundle sign secure-coding"
    Then the command succeeds
    And the output contains "signed by git config user.signingkey"
    And the signature beside bundle "secure-coding" is non-empty and verifies against the bundle's bytes on disk

  # COUNTED, not asserted-by-message: "signed everything" that signs nothing
  # and exits 0 is this codebase's characteristic failure, and --all over a
  # publish set is exactly where it would live.
  Scenario: Signing everything the project publishes signs every one of them
    Given Trent's project publishes 3 bundles
    When I run "ctxloom bundle sign --all"
    Then the command succeeds
    And every published bundle carries a signature that verifies, and there are exactly 3

  # MEASURED IN PRODUCTION, on ctxloom's own default-content repo: `bundle sign
  # --all` with no --key fell through to `git config user.signingkey`, which in
  # that checkout resolved to the author's PERSONAL key while the repo's
  # allowed_signers names the release identity. 43 of 45 bundles were re-signed
  # with a key the repo does not authorise — every one printing "signed by ..."
  # and exiting 0. Nothing surfaces at the publisher: the failure lands in a
  # CONSUMER, which withholds the bundle, and any profile inheriting it silently
  # degrades.
  #
  # So the guard is: when a repo DECLARES who may publish it, a run that would
  # sign as anyone else must refuse before it writes a byte. The count assertion
  # is the load-bearing one — a refusal that still wrote the unverifiable
  # signatures would be the same defect wearing an error message.
  Scenario: Signing with a key the repo does not authorise refuses instead of writing signatures nobody can verify
    Given Trent's repo declares "releases@acme.example" as the only identity allowed to publish, holding a key Trent does not have
    And Trent's project publishes 3 bundles
    When I run "ctxloom bundle sign --all"
    Then the command fails
    And the refusal names the key it would have signed with and the principal the repo requires
    And exactly 0 signature files exist in the published bundle tree

  # The other branch of the same guard, and the one that stops it degenerating
  # into "refuse whenever a declaration exists": the authorised key still signs,
  # and what it writes is checked the way .github/verify-signatures.sh checks it
  # — ssh-keygen -Y verify semantics against the declared key, in the publish
  # namespace — never against ctxloom's own success line.
  Scenario: Signing with the key the repo does authorise writes signatures that repo's own trust root accepts
    Given Trent's repo declares "releases@acme.example" as the only identity allowed to publish, and it is Trent's own key
    And Trent's project publishes 3 bundles
    When I run "ctxloom bundle sign --all"
    Then the command succeeds
    And every signature in the published bundle tree verifies against the key the repo declares

  # DIRECTORY-form bundles (<name>/bundle.yaml) are signed twice over, and only
  # one half was ever refreshed: `bundle sign` signs the TREE (SHA256SUMS ->
  # .sigs/) and left the detached bundle.yaml.sig sibling exactly as it found it
  # — absent on a first signing, STALE on a re-signing. Anything reading the
  # sibling (bundles' own localFSReader, .github/verify-signatures.sh, a
  # publishing repo's CI) then reports "incorrect signature" on a bundle its
  # author just signed and was told was signed.
  #
  # Reproduced on ctxloom-personal's `unattended` bundle; the workaround was to
  # run ssh-keygen -Y sign by hand. Asserted against the bundle.yaml bytes read
  # fresh off disk AFTER the edit, so a signature left over from the first
  # signing cannot satisfy it.
  Scenario: Re-signing a directory bundle refreshes the signature beside its manifest
    Given Trent's project publishes the directory bundle "unattended" carrying the fragment "policy"
    And Trent has signed the bundle "unattended"
    And Trent revises the directory bundle "unattended"
    When I run "ctxloom bundle sign unattended"
    Then the command succeeds
    And the signature beside the directory bundle "unattended" verifies against its bundle.yaml on disk
    # And the sibling must not have cost the bundle its OTHER attestation. The
    # sibling sits at the bundle root, where the manifest covers everything but
    # SHA256SUMS and .sigs/ — so writing it after the manifest is built leaves a
    # file the manifest never claims, and every consumer reads the freshly
    # signed bundle as content-added. Asserted through the consumer's own
    # verifier, which is the only thing that can tell.
    And the directory bundle "unattended" still verifies as a whole tree, with nothing unclaimed

  # The other half of the same failure: --all with nothing to sign must FAIL,
  # not report success over an empty set.
  Scenario: Signing everything when there is nothing to sign fails instead of reporting success
    Given Trent has authored no bundles yet
    When I run "ctxloom bundle sign --all"
    Then the command fails
    And the output contains "nothing was signed"
    And exactly 0 signature files exist in the published bundle tree

  # A publisher signature covers the whole bundle FILE, so an item ref resolves
  # to its containing bundle. The count assertion is the load-bearing one: a
  # per-fragment .sig would be a signature over a preimage no verifier reads.
  Scenario: Signing one fragment signs the bundle that contains it, and says so
    Given Trent's project publishes a bundle "my-tools" carrying the fragment "go-testing"
    When I run "ctxloom bundle sign my-tools#fragments/go-testing"
    Then the command succeeds
    And the output contains "Signing bundle my-tools (contains fragments/go-testing)"
    And the signature beside bundle "my-tools" is non-empty and verifies against the bundle's bytes on disk
    And exactly 1 signature file exists in the published bundle tree

  # `bundle sign` is the ONLY spelling: the deprecated top-level `ctxloom
  # sign` twin this scenario pair used to cover is deleted (verb-spine reorg
  # §6), so what survives is the canonical leaf signing a second, differently
  # named bundle — the payload assertion, which was always the value here.
  Scenario: Signing a bundle by bare name lands a verifiable signature
    Given Trent's project publishes the "secure-coding" bundle his team depends on
    When I run "ctxloom bundle sign secure-coding"
    Then the command succeeds
    And the signature beside bundle "secure-coding" is non-empty and verifies against the bundle's bytes on disk

  # --project is how a team distributes trust: the committable store every
  # clone inherits, as opposed to the user store that follows one person. BOTH
  # paths are asserted — a trust root written to the wrong store is invisible
  # until a teammate's clone silently trusts nothing.
  Scenario: Trent distributes his trust root in the store his teammates inherit by cloning
    When I run "ctxloom signer trust context@acme.example --key acme-publish.pub --project --yes"
    Then the command succeeds
    And the project store ".ctxloom/allowed_signers" trusts "context@acme.example" for publishing, with Trent's own key
    And the user store ".ctxloom/allowed_signers" was never written
    When I run "ctxloom signer list"
    Then the command succeeds
    And the listing names "context@acme.example" in the "project" store, with Trent's fingerprint and the publish namespace

  # A principal can hold entries in both stores at once, and `show` is the only
  # command that has to render BOTH — a listing that silently collapses them
  # hides half of what is actually trusted.
  Scenario: A signer trusted in both stores is shown from both
    Given Trent's key is trusted in the committable project store as "context@acme.example"
    And Trent's key is also trusted in his personal user store as "context@acme.example"
    When I run "ctxloom signer show context@acme.example"
    Then the command succeeds
    And the listing names "context@acme.example" in the "project" store, with Trent's fingerprint and the publish namespace
    And the listing names "context@acme.example" in the "user" store, with Trent's fingerprint and the publish namespace

  # The acceptance binds to the item's CURRENT content hashes, so a later
  # revision returns it to pending rather than riding the old decision.
  #
  # THREE assertions after the pull, not one, because "the revised marker is
  # absent" alone was satisfied by the revision NEVER ARRIVING (audit
  # irate-catfish, F1): a plain `deps pull` is passive and never advances an
  # existing pin ("Skipped (kept at their locked commit)"), so Alice went on
  # holding — and being served — the ORIGINAL bytes, and the scenario proved
  # nothing about what an acceptance binds to. Taking the new commit needs
  # `deps upgrade` first, and the two assertions added here are the
  # ones an absence cannot fake:
  #   - the ORIGINAL guidance stops being delivered. It was approved and
  #     flowing one step earlier, so this can only be true if the revision
  #     actually reached her project and displaced it.
  #   - the item reads as PENDING again — the claim in the scenario title,
  #     and the state a still-pinned, still-approved original could never be in.
  Scenario: Alice accepts one item, and a later revision returns it to review
    Given Trent's project publishes the "secure-coding" bundle his team depends on
    And Trent has signed the bundle "secure-coding"
    And Alice's own review key is trusted for approve and reject as "reviewer@acme.example"
    And Trent publishes the signed bundle to his company repo, and Alice references it
    And her assistant does not receive the "tdd" guidance
    When I run "ctxloom bundle trust" on the published "tdd" fragment
    Then her assistant receives the "tdd" guidance
    When Trent revises the "tdd" fragment, re-signs it, and publishes again
    And Alice pulls the newly published version
    Then her assistant does not receive the "revised tdd" guidance
    And her assistant no longer receives the "tdd" guidance either
    And the published "tdd" fragment's review state is "pending"

  # A rejection writes two companion records: the ref-level block (sticky,
  # survives content changes) and the item's content hashes on the denylist (so
  # a renamed or moved identical copy stays rejected too). Asserted through
  # what ctxloom reports it wrote AND through what stops being delivered.
  Scenario: Alice rejects one item and it stays out, though the signature still verifies
    Given Trent's project publishes the "secure-coding" bundle his team depends on
    And Trent has signed the bundle "secure-coding"
    And Trent's key is trusted in the committable project store as "context@acme.example"
    And Alice's own review key is trusted for approve and reject as "reviewer@acme.example"
    And Trent publishes the signed bundle to his company repo, and Alice references it
    And her assistant receives the "curl-pipe-sh" guidance
    When I run "ctxloom bundle reject" on the published "curl-pipe-sh" fragment
    Then the output contains "ref block: recorded"
    And the output contains "content:   rejected in form(s)"
    And her assistant does not receive the "curl-pipe-sh" guidance
    And the content Trent signed still verifies against the bytes he published

  # Removing a signer means "I will review this myself from now on", not
  # "deny" — so the content is held, not refused. The assertion that makes the
  # removal real rather than cosmetic is on the STORE FILE, not the listing.
  Scenario: Withdrawing trust in Trent's key removes the key line itself
    Given Trent's project publishes the "secure-coding" bundle his team depends on
    And Trent has signed the bundle "secure-coding"
    And Trent's key is trusted in the committable project store as "context@acme.example"
    And Trent publishes the signed bundle to his company repo, and Alice references it
    And her assistant receives the "tdd" guidance
    When I run "ctxloom signer untrust context@acme.example --project"
    Then the command succeeds
    And the output contains "removed 1 entry for context@acme.example"
    And the project store ".ctxloom/allowed_signers" no longer names Trent's key
    And the project store ".ctxloom/allowed_signers" holds nothing at all
    And her assistant does not receive the "tdd" guidance

  # The scenario above removes the store's ONLY entry, so it can never see what
  # a removal does to the lines it keeps. That is the whole of the removal
  # path: it parses the file, decides which lines to drop, and REWRITES the
  # file from the survivors. Every failure mode of that rewrite — dropping a
  # neighbouring entry, duplicating one, reordering them, losing the final
  # newline that makes the last line readable at all — leaves a command that
  # exits 0 and prints exactly the right count over a trust root that has
  # quietly changed. Only the file's contents can tell the two apart.
  Scenario: Removing one signer leaves every other entry in the store intact
    Given Trent's team trusts "alpha@acme.example,context@acme.example,omega@acme.example" in the committable project store
    And the project store ".ctxloom/allowed_signers" holds exactly the entries for "alpha@acme.example,context@acme.example,omega@acme.example"
    When I run "ctxloom signer untrust context@acme.example --project"
    Then the command succeeds
    And the output contains "removed 1 entry for context@acme.example"
    And the project store ".ctxloom/allowed_signers" holds exactly the entries for "alpha@acme.example,omega@acme.example"
    # Down to a SINGLE survivor, because "join the kept lines and terminate the
    # result" behaves differently at one than at several: a rewrite that only
    # terminates the last line when there are at least two leaves a lone
    # surviving entry unterminated, and an unterminated final line is one
    # ssh-keygen does not read — the key stops counting while `list` and the
    # exit code both still look right.
    When I run "ctxloom signer untrust omega@acme.example --project"
    Then the command succeeds
    And the output contains "removed 1 entry for omega@acme.example"
    And the project store ".ctxloom/allowed_signers" holds exactly the entries for "alpha@acme.example"

  # "Nothing to remove" must be a no-op on disk as well as a message. A remove
  # that reports "no entry" while rewriting the store has still edited the
  # trust root, and a remove that reports having removed something when it
  # removed nothing tells an operator a key is gone while it still signs.
  Scenario: Removing a principal the store does not hold changes nothing
    Given Trent's team trusts "alpha@acme.example,omega@acme.example" in the committable project store
    And I note exactly what the project store ".ctxloom/allowed_signers" holds
    When I run "ctxloom signer untrust nobody@acme.example --project"
    Then the command succeeds
    And the output contains "no entry for nobody@acme.example"
    And the output does not contain "removed"
    And the project store ".ctxloom/allowed_signers" is byte-for-byte what it was
    And the project store ".ctxloom/allowed_signers" holds exactly the entries for "alpha@acme.example,omega@acme.example"

  # No store file at all is the state every project starts in, and it is
  # "nothing to remove", not "removed something". Reporting a removal against
  # a file that does not exist tells an operator they have withdrawn trust
  # they never granted — and leaves them believing a key is handled.
  Scenario: Removing a signer when the project has no store yet reports nothing removed
    Given the file ".ctxloom/allowed_signers" does not exist
    When I run "ctxloom signer untrust nobody@acme.example --project"
    Then the command succeeds
    And the output contains "no entry for nobody@acme.example"
    And the output does not contain "removed"
    And the file ".ctxloom/allowed_signers" does not exist

  # A line the parser cannot read contributes no entry, so a principal hiding
  # in it looks ABSENT. Reporting "no entry for X" there would tell an operator
  # a key is untrusted while the line that may grant it stays in the file —
  # the failure mode this refusal exists to prevent. It must refuse, name the
  # unreadable line, and leave the store exactly as it found it.
  Scenario: Removing nothing from a store with an unreadable line refuses instead of reporting nothing to remove
    Given Trent's team trusts "alpha@acme.example" in the committable project store
    And one line in the project store ".ctxloom/allowed_signers" cannot be read
    And I note exactly what the project store ".ctxloom/allowed_signers" holds
    When I run "ctxloom signer untrust nobody@acme.example --project"
    Then the command fails
    And the output contains "could not be read"
    And the project store ".ctxloom/allowed_signers" is byte-for-byte what it was
    And the project store ".ctxloom/allowed_signers" still holds the line that could not be read

  # A scripted caller writes the FULL namespace strings the store itself uses
  # rather than the short aliases a human types, and both spellings have to
  # land the same grant — otherwise a CI job that says "approve" the long way
  # silently trusts a key for nothing, or for more than it asked. The grant
  # written is asserted exactly, because "trusted for approve" and "trusted
  # for approve and publish" differ by the ability to publish.
  Scenario: A grant written with full namespace strings lands exactly that grant
    When I run "ctxloom signer trust scripted@acme.example --key acme-publish.pub --namespace publish.v1.ctxloom.dev,approve.v1.ctxloom.dev,reject.v1.ctxloom.dev --project --yes"
    Then the command succeeds
    And the project store ".ctxloom/allowed_signers" trusts "scripted@acme.example" for exactly the namespaces "publish.v1.ctxloom.dev,approve.v1.ctxloom.dev,reject.v1.ctxloom.dev"

  # A namespace ctxloom does not recognise must be REFUSED, not written
  # through. Storing it verbatim would produce an allowed_signers line that
  # grants nothing while reading, to anyone auditing the file, as a grant.
  Scenario: A grant naming an unrecognised namespace is refused, and writes nothing
    When I run "ctxloom signer trust typo@acme.example --key acme-publish.pub --namespace publsh --project --yes"
    Then the command fails
    And the output contains "unknown namespace"
    And the file ".ctxloom/allowed_signers" does not exist

  # `signer create` APPENDS to whatever is already there, and a store whose
  # last line has no terminating newline is the ordinary result of a human
  # editing it by hand. Appending without repairing that would splice the new
  # entry onto the end of the existing one, destroying both: one unparseable
  # line where there were two grants.
  Scenario: Trusting a second signer repairs a store whose last line is unterminated
    Given the project store ".ctxloom/allowed_signers" holds one entry with no trailing newline
    When I run "ctxloom signer trust second@acme.example --key acme-publish.pub --namespace publish --project --yes"
    Then the command succeeds
    And the project store ".ctxloom/allowed_signers" holds exactly the entries for "handwritten@acme.example,second@acme.example"

  # The mirror of the refusal above. When something WAS removed the command
  # did its job, so an unrelated unreadable line is a WARNING, not a failure —
  # but it must still be left in place rather than quietly dropped by the
  # rewrite. Losing it would be the rewrite silently editing a line it could
  # not read, which is the one thing a trust root must never do.
  Scenario: Removing a signer alongside an unreadable line warns but keeps the line
    Given Trent's team trusts "alpha@acme.example,context@acme.example" in the committable project store
    And one line in the project store ".ctxloom/allowed_signers" cannot be read
    When I run "ctxloom signer untrust context@acme.example --project"
    Then the command succeeds
    And the output contains "removed 1 entry for context@acme.example"
    And the project store ".ctxloom/allowed_signers" still holds the line that could not be read

  # ctxloom's own compiled-in release key is the one entry no `delete` can
  # edit, so the withdrawal has to be a RECORD the read side subtracts. The
  # record is only worth anything if it names the embedded entry's own
  # principal strings: the subtraction is a literal membership check, so a
  # record naming anything else reports success and suppresses nothing.
  Scenario: Withdrawing trust in ctxloom's own embedded key records a suppression the read side can honour
    Given ctxloom's own compiled-in publishing key is trusted
    When I run "ctxloom signer untrust ben+ctxloom@abbitt.me --project"
    Then the command succeeds
    And the output contains "cannot be deleted"
    And the output contains "DISTRUSTED"
    # The message must NAME the file it recorded the withdrawal in. A
    # suppression an operator cannot locate is one they cannot audit or undo,
    # and "recorded in " with nothing after it reads as success either way.
    And the output contains ".ctxloom/distrusted_signers"
    And the distrusted store ".ctxloom/distrusted_signers" records every principal that entry names
    When I run "ctxloom signer list"
    Then the command succeeds
    And the output contains "LOCALLY DISTRUSTED"

  # Withdrawing twice must leave one record, not two. Idempotence here is a
  # property of the FILE — the second run takes the "already suppressed" path
  # only if it actually reads back what the first one wrote.
  Scenario: Withdrawing trust in the embedded key twice records it once
    Given ctxloom's own compiled-in publishing key is trusted
    When I run "ctxloom signer untrust ben+ctxloom@abbitt.me --project"
    Then the command succeeds
    When I run "ctxloom signer untrust ben+ctxloom@abbitt.me --project"
    Then the command succeeds
    And the distrusted store ".ctxloom/distrusted_signers" records every principal that entry names

  # This scenario is the SECOND branch of its own title, and it was a PRODUCT
  # BUG until it was one — carried @wip for as long as neither branch held.
  #
  # What it used to do, on the ORDINARY developer setup — a signing key in
  # ssh-agent, which is exactly what the rest of this feature establishes:
  # `ctxloom bundle trust` recorded a SIGNED approval, printed "Approved …
  # signed by SHA256:…", exited 0, and the item stayed withheld. The signed
  # record and the unsigned one carried the SAME ref and the SAME payload_hash
  # in ~/.ctxloom/approvals/index.yaml, differing only in `unsigned: true`
  # versus `principal: SHA256:…`, and only the unsigned one took effect —
  # because a signed decision is honoured only when its signer is trusted for
  # the approve namespace (VerifyCountersignature asks TrustedForNamespace
  # before it verifies a single byte, and answers a flat "not countersigned"
  # when the answer is no), and nothing in the accept flow said so. Removing
  # the key from the agent made the identical command work. Exit 0, a success
  # message naming the key, and no effect — the flagship trust command, in the
  # default configuration, doing this project's signature failure.
  #
  # It is closed by authorizing the key on the WRITE side too, against the same
  # trust root and through the same namespace derivation the verifier uses
  # (operations.resolveDecisionSigner / requireTrustedForAssertion), so the two
  # sides cannot disagree about which namespace a decision needs. The command
  # now REFUSES, at the point of decision, before anything is written.
  #
  # Asserted on all three things a silent no-op gets wrong at once: the exit
  # code, the message a human acts on, and what landed on disk. The RECORD
  # assertion is the load-bearing one — a refusal that still wrote the useless
  # approval would satisfy the other two — and it reads the store's own files
  # rather than the honoured-decision lookup, which answers "no" for a written
  # record too and so cannot tell "refused" from the bug.
  #
  # The passing twin is the acceptance scenario above, which differs by exactly
  # one Given: Alice's review key trusted for approve and reject.
  Scenario: A review decision recorded with an untrusted key is honoured, or says why it is not
    Given Trent's project publishes the "secure-coding" bundle his team depends on
    And Trent has signed the bundle "secure-coding"
    And Trent publishes the signed bundle to his company repo, and Alice references it
    And her assistant does not receive the "tdd" guidance
    When I try to run "ctxloom bundle trust" on the published "tdd" fragment
    Then the command fails
    And the refusal names Alice's key, the "approve" namespace, and how to trust it
    And the approvals store holds no approve record at all
    And her assistant does not receive the "tdd" guidance
    And the published "tdd" fragment's review state is "pending"

  # Bytes carried VERBATIM — never re-parsed, never re-serialized, never
  # re-signed. This is the scenario that catches a "helpful" round-trip: if the
  # mover ever re-emits the YAML the signature dies, and the only visible
  # symptom is content quietly going unsigned at the destination.
  Scenario: Relocating a bundle carries its bytes and its signature verbatim
    Given Trent's project publishes a bundle "go-tools" carrying the fragment "layout"
    And Trent has signed the bundle "go-tools"
    When I run "ctxloom bundle move" to relocate "go-tools" into the shared standards directory
    Then the command succeeds
    And the relocated "go-tools" is byte-identical to what was signed, and its signature still verifies
    And the source bundle "go-tools" and its signature are gone

  # The ordering invariant: the source is removed ONLY after the destination
  # holds bundle AND signature. Asserted as "the source is still there", not as
  # an error string — a move that eats the source and then reports an error is
  # the failure that matters.
  Scenario: A move that cannot carry the signature leaves the source untouched
    Given Trent's project publishes a bundle "go-tools" carrying the fragment "layout"
    And Trent has signed the bundle "go-tools"
    And Trent edits the bundle "go-tools" after signing it
    When I run "ctxloom bundle move" to relocate "go-tools" into the shared standards directory
    Then the command fails
    And the source bundle "go-tools" and its signature are untouched
    And the shared standards directory is still empty
