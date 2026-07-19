Feature: Search
  search finds local content by name, tag, and type.

  Scenario: Restrict search to fragments
    Given an initialized ctxloom project
    And a bundle "demo" exists
    And a fragment "testing" in bundle "demo" exists
    When I run "ctxloom search --local --type fragment testing"
    Then the command succeeds
    And the output contains "testing"
