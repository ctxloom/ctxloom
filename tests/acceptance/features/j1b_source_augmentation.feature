@doc
Feature: Sources and companions shape how a project is set up

  Setting up a project is not one-size-fits-all. A company standardizes how its
  projects are configured; a developer's own tooling carries its own setup steps.
  ctxloom lets both feed the setup interview — the built-in guidance PLUS every
  trusted source's onboarding PLUS every installed companion's setup steps,
  composed together. Nothing replaces anything; contributions add up. An
  organization gets consistent onboarding without discarding the developer's
  baseline or their personal preferences.

  # DEPENDS ON paced-trump (augment-not-override) — RED until that lands. Today
  # a bundle's agent-setup command OVERRIDES the built-in; these assert AUGMENT.

  # PROPOSED — repo bundles augment (mock): proves DELIVERY of the composed prompt.
  Scenario: Trusted sources augment the setup interview, they do not replace it
    Given her company's repository ships an "agent-setup" command with the company's onboarding steps
    And her personal repository ships an "agent-setup" command with her own setup preferences
    And both repositories are trusted, each signed with its owner's key
    When Alice runs the ctxloom setup
    And it launches a mock engine for the configuration interview
    Then the interview prompt the mock engine receives includes ctxloom's built-in setup guidance
    And it includes the company's onboarding steps
    And it includes her personal setup preferences

  # PROPOSED — @live twin: a REAL assistant reflects the composed guidance it was given.
  @live
  Scenario: A real assistant follows the composed setup guidance
    Given her company's "agent-setup" command instructs the assistant to confirm a company codeword
    And the company repository is trusted, signed with the company key
    When Alice runs the ctxloom setup and its interview launches her real assistant
    Then the assistant's setup response confirms the company codeword

  # PROPOSED — installed companions augment (mock). A companion binary implements a
  # CLI contract that outputs setup-prompt text; ctxloom injects it. Proves DELIVERY.
  Scenario: An installed companion augments the setup interview
    Given the "reprise" companion is installed
    And it outputs its own setup guidance through ctxloom's setup-prompt CLI contract
    When Alice runs the ctxloom setup
    And it launches a mock engine for the configuration interview
    Then the interview prompt the mock engine receives includes ctxloom's built-in setup guidance
    And it includes reprise's setup guidance

  # PROPOSED — @live twin: a REAL assistant reflects the companion's injected guidance.
  @live
  Scenario: A real assistant follows an installed companion's setup guidance
    Given the "reprise" companion is installed
    And its setup guidance instructs the assistant to confirm a companion codeword
    When Alice runs the ctxloom setup and its interview launches her real assistant
    Then the assistant's setup response confirms the companion codeword
