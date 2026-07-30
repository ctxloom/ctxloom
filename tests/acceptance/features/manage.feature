Feature: Manage the project harness
  manage installs, inspects, and removes ctxloom's integration with a project:
  the .ctxloom scaffold, backend hooks/statusline, MCP registration, and the
  .gitignore entries that keep ctxloom's private state out of source control.

  Scenario: Install wires ctxloom into an empty project
    Given an empty project directory
    When I run "ctxloom manage install --engine claude-code"
    Then the command succeeds
    And the file ".ctxloom/config.yaml" exists
    And the file ".claude/settings.json" exists
    And the file ".mcp.json" exists
    And the file ".gitignore" contains "ctxloom"

  Scenario: Status reports the wired harness
    Given an initialized ctxloom project
    When I run "ctxloom manage hooks install"
    Then the command succeeds
    When I run "ctxloom manage status"
    Then the command succeeds
    And the output contains "claude-code"

  Scenario: Uninstall strips the harness but keeps .ctxloom
    Given an initialized ctxloom project
    And I run "ctxloom manage hooks install"
    When I run "ctxloom manage uninstall"
    Then the command succeeds
    And the file ".ctxloom/config.yaml" exists
    And the file ".claude/settings.json" does not contain "ctxloom hook"

  Scenario: Hooks can be installed, inspected, and actually removed
    Given an initialized ctxloom project
    When I run "ctxloom manage hooks install"
    Then the command succeeds
    And the file ".claude/settings.json" contains "ctxloom hook"
    When I run "ctxloom manage hooks status"
    Then the command succeeds
    When I run "ctxloom manage hooks uninstall"
    Then the command succeeds
    And the file ".claude/settings.json" does not contain "ctxloom hook"

  Scenario: Re-applying hooks does not duplicate them
    Given an initialized ctxloom project
    When I run "ctxloom manage hooks install"
    Then the command succeeds
    When I run "ctxloom manage hooks install"
    Then the command succeeds
    And the file ".claude/settings.json" contains "ctxloom hook session-bind" exactly 1 times

  Scenario: Gitignore install excludes ctxloom's private state
    Given an initialized ctxloom project
    When I run "ctxloom manage gitignore install"
    Then the command succeeds
    And the file ".gitignore" contains ".ctxloom/ephemeral/"

  Scenario: Config init scaffolds a config in a bare project
    Given an empty project directory
    When I run "ctxloom manage config init --engine claude-code"
    Then the command succeeds
    And the file ".ctxloom/config.yaml" exists

  Scenario: Statusline can be disabled and re-enabled
    Given an initialized ctxloom project
    When I run "ctxloom manage statusline uninstall"
    Then the command succeeds
    When I run "ctxloom manage hooks install"
    Then the command succeeds
    And the file ".claude/settings.json" does not contain "hook hud"
    When I run "ctxloom manage statusline install"
    Then the command succeeds
    When I run "ctxloom manage hooks install"
    Then the command succeeds
    And the file ".claude/settings.json" contains "hook hud"

  # A rewrite of settings.json must only ADD ctxloom's own keys: whatever the
  # user wrote by hand comes back byte-for-byte, including numbers no float64
  # can hold exactly. The failure this pins reported success and altered the
  # file, so the assertion is on the persisted digits.
  Scenario: Installing hooks preserves the exact numbers the user wrote by hand
    Given an initialized ctxloom project
    And the project already has the file ".claude/settings.json":
      """
      {
        "awkwardNumber": 1234567890123456789,
        "nested": { "id": -9223372036854775808 },
        "permissions": { "allow": ["Read"], "quota": 18446744073709551615 }
      }
      """
    When I run "ctxloom manage hooks install"
    Then the command succeeds
    And the file ".claude/settings.json" contains "ctxloom hook"
    And the file ".claude/settings.json" contains "1234567890123456789"
    And the file ".claude/settings.json" contains "-9223372036854775808"
    And the file ".claude/settings.json" contains "18446744073709551615"
    And the file ".claude/settings.json" contains "Read"
