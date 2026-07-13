Feature: Trust posture CLI
  Per-item acceptances (`trust`) and rejections (`blacklist`) manage what
  content the agent sees. The retired whole-bundle postures (`bundle
  trust`/`untrust`, `remote trust`/`untrust`) are DELETED commands, not
  deprecation stubs — trust is now keyed to a publisher signing key, not a
  bundle or remote (see docs/trust-model.md, docs/trust-simplify-plan.md).

  Scenario: A per-item acceptance and a rejection are recorded
    Given an initialized ctxloom project
    And a bundle "demo" exists
    And a fragment "guide" in bundle "demo" exists
    When I run "ctxloom trust demo#fragments/guide"
    Then the command succeeds
    And the output contains "Approved demo#fragments/guide"
    And the output contains "sha256:"
    When I run "ctxloom blacklist demo#fragments/guide"
    Then the command succeeds
    And the output contains "Rejected demo#fragments/guide"
    And the output contains "denylist"

  Scenario: Materialize a profile into a native agent surface
    Given an initialized ctxloom project
    And a bundle "demo" exists
    And a fragment "guide" in bundle "demo" exists
    And a profile "dev" with bundle "demo"
    When I run "ctxloom profile materialize dev --target out"
    Then the command succeeds
    And the file "out/CLAUDE.md" contains "Add content here"
