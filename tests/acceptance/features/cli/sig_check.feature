Feature: --disable-sig-check — waiving signature verification for one invocation

  Covers: the persistent `--disable-sig-check` flag and its
  CTXLOOM_DISABLE_SIG_CHECK environment switch.

  Signing is mandatory: remote content nobody trusted the signer of is
  withheld until a human reviews it. A developer iterating on their own
  bundles pays the sign/re-sign cycle on every edit, so this switch waives the
  SIGNATURE step — and only that — for the invocation that carries it. It is
  carried by a task-runner recipe, never by config: there is no config key, so
  no project can make every later run silently unsigned. Signing itself is
  untouched; only verification is waived, by name.

  It is not a trust-everything switch. A human's rejection, a publisher's
  retraction and an unreadable approvals store all decide BEFORE the
  signature step, so each still refuses with the switch on.

  The same bundle from an unsigned, never-reviewed publisher serves every
  scenario, borrowed from the trust-surface reference: one of each kind of
  thing a bundle can ship.

  # The claim itself: unsigned content is refused without the switch and
  # delivered with it, in the same project, so neither half can pass for the
  # other's reason.
  Scenario Outline: An unsigned bundle is withheld, and reaches the assistant only when signature verification is disabled
    Given a bundle from an unsigned, never-reviewed publisher ships one of each: a fragment, a command, an MCP server, and a hook
    When Alice starts a session
    Then the <element> is absent from her assistant's delivered surface
    When Alice starts a session with signature verification disabled
    Then the <element> is present in her assistant's delivered surface

    Examples:
      | element    |
      | hook       |
      | MCP server |
      | command    |
      | fragment   |

  # The waiver is not a trust-everything switch: a human's rejection decides
  # before the signature step is ever reached. The command alongside it is the
  # control — it proves the waiver was in effect for this very session.
  Scenario: A rejection still holds with signature verification disabled
    Given a bundle from an unsigned, never-reviewed publisher ships one of each: a fragment, a command, an MCP server, and a hook
    When Alice rejects the fragment
    And Alice starts a session with signature verification disabled
    Then the fragment is absent from her assistant's delivered surface
    And the command is present in her assistant's delivered surface

  Scenario: The environment switch disables signature verification for the invocation that carries it
    Given a bundle from an unsigned, never-reviewed publisher ships one of each: a fragment, a command, an MCP server, and a hook
    When Alice starts a session
    Then the fragment is absent from her assistant's delivered surface
    When Alice starts a session with signature verification disabled through the environment
    Then the fragment is present in her assistant's delivered surface

  # Owner ruling 2026-10-02: an installed tree pulled SIGNED and then edited
  # locally is accepted under the switch. The owner accepted that the switch
  # then also hides tampering of an installed signed tree, on the condition
  # that every surface says so: the session without the switch refuses it, and
  # doctor and the dry run with the switch name the bundle they accepted.
  Scenario: An installed signed bundle edited after its pull reaches the assistant only with signature verification disabled, and is named
    Given Trent's company publishes a "secure-coding" bundle, signed with the company key
    And Alice trusts the company key
    And Alice references the company's secure-coding bundle from her project
    When Alice edits her installed copy of the company's guidance
    And Alice starts a session
    Then her assistant does not receive her edited guidance
    When Alice starts a session with signature verification disabled
    Then her assistant receives her edited guidance
    And doctor, with signature verification disabled, names the company's bundle as accepted although edited
    And the dry run, with signature verification disabled, names the company's bundle as accepted although edited

  # Owner ruling 2026-10-02: the session's OWN ctxloom children share its
  # waiver, so a session decides alike throughout. The launch puts the session
  # carrier on the engine's own environment — the one every hook it runs
  # inherits — and the next session without the switch puts nothing there.
  Scenario: A waived session's engine is handed the waiver for its hooks; an enforced one is not
    Given an initialized ctxloom project
    And a bundle "demo" exists
    And a fragment "testing" in bundle "demo" exists
    And a profile "dev" with bundle "demo"
    And the mock LLM responds "MOCK-REPLY"
    When I run "ctxloom --disable-sig-check run --one-shot --profile dev hello"
    Then the command succeeds
    And the mock recorded input contains "CTXLOOM_SESSION_DISABLE_SIG_CHECK"
    When I run "ctxloom run --one-shot --profile dev hello"
    Then the command succeeds
    And the mock recorded input does not contain "CTXLOOM_SESSION_DISABLE_SIG_CHECK"

  # The carrier reaches whatever the engine starts, its shell included. A
  # ctxloom hook honours it; a `ctxloom run` or `ctxloom doctor` typed in that
  # shell is a new invocation and verifies — but names the waiver of the
  # session it runs in, so it is never silent.
  Scenario: A hook honours the session's waiver; a command typed in the session's shell verifies, and names the waiver
    Given an initialized ctxloom project
    And a bundle "demo" exists
    And a fragment "testing" in bundle "demo" exists
    And a profile "dev" with bundle "demo"
    And the mock LLM responds "MOCK-REPLY-NEVER-SENT"
    And the environment variable "CTXLOOM_SESSION_DISABLE_SIG_CHECK" is set to "1"
    When I run "ctxloom hook hud" with input:
      """
      {"model":{"display_name":"Opus 5"},"context_window":{"used_percentage":42}}
      """
    Then the command succeeds
    And the output contains "signature verification is DISABLED for this invocation"
    When I run "ctxloom run --dry-run --format json --profile dev hello"
    Then the command succeeds
    And the output reports "signature_check" as "enforced"
    And the output reports "session_signature_check" as "disabled"
    When I run "ctxloom --format json doctor"
    Then the command succeeds
    And the JSON output array "checks" contains an object whose "marker" is "DOCTOR-CHECK-SIG-CHECK-e2" and whose "status" is "warn"
