Feature: Editing content
  edit opens content for modification. With the configured editor pinned to a
  no-op, the edit commands run non-interactively and exercise the edit path.

  Scenario: Edit bundle metadata
    Given an initialized ctxloom project
    And a bundle "demo" exists
    When I run "ctxloom bundle edit demo -d updated"
    Then the command succeeds

  Scenario: Edit a fragment
    Given an initialized ctxloom project
    And a bundle "demo" exists
    And a fragment "testing" in bundle "demo" exists
    When I run "ctxloom fragment edit demo#fragments/testing"
    Then the command succeeds

  Scenario: Edit a prompt
    Given an initialized ctxloom project
    And a bundle "demo" exists
    And a prompt "review" in bundle "demo" exists
    When I run "ctxloom prompt edit demo#prompts/review"
    Then the command succeeds

  Scenario: Edit a profile
    Given an initialized ctxloom project
    And a bundle "demo" exists
    And a profile "dev" with bundle "demo"
    When I run "ctxloom profile edit dev"
    Then the command succeeds
