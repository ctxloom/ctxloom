Feature: Editing content
  edit opens content for modification. Flag-driven edits change metadata directly;
  editor-driven edits round-trip the content through the configured editor. With a
  marker editor the change is observable in the bundle file and across MCP.

  What remains here is the ITEM nouns' editor round-trip. The bundle's own
  `edit` (flag-driven metadata and item attachment) moved to
  cli/bundle.feature, and the profile's moved to cli/profile.feature, when
  those per-noun specs took over: each is now asserted alongside the rest of
  its noun's surface rather than beside an unrelated noun's.

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

