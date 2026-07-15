@doc
Feature: An incident — a bad command ships and must be pulled

  A publisher's mistake does not stay contained to one developer. By the time
  anyone notices a bundle is wrong, it is usually already sitting on more than
  one machine — and pulling it back only matters if it reaches EVERYONE who
  already has it, quietly, on their next ordinary sync, without an operator
  chasing engineers down one by one. An incident also provokes a reflex of its
  own: revoke every key that might be involved. ctxloom has to be honest about
  where that reflex hits a wall it cannot get through.

  # NOTE ON SCOPE: J3 (j3_corporate_signed.feature) already has LOCKED, green
  # scenarios for publisher retraction (single developer), company-key
  # compromise/revocation, and rejection beating a trusted publisher. Writing
  # any of those again here would restate a mechanism J3 already proves — a
  # second green scenario asserting the same thing reads as coverage while
  # adding none. This journey keeps ONLY the two things J3 cannot express; see
  # each scenario's own comment for exactly what that is and what it deliberately
  # does not repeat.

  # LOCKED — retraction across MORE THAN ONE developer. J3's retraction
  # scenario (j3_corporate_signed.feature:69) is single-persona: only Alice
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
  # revocation, or rejection-beats-trust — all already LOCKED in J3.
  Scenario: A retracted bundle stops reaching every developer who already installed it, not just one
    Given Carol leads a team that already uses the company's "incident-runbook" bundle
    And Bob has already pulled the team project and installed the bundle too
    When Trent retracts the bundle
    And Carol runs her next routine sync
    And Bob runs his next routine sync
    Then Carol is told the bundle was retracted, and her assistant no longer receives it
    And Bob is told the bundle was retracted too, and his assistant no longer receives it either

  # LOCKED — THE IRREVOCABLE KEY, documented rather than tested away. ctxloom's
  # own publish key (internal/config/trustroot.go:14-19) is compiled INTO the
  # binary via go:embed, and TrustRoot() (trustroot.go:47-57) unconditionally
  # unions it in alongside the on-disk user/project stores. signer remove
  # (operations/signer.go:317-370) only ever edits those two on-disk files —
  # there is no code path that reaches the embedded one. Revoking it for real
  # means shipping a new ctxloom binary; nothing this CLI does can. This
  # scenario asserts the CURRENT, HONEST behaviour, not a hoped-for one — we
  # are NOT fixing this here, we are saying it plainly before a reviewer finds
  # it unsaid. It also surfaces a sharper, unplanned-for fact: `signer
  # show`/`signer list` never reveal this principal as trusted at all (see
  # operations/signer.go:245-251's own comment, now stale — it says the
  # embedded root is "empty today", written the day BEFORE the release key was
  # actually embedded), so an operator has no CLI surface that would even show
  # them this key exists to worry about.
  Scenario: Nothing can revoke ctxloom's own publisher key — not even the signer command aimed straight at it
    Given Alice's project exists
    When Trent tries to remove ctxloom's own publisher key from the project's trust store
    Then ctxloom reports that no entry existed for that key
    And ctxloom's own signer listing never showed that key as trusted to begin with
