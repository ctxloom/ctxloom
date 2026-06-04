Feature: Version and completion
  Smoke coverage for the always-scriptable utility commands.

  Scenario: Print the version
    Given an initialized ctxloom project
    When I run "ctxloom version"
    Then the command succeeds
    And the output matches "."

  Scenario: Generate a shell completion script
    Given an initialized ctxloom project
    When I run "ctxloom completion bash"
    Then the command succeeds
    And the output contains "complete"
