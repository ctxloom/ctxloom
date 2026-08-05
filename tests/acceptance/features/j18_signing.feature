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

  # NOTE ON SCOPE: J3 owns the ADVERSARY — tamper detection, retraction, key
  # revocation, rejection beating a trusted publisher. This journey owns the
  # PRODUCTION of the artifacts J3 assumes: the signature itself, the trust
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
    When I run "ctxloom trust signer create context@acme.example --key acme-publish.pub --project --yes"
    Then the command succeeds
    And the project store ".ctxloom/allowed_signers" trusts "context@acme.example" for publishing, with Trent's own key
    And the user store ".ctxloom/allowed_signers" was never written
    When I run "ctxloom trust signer list"
    Then the command succeeds
    And the listing names "context@acme.example" in the "project" store, with Trent's fingerprint and the publish namespace

  # A principal can hold entries in both stores at once, and `show` is the only
  # command that has to render BOTH — a listing that silently collapses them
  # hides half of what is actually trusted.
  Scenario: A signer trusted in both stores is shown from both
    Given Trent's key is trusted in the committable project store as "context@acme.example"
    And Trent's key is also trusted in his personal user store as "context@acme.example"
    When I run "ctxloom trust signer show context@acme.example"
    Then the command succeeds
    And the listing names "context@acme.example" in the "project" store, with Trent's fingerprint and the publish namespace
    And the listing names "context@acme.example" in the "user" store, with Trent's fingerprint and the publish namespace

  # The acceptance binds to the item's CURRENT content hashes, so a later
  # revision returns it to pending rather than riding the old decision.
  #
  # THREE assertions after the pull, not one, because "the revised marker is
  # absent" alone was satisfied by the revision NEVER ARRIVING (audit
  # irate-catfish, F1): a plain `remote pull` is passive and never advances an
  # existing pin ("Skipped (kept at their locked commit)"), so Alice went on
  # holding — and being served — the ORIGINAL bytes, and the scenario proved
  # nothing about what an acceptance binds to. Taking the new commit needs
  # `remote update --apply` first, and the two assertions added here are the
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
    When I run "ctxloom trust accept" on the published "tdd" fragment
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
    When I run "ctxloom trust reject" on the published "curl-pipe-sh" fragment
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
    When I run "ctxloom trust signer delete context@acme.example --project"
    Then the command succeeds
    And the output contains "removed 1 entry for context@acme.example"
    And the project store ".ctxloom/allowed_signers" no longer names Trent's key
    And her assistant does not receive the "tdd" guidance

  # PRODUCT BUG, found while wiring this journey — reported, not fixed, and
  # tagged @wip because it is a real product gap rather than a harness one
  # (the same tag j3_corporate_signed.feature's retraction scenario carried
  # while retraction had no effect). Excluded from the default run; delete the
  # tag when the product decides.
  #
  # On the ORDINARY developer setup — a signing key in ssh-agent, which is
  # exactly what the rest of this feature establishes — `ctxloom trust accept`
  # records a SIGNED approval, prints "Approved …  signed by SHA256:…", and
  # exits 0. The item stays withheld. Measured: the signed record and the
  # unsigned one carry the SAME ref and the SAME payload_hash in
  # ~/.ctxloom/approvals/index.yaml, differing only in `unsigned: true` versus
  # `principal: SHA256:…`, and only the unsigned one takes effect — because a
  # signed decision is honoured only when its signer is trusted for the
  # approve namespace, which nothing in the accept flow tells the user to do
  # (and which `ctxloom review --list` then reports as an "update", not as an
  # untrusted decision). Remove the key from the agent and the identical
  # command works. Exit 0, a success message naming the key, and no effect —
  # in the flagship trust command, in the default configuration.
  @wip
  Scenario: A review decision recorded with an untrusted key is honoured, or says why it is not
    Given Trent's project publishes the "secure-coding" bundle his team depends on
    And Trent has signed the bundle "secure-coding"
    And Trent publishes the signed bundle to his company repo, and Alice references it
    And her assistant does not receive the "tdd" guidance
    When I run "ctxloom trust accept" on the published "tdd" fragment
    Then her assistant receives the "tdd" guidance

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
