Feature: Harp identifiers
  harp generates human-readable adjective-noun identifiers used for sessions and
  tasks.

  Scenario: Generate a default three-word identifier
    Given an initialized ctxloom project
    When I run "ctxloom harp"
    Then the command succeeds
    And the output matches "^[a-z]+-[a-z]+-[a-z]+"

  Scenario: Generate a two-word identifier
    Given an initialized ctxloom project
    When I run "ctxloom harp -c 2"
    Then the command succeeds
    And the output matches "^[a-z]+-[a-z]+\s*$"

  Scenario: Generate several identifiers
    Given an initialized ctxloom project
    When I run "ctxloom harp -n 3"
    Then the command succeeds
    And the output matches "(?s)([a-z-]+\n){3}"
