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

  # Companion EXEC consent. Companions are DISCOVERED, not configured — the
  # shipped names plus anything called ctxloom-companion-* on $PATH — and
  # reading one's loadout means RUNNING it. ./node_modules/.bin is on $PATH in
  # a large share of JS projects, so without a gate an npm dependency nobody
  # chose could earn an exec at the next session start just by picking the
  # name. The proof below is the witness file the fake writes when it runs, not
  # an exit code: the failure mode being closed is the silent one.
  Scenario: A companion nobody confirmed is never executed, and says so
    Given an initialized ctxloom project
    And a discovered companion "ctxloom-companion-acme" is on PATH, never confirmed
    When I run "ctxloom doctor"
    Then the output contains "never confirmed for execution"
    And the companion "ctxloom-companion-acme" was never executed
    When I run "ctxloom trust companion allow ctxloom-companion-acme"
    Then the command succeeds
    And the output contains "ctxloom will run it"
    When I run "ctxloom doctor"
    Then the companion "ctxloom-companion-acme" was executed

  # Asserted by the acme entry's PRESENCE and ABSENCE rather than by an empty
  # listing: the scenario HOME legitimately starts with consent recorded for
  # whatever real companions this machine has installed (testenv grants those
  # so the suite behaves as it did before exec consent landed), and an
  # "is it empty" assertion would be an assertion about the developer's laptop.
  Scenario: Companion execution decisions are listable and revocable
    Given an initialized ctxloom project
    And a discovered companion "ctxloom-companion-acme" is on PATH, never confirmed
    When I run "ctxloom trust companion list"
    Then the command succeeds
    And the output does not contain "ctxloom-companion-acme"
    When I run "ctxloom trust companion allow ctxloom-companion-acme"
    Then the command succeeds
    When I run "ctxloom trust companion list"
    Then the command succeeds
    And the output contains "allowed"
    And the output contains "ctxloom-companion-acme"
    When I run "ctxloom trust companion forget ctxloom-companion-acme"
    Then the command succeeds
    And the output contains "forgot 1 decision(s)"
    When I run "ctxloom trust companion list"
    Then the command succeeds
    And the output does not contain "ctxloom-companion-acme"
