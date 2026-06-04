Feature: Configuration
  manage config reads the project configuration. Sections are addressable individually.

  Scenario: Show the full configuration
    Given an initialized ctxloom project
    When I run "ctxloom manage config show"
    Then the command succeeds
    And the output matches "."

  Scenario: Get the llm section
    Given an initialized ctxloom project
    When I run "ctxloom manage config get llm"
    Then the command succeeds
    And the output contains "claude-code"

  Scenario: Get the profiles section reflects a created profile
    Given an initialized ctxloom project
    And a bundle "demo" exists
    And a profile "dev" with bundle "demo"
    When I run "ctxloom manage config get profiles"
    Then the command succeeds
    And the output contains "dev"

  Scenario: An unknown section is rejected
    Given an initialized ctxloom project
    When I run "ctxloom manage config get nonsense"
    Then the command fails
    And the output contains "Available"
