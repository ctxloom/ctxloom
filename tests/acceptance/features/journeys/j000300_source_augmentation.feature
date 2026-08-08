@doc
Feature: Sources and companions shape how a project is set up

  Setting up a project is not one-size-fits-all. A company standardizes how its
  projects are configured; a developer's own tooling carries its own setup steps.
  ctxloom lets both feed the setup interview — the built-in guidance PLUS every
  trusted source's onboarding PLUS every installed companion's setup steps,
  composed together. Nothing replaces anything; contributions add up. An
  organization gets consistent onboarding without discarding the developer's
  baseline or their personal preferences.

  # COMPOSE, never replace. ResolveSetupPrompt starts from the built-in and
  # appends every installed "agent-setup" command it finds — so the assertions
  # below name the built-in marker AND each source's, and all of them have to
  # survive together. Dropping the built-in from that composition fails the two
  # hermetic scenarios, which is what makes them worth having: a first-match
  # override would still deliver A prompt, and a scenario asserting only the
  # company's contribution could not tell the two apart.

  Scenario: Trusted sources augment the setup interview, they do not replace it
    Given her company's repository ships an "agent-setup" command with the company's onboarding steps
    And her personal repository ships an "agent-setup" command with her own setup preferences
    And both repositories are trusted, each signed with its owner's key
    When Alice runs the ctxloom setup
    And it launches a mock engine for the configuration interview
    Then the interview prompt the mock engine receives includes ctxloom's built-in setup guidance
    And it includes the company's onboarding steps
    And it includes her personal setup preferences

  # The @live twin: a REAL assistant reflects the composed guidance. The
  # hermetic row above proves the prompt was DELIVERED; only this one proves a
  # model actually read it.
  @live
  Scenario: A real assistant follows the composed setup guidance
    Given her company's "agent-setup" command instructs the assistant to confirm a company codeword
    And the company repository is trusted, signed with the company key
    When Alice runs the ctxloom setup and its interview launches her real assistant
    Then the assistant's setup response confirms the company codeword

  # A companion contributes through the SAME path as a repo bundle: its loadout
  # is seeded into the same bundle loader, so an "agent-setup" command it ships
  # is picked up by the one ListAllCommands loop. No separate companion verb
  # exists, and this scenario is what would notice if one were introduced.
  Scenario: An installed companion augments the setup interview
    Given the "reprise" companion is installed
    And it outputs its own setup guidance through ctxloom's setup-prompt CLI contract
    When Alice runs the ctxloom setup
    And it launches a mock engine for the configuration interview
    Then the interview prompt the mock engine receives includes ctxloom's built-in setup guidance
    And it includes reprise's setup guidance

  # The @live twin for the companion path, for the same reason as above.
  @live
  Scenario: A real assistant follows an installed companion's setup guidance
    Given the "reprise" companion is installed
    And its setup guidance instructs the assistant to confirm a companion codeword
    When Alice runs the ctxloom setup and its interview launches her real assistant
    Then the assistant's setup response confirms the companion codeword
