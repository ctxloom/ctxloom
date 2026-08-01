Feature: Trust posture CLI
  Per-item acceptances (`trust accept`) and rejections (`trust reject`) manage
  what content the agent sees. The retired whole-bundle postures (`bundle
  trust`/`untrust`, `remote trust`/`untrust`) are DELETED commands, not
  deprecation stubs — trust is now keyed to a publisher signing key, not a
  bundle or remote (see docs/trust-model.md, docs/trust-simplify-plan.md).

  Scenario: A per-item acceptance and a rejection are recorded
    # Acceptance now countersigns content BYTES (a signing-key fingerprint),
    # not a hash-pair ledger entry — there is no "sha256:" content hash to
    # print. With no signing key configured the fixture takes the unsigned
    # degraded path (spec S9.5), which the CLI says so plainly.
    Given an initialized ctxloom project
    And a bundle "demo" exists
    And a fragment "guide" in bundle "demo" exists
    When I run "ctxloom trust accept demo#fragments/guide"
    Then the command succeeds
    And the output contains "Approved demo#fragments/guide"
    And the output contains "UNSIGNED"
    When I run "ctxloom trust reject demo#fragments/guide"
    Then the command succeeds
    And the output contains "Rejected demo#fragments/guide"
    And the output contains "rejected in form(s) raw"
