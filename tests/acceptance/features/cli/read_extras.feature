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

  Scenario: ctxloom's own server reaches .mcp.json through the builtin bundle
      Given an initialized ctxloom project
      When I run "ctxloom manage hooks install"
      Then the command succeeds
      # cwd is set ONLY on ctxloom's own entry, so it tracks that one server.
      # The old token here was the _ctxloom marker, which ctxloom no longer
      # writes into the user's file — ownership rides the §9.7 record.
      And the file ".mcp.json" contains "${CLAUDE_PROJECT_DIR}"
      And the file ".mcp.json" contains "ctxloom"
