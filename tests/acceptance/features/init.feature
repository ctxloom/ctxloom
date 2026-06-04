@network
Feature: Init
  init scaffolds a project: it writes the .ctxloom config, registers the backend
  hooks, and clones the default remote for discovery. The clone reaches the
  network, so this feature is tagged @network and excluded from the hermetic run.

  Scenario: Initialize a project non-interactively
    Given an empty project directory
    When I run "ctxloom init --non-interactive --skip-launch --engine claude-code"
    Then the command succeeds
    And the file ".ctxloom/config.yaml" exists
    And the file ".claude/settings.json" exists
    And the file ".mcp.json" exists
    When I run "ctxloom config show"
    Then the command succeeds
    And the output contains "claude-code"
