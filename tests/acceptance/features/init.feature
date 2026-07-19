@network
Feature: Init
  init scaffolds a project: it writes the .ctxloom config, registers the backend
  hooks, and clones the default remote for discovery. The clone reaches the
  network, so this feature is tagged @network and excluded from the hermetic run.

  Scenario: The manage init form scaffolds identically to the alias
    Given an empty project directory
    When I run "ctxloom manage init --non-interactive --skip-launch --engine claude-code"
    Then the command succeeds
    And the file ".ctxloom/config.yaml" exists
