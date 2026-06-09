Feature: Profiles
  A profile names a collection of bundles. Creating one writes a profile file,
  registers it as a default, and exposes it to the agent through the profiles
  resource.

  Scenario: Create a profile across every axis
    Given an initialized ctxloom project
    And a bundle "demo" exists
    When I run "ctxloom profile create dev -b demo"
    Then the command succeeds
    And the file ".ctxloom/profiles/dev.yaml" exists
    When I run "ctxloom profile list"
    Then the command succeeds
    And the output contains "dev"
    When the agent reads resource "ctxloom://profiles"
    Then the resource contains "dev"

  Scenario: Show a profile
    Given an initialized ctxloom project
    And a bundle "demo" exists
    And a profile "dev" with bundle "demo"
    When I run "ctxloom profile show dev"
    Then the command succeeds
    And the output contains "demo"

  Scenario: Read a single profile over MCP
    Given an initialized ctxloom project
    And a bundle "demo" exists
    And a profile "dev" with bundle "demo"
    When the agent reads resource "ctxloom://profiles/dev"
    Then the resource contains "demo"

  Scenario: Delete a profile removes its file
    Given an initialized ctxloom project
    And a bundle "demo" exists
    And a profile "dev" with bundle "demo"
    When I run "ctxloom profile delete dev"
    Then the command succeeds
    And the file ".ctxloom/profiles/dev.yaml" does not exist

  Scenario: Manage the default profile set
    # The default set is a list: defaults can be added, shown, and unset.
    Given an initialized ctxloom project
    And a bundle "demo" exists
    And a profile "dev" with bundle "demo"
    When I run "ctxloom profile default dev"
    Then the command succeeds
    When I run "ctxloom profile default"
    Then the command succeeds
    And the output contains "dev"
    When I run "ctxloom profile default --unset dev"
    Then the command succeeds
    When I run "ctxloom profile default"
    Then the output contains "No default profile set."
