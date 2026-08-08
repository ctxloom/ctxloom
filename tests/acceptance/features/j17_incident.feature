@doc
Feature: An incident — a bad command ships and must be pulled

  A publisher's mistake does not stay contained to one developer. By the time
  anyone notices a bundle is wrong, it is usually already sitting on more than
  one machine — and pulling it back only matters if it reaches EVERYONE who
  already has it, quietly, on their next ordinary sync, without an operator
  chasing engineers down one by one. An incident also provokes a reflex of its
  own: revoke every key that might be involved. ctxloom has to be honest about
  where that reflex hits a wall it cannot get through.

  # NOTE ON SCOPE: J15 (j15_corporate_signed.feature) already has LOCKED, green
  # scenarios for publisher retraction (single developer), company-key
  # compromise/revocation, and rejection beating a trusted publisher. Writing
  # any of those again here would restate a mechanism J15 already proves — a
  # second green scenario asserting the same thing reads as coverage while
  # adding none. This journey keeps ONLY the things J15 cannot express; see
  # each scenario's own comment for exactly what that is and what it deliberately
  # does not repeat.

  # LOCKED — retraction across MORE THAN ONE developer. J15's retraction
  # scenario (j15_corporate_signed.feature:69) is single-persona: only Alice
  # ever has the bundle installed, so it cannot show whether retraction is a
  # per-developer accident or an actual property of the mechanism. The failure
  # that genuinely shipped once (taskloom task outer-shut) was that
  # ALREADY-INSTALLED content kept flowing after retraction — and
  # "already installed" is inherently a multi-machine fact. Carol and Bob each
  # install the bundle independently, on their own separate machines, before
  # Trent retracts it. Both stop receiving it on their own next routine sync,
  # with neither of them doing anything beyond an ordinary `remote pull` — no
  # special "acknowledge the incident" step exists or is needed.
  # Verified: internal/operations/sync.go's checkInstalledRetraction (called
  # from syncItem at :493) re-evaluates retraction for a ref already marked
  # installed — the exact code path that was a silent no-op before it was
  # fixed. NOT restating: single-developer retraction, key-compromise
  # revocation, or rejection-beats-trust — all already LOCKED in J15.
  Scenario: A retracted bundle stops reaching every developer who already installed it, not just one
    Given Carol leads a team that already uses the company's "incident-runbook" bundle
    And Bob has already pulled the team project and installed the bundle too
    When Trent retracts the bundle
    And Carol runs her next routine sync
    And Bob runs his next routine sync
    Then Carol is told the bundle was retracted, and her assistant no longer receives it
    And Bob is told the bundle was retracted too, and his assistant no longer receives it either

  # NEW — retraction must SURVIVE the remote going unreachable AFTERWARD. The
  # scenario above proves retraction reaches every already-installed developer
  # while the remote answers; it says nothing about what happens once it
  # doesn't. U088-F01 / U095-F02's fetch-failure half / U150-F04:
  # CheckRetracted's fetch-failure branches used to return "not retracted"
  # indistinguishably from a genuine clean bill of health — so a network
  # partition, an outage, or anyone sitting between a developer and the
  # publisher could resurrect retracted content merely by making the
  # retraction check fail. Fixed FAIL-STALE: the last known verdict is
  # persisted with its own timestamp (internal/remote/types.go
  # LockEntry.RetractionCheckedAt) and is what gets delivered when the remote
  # cannot be reached (internal/remote/pull.go Puller.resolveRetraction). This
  # is the security-critical direction to prove: a bundle already marked
  # RETRACTED must stay withheld even when the retraction check itself can no
  # longer run — the opposite failure (fail-open, today's pre-fix behavior)
  # is exactly what let an unreachable remote read as "cleared".
  Scenario: A retracted bundle stays withheld even after the remote that retracted it becomes unreachable
    Given Carol leads a team that already uses the company's "incident-runbook" bundle
    And Trent retracts the bundle
    And Carol runs her next routine sync
    And the company's remote becomes unreachable
    When Carol runs her next routine sync
    Then Carol is told the bundle is still retracted, and her assistant still does not receive it

  # UPDATED — the key is now VISIBLE, and CAN be locally distrusted. This
  # scenario used to assert the opposite of both halves below: `signer
  # show`/`signer list` never revealed ctxloom's own embedded publisher
  # principal at all (justified by a comment at operations/signer.go:245-251
  # claiming the embedded root was "empty today" — false the moment a release
  # key was actually embedded), and `signer remove` aimed at it reported a
  # bare "no entry for", indistinguishable from a typo'd principal that never
  # existed. Both were dishonest, not merely incomplete: an operator auditing
  # "whom do I trust to publish?" had no surface that would even show them
  # this key existed to worry about.
  #
  # The fix is two-part. Visibility: ListSigners (operations/signer.go) now
  # enumerates config.EmbeddedSigners() alongside the on-disk user/project
  # stores, tagged "embedded" and not-removable. Local revocation: `signer
  # remove <embedded-principal>` still cannot delete the compiled-in bytes —
  # nothing this CLI does can; shipping a new binary remains the only way to
  # change what's IN the binary — but it now writes a REAL local suppression
  # record (a new distrusted_signers store) that config.TrustRoot() subtracts
  # from the embedded root on every subsequent trust decision. This is not
  # cosmetic: TestVerifyPublisher_SuppressedPrincipal_NoLongerVerifies and
  # TestTrustRoot_SuppressedEmbeddedPrincipal_NoLongerTrusted
  # (internal/config) prove content genuinely signed by a suppressed key stops
  # verifying as trusted-publisher — this repo can never forge a signature
  # from ctxloom's actual production key, so those unit tests prove the
  # SUBTRACTION mechanism with a real generated key standing in for it, and
  # this acceptance scenario proves the CLI surface that drives it for real.
  Scenario: ctxloom's own publisher key is visible, and can be locally distrusted even though it cannot be deleted
    Given Alice's project exists
    When Trent removes ctxloom's own publisher key from the project's trust store
    Then ctxloom reports the key cannot be deleted but is now distrusted locally
    And ctxloom's own signer listing shows that key, tagged embedded and locally distrusted
