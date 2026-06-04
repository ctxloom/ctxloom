Feature: Bundles
  Bundles are the on-disk unit of context. Creating one writes a YAML manifest
  the CLI lists and whose fragments the agent can see.

  Scenario: Create a bundle and list it
    Given an initialized ctxloom project
    When I run "ctxloom bundle create demo"
    Then the command succeeds
    And the file ".ctxloom/cache/bundles/demo.yaml" exists
    And the file ".ctxloom/cache/bundles/demo.yaml" is valid YAML
    When I run "ctxloom bundle list"
    Then the command succeeds
    And the output contains "demo"

  Scenario: Show a bundle's contents
    Given an initialized ctxloom project
    And a bundle "demo" exists
    When I run "ctxloom bundle show demo"
    Then the command succeeds
    And the output contains "demo"

  Scenario: View a bundle shows its fragment
    Given an initialized ctxloom project
    And a bundle "demo" exists
    And a fragment "testing" in bundle "demo" exists
    When I run "ctxloom bundle view demo"
    Then the command succeeds
    And the output contains "testing"

  Scenario: Pinning a local bundle reports it is not lockfile-tracked
    Given an initialized ctxloom project
    And a bundle "demo" exists
    When I run "ctxloom bundle pin demo"
    Then the command succeeds
    And the output contains "nothing to pin"
    When I run "ctxloom bundle unpin demo"
    Then the command succeeds

  Scenario: Delete a bundle removes its file
    Given an initialized ctxloom project
    And a bundle "demo" exists
    When I run "ctxloom bundle delete demo -f"
    Then the command succeeds
    And the file ".ctxloom/cache/bundles/demo.yaml" does not exist
