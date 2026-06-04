Feature: Additional read and configuration commands
  Coverage for the remaining show/search/modify/registration commands.

  Scenario: Show a fragment's content
    Given an initialized ctxloom project
    And a bundle "demo" exists
    And a fragment "testing" in bundle "demo" exists
    When I run "ctxloom fragment show demo#fragments/testing"
    Then the command succeeds
    And the output contains "testing"

  Scenario: Search fragments by name
    Given an initialized ctxloom project
    And a bundle "demo" exists
    And a fragment "testing" in bundle "demo" exists
    When I run "ctxloom fragment search testing"
    Then the command succeeds
    And the output contains "testing"

  Scenario: Show a prompt's content
    Given an initialized ctxloom project
    And a bundle "demo" exists
    And a prompt "review" in bundle "demo" exists
    When I run "ctxloom prompt show demo#prompts/review"
    Then the command succeeds
    And the output contains "review"

  Scenario: Modify a profile's description
    Given an initialized ctxloom project
    And a bundle "demo" exists
    And a profile "dev" with bundle "demo"
    When I run "ctxloom profile modify dev -d updated"
    Then the command succeeds

  Scenario: Toggle MCP auto-registration
    Given an initialized ctxloom project
    When I run "ctxloom mcp auto-register --disable"
    Then the command succeeds
    When I run "ctxloom mcp auto-register"
    Then the command succeeds
