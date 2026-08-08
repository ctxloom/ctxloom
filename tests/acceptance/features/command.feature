Feature: Commands
  Commands live in bundles alongside fragments. A command created on the
  CLI is listed there and exposed to the agent through the commands resource.

  # Same hazard as fragment.feature's create scenario: "review" is the
  # argument the command line passed, and is the YAML key, the listed name,
  # and the resource entry alike — so Content: "" in cli.createItem stored no
  # body at all and every axis stayed green. "Add content here." is the
  # placeholder body the create actually writes.
  Scenario: Create a command and see it listed and exposed
    Given an initialized ctxloom project
    And a bundle "demo" exists
    When I run "ctxloom command create demo review"
    Then the command succeeds
    And the file ".ctxloom/content/bundles/demo.yaml" contains "review"
    And the file ".ctxloom/content/bundles/demo.yaml" contains "Add content here."
    When I run "ctxloom command list"
    Then the command succeeds
    And the output contains "review"
    When the agent reads resource "ctxloom://commands"
    Then the resource contains "review"

  # The MIME type is a static field on the envelope: every MCP resource
  # returning an EMPTY BODY left this green. The body is the resource.
  Scenario: Read a single command over MCP
    Given an initialized ctxloom project
    And a bundle "demo" exists
    And a command "review" in bundle "demo" exists
    When the agent reads resource "ctxloom://commands/review"
    Then the resource MIME type is "text/markdown"
    And the resource contains "COMMAND-BODY-review"

  Scenario: Delete a command removes it from listing
    Given an initialized ctxloom project
    And a bundle "demo" exists
    And a command "review" in bundle "demo" exists
    When I run "ctxloom command delete demo#commands/review"
    Then the command succeeds
    When I run "ctxloom command list"
    Then the output does not contain "review"
