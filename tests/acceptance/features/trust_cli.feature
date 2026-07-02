Feature: Trust posture CLI
  Per-item grants (`trust`), sticky blocks (`blacklist`), and whole-bundle
  postures (`bundle trust`/`untrust`) manage what content the agent sees.

  Scenario: Bundle trust posture toggles
    Given an initialized ctxloom project
    And a bundle "demo" exists
    When I run "ctxloom bundle trust demo"
    Then the command succeeds
    And the output contains "trusted"
    When I run "ctxloom bundle untrust demo"
    Then the command succeeds
    And the output contains "untrusted"

  Scenario: A per-item grant and a blacklist are recorded
    Given an initialized ctxloom project
    And a bundle "demo" exists
    And a fragment "guide" in bundle "demo" exists
    When I run "ctxloom trust demo#fragments/guide"
    Then the command succeeds
    And the output contains "sha256:"
    When I run "ctxloom blacklist demo#fragments/guide"
    Then the command succeeds
    And the output contains "denylist"

  Scenario: Materialize a profile into a native agent surface
    Given an initialized ctxloom project
    And a bundle "demo" exists
    And a fragment "guide" in bundle "demo" exists
    And a profile "dev" with bundle "demo"
    When I run "ctxloom profile materialize dev --target out"
    Then the command succeeds
    And the file "out/CLAUDE.md" contains "Add content here"
