Feature: Prompts
  Prompts (skills) live in bundles alongside fragments. A prompt created on the
  CLI is listed there and exposed to the agent through the prompts resource.

  Scenario: Create a prompt and see it listed and exposed
    Given an initialized ctxloom project
    And a bundle "demo" exists
    When I run "ctxloom prompt create demo review"
    Then the command succeeds
    And the file ".ctxloom/cache/bundles/demo.yaml" contains "review"
    When I run "ctxloom prompt list"
    Then the command succeeds
    And the output contains "review"
    When the agent reads resource "ctxloom://prompts"
    Then the resource contains "review"

  Scenario: Read a single prompt over MCP
    Given an initialized ctxloom project
    And a bundle "demo" exists
    And a prompt "review" in bundle "demo" exists
    When the agent reads resource "ctxloom://prompts/review"
    Then the resource MIME type is "text/markdown"

  Scenario: Delete a prompt removes it from listing
    Given an initialized ctxloom project
    And a bundle "demo" exists
    And a prompt "review" in bundle "demo" exists
    When I run "ctxloom prompt delete demo#prompts/review"
    Then the command succeeds
    When I run "ctxloom prompt list"
    Then the output does not contain "review"
