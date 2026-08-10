Feature: Additional read and configuration commands
  Coverage for the remaining show/registration commands.

  The search and profile-modify scenarios that used to live here moved into
  the per-noun specs (cli/search.feature, cli/profile.feature) when those took
  over their nouns. What remains is the two item-content reads and the MCP
  auto-registration round-trip, none of which has a per-noun home yet.

  # "the output contains <name>" asserted nothing about the CONTENT: `show`
  # prints the item name as a header, echoed straight from the argument, so
  # blanking the stored body left this green. The fixture seeds a marker that
  # exists nowhere but inside the item's bytes.
  Scenario: Show a fragment's content
    Given an initialized ctxloom project
    And a bundle "demo" exists
    And a fragment "testing" in bundle "demo" exists
    When I run "ctxloom fragment show demo#fragments/testing"
    Then the command succeeds
    And the output contains "testing"
    And the output contains "FRAGMENT-BODY-testing"

  Scenario: Show a prompt's content
    Given an initialized ctxloom project
    And a bundle "demo" exists
    And a command "review" in bundle "demo" exists
    When I run "ctxloom command show demo#commands/review"
    Then the command succeeds
    And the output contains "review"
    And the output contains "COMMAND-BODY-review"

  Scenario: Disabling MCP auto-registration removes the server on re-apply
    Given an initialized ctxloom project
    When I run "ctxloom manage hooks install"
    Then the command succeeds
    And the file ".mcp.json" contains "ctxloom-auto"
    When I run "ctxloom mcp unregister"
    Then the command succeeds
    When I run "ctxloom manage hooks install"
    Then the command succeeds
    And the file ".mcp.json" does not contain "ctxloom-auto"
    When I run "ctxloom mcp register"
    Then the command succeeds
    When I run "ctxloom manage hooks install"
    Then the command succeeds
    And the file ".mcp.json" contains "ctxloom"
