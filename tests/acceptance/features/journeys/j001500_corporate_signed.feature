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
  # Distinct from J000200's benign held-for-review — this is a LOUD refusal, and
  # it happens at the pull: a pull verifies what it fetched before pinning it, so
  # the altered tree is never installed at all.
  Scenario: Content Mallory altered after it was signed is refused, loudly
    Given Mallory alters the company's secure-coding bundle after it was signed
    When Alice tries to sync her project
    Then the sync refuses to install the altered bundle
    And her assistant does not receive the altered guidance
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
