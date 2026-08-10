Feature: Bundles
  Bundles are the on-disk unit of context. Creating one writes a YAML manifest
  the CLI lists and whose fragments the agent can see.

  Scenario: Create a bundle and list it
    Given an initialized ctxloom project
    When I run "ctxloom bundle create demo"
    Then the command succeeds
    And the file ".ctxloom/content/bundles/demo.yaml" exists
    And the file ".ctxloom/content/bundles/demo.yaml" is valid YAML
    When I run "ctxloom bundle list"
    Then the command succeeds
    And the output contains "demo"

  # "the output contains demo" is satisfied by the `Bundle: demo` header alone,
  # so a render that stopped right after the header — suppressing every
  # section, which IS the bundle's contents — passed. `bundle create` seeds one
  # example fragment and one example command; the sections that describe them
  # are what this scenario is named after.
  Scenario: Show a bundle's contents
    Given an initialized ctxloom project
    And a bundle "demo" exists
    When I run "ctxloom bundle show demo"
    Then the command succeeds
    And the output contains "Bundle: demo"
    And the output contains "Fragments (1):"
    And the output contains "- example [example] (no_distill)"
    And the output contains "Commands (1):"
    And the output contains "Example prompt"

  # `bundle view <name>` dumps the whole bundle YAML, and "testing" is the
  # fragment's YAML KEY — so re-marshalling through a struct that omits each
  # item's content (or emitting only Bundle.Refs()) left this green while view
  # showed nothing of the fragment it is named after. bundle view appears in
  # exactly one scenario in the whole suite, so nothing else caught it. The
  # marker exists only inside the fragment's stored bytes.
  Scenario: View a bundle shows its fragment
    Given an initialized ctxloom project
    And a bundle "demo" exists
    And a fragment "testing" in bundle "demo" exists
    When I run "ctxloom bundle view demo"
    Then the command succeeds
    And the output contains "testing"
    And the output contains "FRAGMENT-BODY-testing"

  Scenario: Holding a local bundle reports it is not lockfile-tracked
    Given an initialized ctxloom project
    And a bundle "demo" exists
    When I run "ctxloom bundle hold demo"
    Then the command succeeds
    And the output contains "nothing to hold"
    When I run "ctxloom bundle unhold demo"
    Then the command succeeds

  # Bare `remove` is a preview: it must name what it would destroy AND leave
  # the bundle in place. A guard that quietly destroyed anyway would still
  # pass a scenario that only checked exit code or the report text — the
  # file-exists check is what actually catches that.
  Scenario: Bare bundle remove reports and destroys nothing
    Given an initialized ctxloom project
    And a bundle "demo" exists
    When I run "ctxloom bundle remove demo"
    Then the command succeeds
    And the output contains "Nothing was removed"
    And the output contains "--yes"
    And the file ".ctxloom/content/bundles/demo.yaml" exists

  Scenario: Remove a bundle removes its file
    Given an initialized ctxloom project
    And a bundle "demo" exists
    When I run "ctxloom bundle remove demo --yes"
    Then the command succeeds
    And the file ".ctxloom/content/bundles/demo.yaml" does not exist
