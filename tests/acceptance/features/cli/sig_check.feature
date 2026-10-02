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
