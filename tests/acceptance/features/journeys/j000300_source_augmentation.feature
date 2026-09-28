@doc
Feature: Companions shape how a project is set up

  Setting up a project is not one-size-fits-all. A company standardizes how its
  projects are configured; a developer's own tooling carries its own setup steps.
  ctxloom lets both feed the setup interview — the built-in guidance PLUS every
  installed companion's setup guidance, composed together. Nothing replaces
  anything; contributions add up. An organization gets consistent onboarding
  without discarding the developer's baseline or their personal preferences.

  # COMPOSE, never replace. ResolveSetupPrompt starts from the built-in and
  # appends every installed companion's typed `init.setup_guidance` — so the
  # assertions below name the built-in marker AND each companion's, and all of
  # them have to survive together. Dropping the built-in from that composition
  # fails the two hermetic scenarios, which is what makes them worth having: a
  # first-match override would still deliver A prompt, and a scenario asserting
  # only the company's contribution could not tell the two apart.
  #
  # Adjusted under the loadout contract v2 (ugly-yodel): a source bundle no
  # longer contributes setup guidance — the well-known `agent-setup` command it
  # used to ship is gone. Setup guidance is a TYPED field of a companion's
  # loadout, so the company's and the developer's contributions now ship as
  # companions, through the same road reprise's always took.

  Scenario: Installed companions augment the setup interview, they do not replace it
    Given her company ships a companion whose loadout declares the company's onboarding steps
    And her own tooling ships a companion whose loadout declares her setup preferences
    And both companions are installed, each signed with its publisher's key
    When Alice runs the ctxloom setup
    And it launches a mock engine for the configuration interview
    Then the interview prompt the mock engine receives includes ctxloom's built-in setup guidance
    And it includes the company's onboarding steps
    And it includes her personal setup preferences

  # The switch is the negative half, and it only means something beside the
  # scenario above: the SAME two companions, installed the same way, reach the
  # prompt there and must not here. The built-in guidance is asserted too, so
  # a launch that delivered no prompt at all cannot pass as "companions off".
  Scenario: Switching companions off for one setup keeps their guidance out of the interview
    Given her company ships a companion whose loadout declares the company's onboarding steps
    And her own tooling ships a companion whose loadout declares her setup preferences
    And both companions are installed, each signed with its publisher's key
    When Alice runs the ctxloom setup with companions switched off
    Then the interview prompt the mock engine receives includes ctxloom's built-in setup guidance
    And it does not include the company's onboarding steps
    And it does not include her personal setup preferences

  # The @live twin: a REAL assistant reflects the composed guidance. The
  # hermetic row above proves the prompt was DELIVERED; only this one proves a
  # model actually read it.
  @live
  Scenario: A real assistant follows the composed setup guidance
    Given her company's companion instructs the assistant to confirm a company codeword
    And the company's companion is installed, signed with the company key
    When Alice runs the ctxloom setup and its interview launches her real assistant
    Then the assistant's setup response confirms the company codeword

  # A first-party companion contributes through the SAME path as any other:
  # its loadout is read into the same catalog, and the typed setup guidance it
  # declares is picked up by the one InitLoadouts pass. No separate companion
  # verb exists, and this scenario is what would notice if one were introduced.
  Scenario: An installed companion augments the setup interview
    Given the "reprise" companion is installed
    And it declares its own setup guidance in its loadout
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
