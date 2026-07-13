@doc
Feature: Setting up ctxloom on a project

  A developer's assistant is only as good as the context it is handed. Left to
  chance, every engineer's assistant behaves differently and none of them knows
  the team's standards. ctxloom's first job is to make the right context — the
  developer's own, the team's, the company's — actually reach the assistant,
  automatically, on every session, without copying anything by hand. And only
  the context the developer trusts: everything is signed, and reaches the
  assistant because a key the developer trusts signed it.

  # HARNESS CAVEAT: the init discovery / agent-setup machinery some of these
  # scenarios exercise may not all be built yet (see the source-augmentation
  # feature and its tasks). If a piece is missing, FILE the gap — do not fake a
  # scenario green or assume it exists.

  # LOCKED — trust posture only (no engine): after setup, what is wired into the
  # configuration, and why. Delivery to a running assistant is the restart scenario.
  Scenario Outline: After setup, trusted sources are part of the configuration
    Given Alice has a fresh project directory
    And her personal ctxloom repository is signed with her own key
    And her company's ctxloom repository is signed with the company key, which Alice trusts
    When Alice runs the ctxloom setup for <engine>
    And she adds her personal repository as a source
    And she adds her company's repository as a source
    Then her project is configured for <engine>
    And her personal repository's context is part of her configuration, because it is signed with her own key
    And her company repository's context is part of her configuration, because she trusts the company key

    Examples:
      | engine      |
      | claude-code |

  # LOCKED — RUNTIME delivery. The setup session CONFIGURES; a RESTART delivers the
  # configured context to a fresh agent (the setup session itself cannot see what
  # it just installed — init.go offerSessionRelaunch). Mock engine, called out.
  Scenario Outline: Setup configures the agents, then a restart delivers their context
    Given her personal ctxloom repository is signed with her own key
    And her company's ctxloom repository is signed with the company key, which Alice trusts
    When Alice runs the ctxloom setup for <engine>
    And she adds her personal and company repositories as sources
    And the setup interview composes her agents' profiles from the sources' fragments
    And ctxloom offers to restart into her newly configured session
    And Alice accepts the restart
    Then the restarted mock engine receives her personal repository's fragments
    And the restarted mock engine receives her company repository's fragments

    Examples:
      | engine      |
      | claude-code |

  # LOCKED — @live: a REAL assistant, after the restart; proves it can USE the
  # delivered context. Self-skips without credentials.
  @live
  Scenario: The restarted assistant can see every source
    Given her personal and company repositories are trusted, signed sources
    And each source carries a distinct marker phrase
    And Alice has completed setup and restarted into her configured session
    When she asks her assistant to repeat every marker phrase it can see
    Then its reply contains her personal repository's marker
    And its reply contains her company repository's marker

  # LOCKED — held content. Unsigned and untrusted-key are EQUIVALENT: both yield
  # an empty verified signer, so both are held. Verified in config.go:1408
  # (StampSigner is "" for unsigned OR unverified; the principal only when a
  # trusted key's signature verifies) and trust.go step 4.
  Scenario Outline: Content ctxloom cannot verify is held, not delivered
    Given a third-party ctxloom repository whose content is <trust_state>
    When Alice adds it as a source
    And Alice starts a session
    Then her assistant does not receive that repository's content
    And Alice is told the content is held for her review

    Examples:
      | trust_state                            |
      | unsigned                               |
      | signed with a key Alice does not trust |

  # LOCKED — illustrate the REVIEW: what Alice sees, the per-item decision, and
  # that approve and reject diverge.
  Scenario: Alice reviews held content and decides item by item
    Given two sources are held for Alice's review
    When Alice reviews the held content
    Then she is shown each held item and where it came from
    When she approves the first and rejects the second
    And Alice starts a new session
    Then her assistant receives the item she approved
    And her assistant never receives the item she rejected
