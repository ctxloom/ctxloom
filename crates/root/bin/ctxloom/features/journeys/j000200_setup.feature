# world: base
# Ported from journeys/j000200_setup.feature (rev f36e8dfb2)
# NOT ported: the scenarios tagged @pending: the @live outline needs a real
#   engine.
@doc
Feature: Setting up ctxloom on a project

  A developer's assistant is only as good as the context it is handed. Left to
  chance, every engineer's assistant behaves differently and none of them knows
  the team's standards. Alice sets ctxloom up on her project once and adds the
  repositories whose context she trusts: her own and her company's. From then
  on, every session she starts through ctxloom carries that context in its
  session home, without her copying anything by hand. Adding a repository is
  the trust act: what she adds is what reaches her assistant.

  Scenario: Alice sets up ctxloom and the context she trusts reaches her assistant
    # new: Go's two by-hand scenarios, as one flow that starts with `ctxloom init`.
    Given Alice has a project that has never used ctxloom
    And Alice has a personal ctxloom repository
    And her company has a ctxloom repository
    When Alice sets ctxloom up in her project and adds her personal and company repositories as remotes:
      """
      ctxloom init --remote https://github.com/alice/ctxloom-content
      ctxloom remote create acme https://github.com/acme/ctxloom-content
      """
    And she puts each repository's bundle in her profile, pulls them, and starts a session:
      """
      ctxloom profile edit default --add-bundle personal/house-rules --add-bundle acme/standards
      ctxloom deps pull
      ctxloom run
      """
    Then her assistant receives her personal repository's content
    And her assistant receives her company repository's content

  # A run installs what her profiles reference and is not installed yet
  # (`sync.on_run: missing`, the default; owner rulings 59, 60), so a bundle
  # she has just put in her profile reaches her assistant without a
  # `ctxloom deps pull` first. `sync.on_run: none` leaves the pulling to her.
  Scenario: A run installs the bundle she just put in her profile
    # new: owner rulings 59, 60.
    Given Alice has a project that has never used ctxloom
    And her company has a ctxloom repository
    When Alice sets ctxloom up, adds her company's repository, puts its bundle in her profile and starts a session without pulling:
      """
      ctxloom init
      ctxloom remote create acme https://github.com/acme/ctxloom-content
      ctxloom profile edit default --add-bundle acme/standards
      ctxloom run
      """
    Then her assistant receives her company repository's content

  # On a terminal, a first init asks two things itself before the interview:
  # where her sessions' outputs go, suggesting `ctxloom/` in the project and
  # saying it is not gitignored, so what they leave there can be committed
  # (owner rulings 89, 106); then whether conditional guidance (a fragment
  # with a premise) loads only when it applies, the suggestion, which keeps
  # the context smaller, or always (`config.premised`, owner ruling of
  # 2026-11-12). Then setup interviews her. The interview runs in a session that
  # cannot see what it has just configured, so ctxloom offers to restart her
  # into the configured one. The offer is [Y/n]: a bare Enter accepts it; n or no
  # declines it, and so does a closed input (no one answered).
  Scenario: Setup configures the agents, then a restart delivers their context
    # substitution: one engine, no Outline; the Thens say what her restarted assistant receives.
    Given Alice has a personal ctxloom repository
    And her company has a ctxloom repository
    When Alice sets ctxloom up at a terminal, naming her personal repository:
      """
      ctxloom init --remote https://github.com/alice/ctxloom-content
      """
    And she keeps the output directory init suggests
    And she keeps init's suggestion that conditional guidance loads only when it applies
    And in the setup interview she adds her company's repository as a remote
    And the setup interview composes her agents' profiles from the sources' fragments
    And ctxloom offers to restart into her newly configured session
    And Alice accepts the restart
    Then her restarted assistant receives her personal repository's content
    And her restarted assistant receives her company repository's content
    And the output contains "Session outputs go to ctxloom/ (not gitignored: they can be committed)"
    And the output contains "Conditional guidance loads only when it applies (config.premised: on-demand)"
    And the file ".ctxloom/config.yaml" contains "premised: on-demand"

  # She may instead have every fragment load at launch, premised ones too:
  # init writes her answer, and the context her restarted session is handed
  # carries the premised fragment itself and no index to ask from.
  Scenario: She chooses at init that conditional guidance always loads
    # new: owner ruling of 2026-11-12.
    Given Alice has a personal ctxloom repository
    And her personal repository carries a fragment with a premise
    When Alice sets ctxloom up at a terminal, naming her personal repository:
      """
      ctxloom init --remote https://github.com/alice/ctxloom-content
      """
    And she keeps the output directory init suggests
    And she answers that conditional guidance always loads
    And the setup interview composes her agent's profile from her personal repository
    And ctxloom offers to restart into her newly configured session
    And Alice accepts the restart
    Then her restarted assistant receives her personal repository's content
    And her restarted assistant receives her personal repository's premised fragment, and no premise index
    And the output contains "Conditional guidance always loads (config.premised: always)"
    And the file ".ctxloom/config.yaml" contains "premised: always"

  # With a real assistant: it can use what it was handed.
  @live @pending
  Scenario Outline: The restarted <engine> assistant can see every source
    # substitution: told as the user's flow, with the commands she runs.
    Given Alice has personal and company ctxloom repositories, served by a real <engine> assistant
    And each source carries a distinct marker phrase
    And Alice has completed setup and restarted into her configured session
    When she asks her assistant to repeat every marker phrase it can see
    Then its reply contains her personal repository's marker
    And its reply contains her company repository's marker

    Examples:
      | engine      |
      | claude-code |
      | opencode    |
