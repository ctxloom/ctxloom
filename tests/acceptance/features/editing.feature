Feature: Editing content
  edit opens content for modification. Flag-driven edits change metadata directly;
  editor-driven edits round-trip the content through the configured editor. With a
  marker editor the change is observable in the bundle file and across MCP.

  Scenario: Edit bundle metadata via flag
    Given an initialized ctxloom project
    And a bundle "demo" exists
    When I run "ctxloom bundle edit demo -d updated-desc"
    Then the command succeeds
    And the file ".ctxloom/content/bundles/demo.yaml" contains "updated-desc"

  Scenario: Editing a fragment lands the change across axes
    Given a ctxloom project with a marker editor
    And a bundle "demo" exists
    And a fragment "testing" in bundle "demo" exists
    When I run "ctxloom fragment edit demo#fragments/testing"
    Then the command succeeds
    And the file ".ctxloom/content/bundles/demo.yaml" contains "EDITED-BY-TEST"
    When the agent reads resource "ctxloom://fragments/testing"
    Then the resource contains "EDITED-BY-TEST"

  Scenario: Editing a prompt lands the change in the bundle
    Given a ctxloom project with a marker editor
    And a bundle "demo" exists
    And a command "review" in bundle "demo" exists
    When I run "ctxloom command edit demo#commands/review"
    Then the command succeeds
    And the file ".ctxloom/content/bundles/demo.yaml" contains "EDITED-BY-TEST"

  # A profile is a structured document, so the append-a-line marker editor the
  # scenarios above use would leave invalid YAML behind — which is why this
  # scenario had no observable effect to assert and settled for the exit code,
  # passing even against an edit path that returned immediately and did
  # nothing. The description-rewriting editor keeps the document valid, so the
  # round-trip is assertable on the file AND on the read-back surface.
  Scenario: Edit a profile
    Given a ctxloom project with a description-rewriting editor
    And a bundle "demo" exists
    And a profile "dev" with bundle "demo"
    When I run "ctxloom profile edit dev"
    Then the command succeeds
    And the output contains "Updated profile"
    And the file ".ctxloom/profiles/dev.yaml" contains "EDITED-BY-TEST"
    When I run "ctxloom profile show dev"
    Then the command succeeds
    And the output contains "EDITED-BY-TEST"
