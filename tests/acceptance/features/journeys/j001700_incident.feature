@doc
Feature: An incident — a bad command ships and must be pulled

  A publisher's mistake does not stay contained to one developer. By the time
  anyone notices a bundle is wrong, it is usually already sitting on more than
  one machine — and pulling it back only matters if it reaches EVERYONE who
  already has it, quietly, on their next ordinary sync, without an operator
  chasing engineers down one by one. An incident also provokes a reflex of its
  own: revoke every key that might be involved. ctxloom has to be honest about
  where that reflex hits a wall it cannot get through.

  # UPDATED — the key is now VISIBLE, and CAN be locally distrusted. This
  # scenario used to assert the opposite of both halves below: `signer
  # show`/`signer list` never revealed ctxloom's own embedded publisher
  # principal at all (justified by a comment at operations/signer.go:245-251
  # claiming the embedded root was "empty today" — false the moment a release
  # key was actually embedded), and `signer untrust` aimed at it reported a
  # bare "no entry for", indistinguishable from a typo'd principal that never
  # existed. Both were dishonest, not merely incomplete: an operator auditing
  # "whom do I trust to publish?" had no surface that would even show them
  # this key existed to worry about.
  #
  # The fix is two-part. Visibility: ListSigners (operations/signer.go) now
  # enumerates config.EmbeddedSigners() alongside the on-disk user/project
  # stores, tagged "embedded" and not-removable. Local revocation: `signer
  # untrust <embedded-principal>` still cannot delete the compiled-in bytes —
  # nothing this CLI does can; shipping a new binary remains the only way to
  # change what's IN the binary — but it now writes a REAL local suppression
  # record (a new distrusted_signers store) that config.TrustRoot() subtracts
  # from the embedded root on every subsequent trust decision. This is not
  # cosmetic: TestVerifyPublisher_SuppressedPrincipal_NoLongerVerifies and
  # TestTrustRoot_SuppressedEmbeddedPrincipal_NoLongerTrusted
  # (internal/core/config) prove content genuinely signed by a suppressed key stops
  # verifying as trusted-publisher — this repo can never forge a signature
  # from ctxloom's actual production key, so those unit tests prove the
  # SUBTRACTION mechanism with a real generated key standing in for it, and
  # this acceptance scenario proves the CLI surface that drives it for real.
  # Tabled by format: `signer untrust`/`signer show` are wired to emit(), so
  # off a terminal (which this harness always is) the no-flag row now gets
  # their JSON result, not the prose the old assertion checked
  # unconditionally. steps_j001700.go's Then steps read
  # operations.RemoveSignerResult's EmbeddedSuppressed/SuppressionPath and
  # operations.SignerListing's Source/Suppressed fields directly for a
  # structured row, and keep the original substring checks for text.
  Scenario Outline: ctxloom's own publisher key is visible, and can be locally distrusted even though it cannot be deleted
    Given Alice's project exists
    When Trent removes ctxloom's own publisher key from the project's trust store, asking for "<flags>"
    Then ctxloom reports the key cannot be deleted but is now distrusted locally
    And ctxloom's own signer listing shows that key, tagged embedded and locally distrusted

    Examples: no --format at all takes the derived default off a terminal; an explicit one wins in both directions
      | flags         |
      |               |
      | --format json |
      | --format text |
