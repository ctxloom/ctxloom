Feature: Shell completion
  Smoke coverage for the always-scriptable utility command that has no noun of
  its own.

  The version surface that used to share this file moved to
  cli/version.feature, where all three of its spellings are cross-checked
  against each other.

  Scenario: Generate a shell completion script
    Given an initialized ctxloom project
    When I run "ctxloom completion bash"
    Then the command succeeds
    And the output contains "complete"
