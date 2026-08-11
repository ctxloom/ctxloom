@doc
Feature: Content my company has validated

  A company needs to guarantee that the guidance reaching its engineers'
  assistants is guidance the company actually approved — and that nothing else
  slips in. That is the difference between "we wrote a standard" and "the
  standard is enforced." Without it, anyone who can publish can silently rewrite
  how every engineer's assistant behaves, or worse, ship an executable that runs
  on their machines. ctxloom answers one question crisply: did what reached my
  assistant come from who I think, unchanged — and only what I allowed?

  # NOTE ON SCOPE: this proves PROVENANCE and INTEGRITY, not secrecy. ctxloom does
  # not encrypt context; there is no Eve here. Mallory is the adversary — she does
  # not want to READ the context, she wants to CHANGE what reaches the assistant.

  Background:
    Given Trent's company publishes a "secure-coding" bundle, signed with the company key
    And Alice trusts the company key

  # LOCKED — the reference mechanic: pull a specific bundle from ANOTHER project's
  # repo, and its guidance flows because a trusted key signed it.
  Scenario: Alice references a bundle from the company repo and its guidance reaches her assistant
    When Alice references the company's secure-coding bundle from her project
    And Alice starts a session
    Then her assistant receives the company's secure-coding guidance, because the company key signed it

  # LOCKED — TAMPER: a trusted key signed the original, but the bytes were changed.
  # Distinct from J000200's benign held-for-review — this is a LOUD refusal (verified:
  # config.go ErrSignatureTampered, "signature that does not verify; withholding it").
  Scenario: Content Mallory altered after it was signed is refused, loudly
    Given Mallory alters the company's secure-coding bundle after it was signed
    When Alice syncs her project
    Then her assistant does not receive the altered guidance
    And Alice is warned that the content's signature does not verify

  # LOCKED — EXECUTABLES admitted: a trusted publisher's MCP servers and hooks
  # reach the engine's generated config (the settings-generation delivery, distinct
  # from context). Verified: a dedicated ExecutableTrustGate (trust_gate.go) gates
  # MCP servers + hooks with the same trust decision as content.
  Scenario: A trusted company's MCP server and hook reach the assistant's configuration
    Given the company's bundle ships an MCP server and a hook
    When Alice starts a session
    Then the MCP server appears in her assistant's configuration
    And the hook appears in her assistant's configuration

  # LOCKED — EXECUTABLES withheld: rejection beats a trusted publisher, on the
  # highest-stakes item. Absorbs "rejection beats trust" for executables.
  Scenario: A rejected executable is withheld even from a trusted company
    Given the company's bundle ships an MCP server and a hook
    And Alice has rejected the hook
    When Alice starts a session
    Then the MCP server still appears in her configuration
    But the hook is absent, because she rejected it

  # LOCKED — REVOCATION (publisher retract): the publisher withdraws ONE version
  # and it stops reaching engineers on the next sync, with a notice.
  #
  # Verified: EffectiveTrust (internal/operations/trust.go) now has a RETRACTED
  # step (a peer of REJECTED, beating the trusted-signer exemption) that
  # consults a LOCAL record — never the network — so exposure-time evaluation
  # never dials out. That local record is written by operations.syncItem
  # (internal/operations/sync.go), which now re-evaluates retraction for
  # ALREADY-INSTALLED refs too (previously it skipped them before
  # Puller.confirmRetraction ever ran), and by Puller.Pull itself on a fresh
  # pull (internal/remote/pull.go), persisting onto the lockfile entry
  # (remote.LockEntry.Retracted) either way. Formerly @wip: retraction had NO
  # effect on already-distributed content through any CLI path. Fixed;
  # was filed as taskloom task outer-shut.
  Scenario: Trent retracts a bundle and it stops reaching engineers on the next sync
    Given Alice already receives the company's secure-coding guidance
    When Trent retracts that version of the bundle
    And Alice syncs her project
    Then Alice is told the content was retracted
    And her assistant no longer receives it

  # LOCKED — REVOCATION (key compromise): revoking a KEY stops ALL content signed
  # by it at once — the response to a stolen key, distinct from retracting one
  # version. Content signed by a no-longer-trusted key falls to the unsigned/held
  # path (verified: signer.go RemoveSigner; publisher.go spec §10.2 quiet review).
  Scenario: When the company key is compromised, revoking it stops all of its content at once
    Given Alice receives several bundles the company signed with its key
    When the company key is compromised
    And Alice revokes her trust in the company key
    And Alice syncs her project
    Then her assistant no longer receives any content signed by that key
    And that content is held for her review, as if it had never been signed

  # LOCKED — the FORGERY PRIMITIVE: a decision written into the COMMITTABLE,
  # team-inherited store must be signed. No key → hard refusal, nothing written.
  # Verified: review.go resolveReviewSigner (spec §9.5, "requires one ... refuses").
  # Zero tests at any layer today.
  Scenario: Recording a team-wide review decision requires a signing key
    Given Alice has no signing key available
    When Alice tries to record a review decision into the team's shared store
    Then ctxloom refuses, because a team decision must be signed
    And nothing is written to the team store
