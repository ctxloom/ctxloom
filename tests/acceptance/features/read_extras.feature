Feature: Additional read and configuration commands
  Coverage for the remaining show/search/modify/registration commands.

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

  Scenario: Search fragments by name
    Given an initialized ctxloom project
    And a bundle "demo" exists
    And a fragment "testing" in bundle "demo" exists
    When I run "ctxloom search --type fragment testing"
    Then the command succeeds
    And the output contains "testing"

  Scenario: Show a prompt's content
    Given an initialized ctxloom project
    And a bundle "demo" exists
    And a command "review" in bundle "demo" exists
    When I run "ctxloom command show demo#commands/review"
    Then the command succeeds
    And the output contains "review"
    And the output contains "COMMAND-BODY-review"

  Scenario: Modify a profile's description
    Given an initialized ctxloom project
    And a bundle "demo" exists
    And a profile "dev" with bundle "demo"
    When I run "ctxloom profile modify dev -d updated-desc"
    Then the command succeeds
    When I run "ctxloom profile show dev"
    Then the output contains "updated-desc"

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
