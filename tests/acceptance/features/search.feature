Feature: Search
  search finds local content by name, tag, and type.

  Scenario: Find a fragment by name
    Given an initialized ctxloom project
    And a bundle "demo" exists
    And a fragment "testing" in bundle "demo" exists
    When I run "ctxloom search --local testing"
    Then the command succeeds
    And the output contains "testing"

  Scenario: Restrict search to fragments
    Given an initialized ctxloom project
    And a bundle "demo" exists
    And a fragment "testing" in bundle "demo" exists
    When I run "ctxloom search --local --type fragment testing"
    Then the command succeeds
    And the output contains "testing"

  Scenario: Search over MCP returns matches
    Given an initialized ctxloom project
    And a bundle "demo" exists
    And a fragment "testing" in bundle "demo" exists
    When the agent calls tool "search_content" with:
      | query | testing |
    Then the tool result contains "testing"
