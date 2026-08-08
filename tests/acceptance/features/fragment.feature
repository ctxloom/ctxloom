Feature: Fragments
  Fragments are the reusable context units inside a bundle. A fragment created on
  the CLI is listed there, searchable, written into the bundle file, and visible
  to the agent through the fragments resource.

  # Every literal on the three axes below is "testing" — the argument the
  # command line just passed. It is the YAML key in the manifest, the name in
  # the listing, and the name in the resource, so a create that stored ZERO
  # BYTES of content left all three green (Content: "" in cli.createItem):
  # this project's characteristic silent no-op, on its primary authoring verb.
  # "Add content here." is the placeholder body the create actually writes,
  # and it appears nowhere else in a freshly created project.
  Scenario: Create a fragment and see it on every axis
    Given an initialized ctxloom project
    And a bundle "demo" exists
    When I run "ctxloom fragment create demo testing"
    Then the command succeeds
    And the file ".ctxloom/content/bundles/demo.yaml" contains "testing"
    And the file ".ctxloom/content/bundles/demo.yaml" contains "Add content here."
    When I run "ctxloom fragment list"
    Then the command succeeds
    And the output contains "testing"
    When the agent reads resource "ctxloom://fragments"
    Then the resource contains "testing"

  # The MIME type is a static field on the envelope: every MCP resource
  # returning an EMPTY BODY left this green. The body is the resource.
  Scenario: Read a single fragment over MCP
    Given an initialized ctxloom project
    And a bundle "demo" exists
    And a fragment "testing" in bundle "demo" exists
    When the agent reads resource "ctxloom://fragments/testing"
    Then the resource MIME type is "text/markdown"
    And the resource contains "FRAGMENT-BODY-testing"

  Scenario: Search finds a fragment by name
    Given an initialized ctxloom project
    And a bundle "demo" exists
    And a fragment "testing" in bundle "demo" exists
    When I run "ctxloom search --local testing"
    Then the command succeeds
    And the output contains "testing"

  Scenario: Delete a fragment removes it from listing
    Given an initialized ctxloom project
    And a bundle "demo" exists
    And a fragment "testing" in bundle "demo" exists
    When I run "ctxloom fragment delete demo#fragments/testing"
    Then the command succeeds
    When I run "ctxloom fragment list"
    Then the output does not contain "testing"
