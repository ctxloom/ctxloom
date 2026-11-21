# world: base
# Ported from cli/editing.feature (rev f36e8dfb2)
# Plan override (owner T29/T30, ruled 2026-11-13): an edit writes the `.md`
#   only, and its distilled form reads stale by its digest until `distill`
#   runs again; Go's "an edit distills what it saved, and a failed
#   distillation fails the edit" is reworded to that.
# NOT ported: deleted (OD-48 amended, owner 2026-11-19: tools-only is the ruled design,
#   so the MCP server serves no resources): the first scenario's agent axis
#   (`ctxloom://fragments/<name>` read by a session's agent).
@doc
Feature: Editing content
  edit opens content for modification. Flag-driven edits change metadata directly;
  editor-driven edits round-trip the content through the configured editor. With a
  marker editor the change is observable in the bundle file.

  What remains here is the ITEM nouns' editor round-trip. The bundle's own
  `edit` (flag-driven metadata and item attachment) moved to
  cli/bundle.feature, and the profile's moved to cli/profile.feature, when
  those per-noun specs took over: each is now asserted alongside the rest of
  its noun's surface rather than beside an unrelated noun's.

  # An edit writes the `.md` only (owner T29, T30): it distills nothing, so
  # it needs no model and succeeds wherever its file can be written. A
  # distilled form beside it is left as it was, and from then on reads STALE
  # by its digest (assembly serves the original) until `fragment distill`
  # runs again.
  Scenario: Editing a fragment lands the change across axes
    Given a ctxloom project with a marker editor
    And a bundle "demo" exists
    And a fragment "testing" in bundle "demo" with a fresh distilled form
    And a profile "dev" with bundle "demo"
    When I run "ctxloom fragment edit demo#fragments/testing"
    Then the command succeeds
    And the file ".ctxloom/content/bundles/v2/demo/fragments/testing.md" contains "EDITED-BY-TEST"
    And "ctxloom fragment show demo#fragments/testing" says its distilled form is stale

  Scenario: Editing a prompt lands the change in the bundle
    Given a ctxloom project with a marker editor
    And a bundle "demo" exists
    And a command "review" in bundle "demo" with a fresh distilled form
    When I run "ctxloom command edit demo#commands/review"
    Then the command succeeds
    And the file ".ctxloom/content/bundles/v2/demo/prompts/review.md" contains "EDITED-BY-TEST"
    And "ctxloom command show demo#commands/review" says its distilled form is stale
