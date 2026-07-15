Feature: ctxloom doctor — deterministic setup checks

  `ctxloom doctor` is the thin, deterministic half of setup diagnosis: PATH
  binaries, whether configured agents resolve, hook-surface presence, and
  trust-store signer count. Each line carries a DOCTOR-CHECK-* marker — the
  SAME vocabulary the "ctxloom-doctor" Agent Skill (j10_agent_skill.feature)
  uses, so a human or an LLM reading either surface sees one language.
  Version currency is deliberately skill-guided (no automated check yet),
  so its line is informational, not pass/fail.

  Scenario: Doctor reports every check with the shared DOCTOR-CHECK-* marker vocabulary
    Given an initialized ctxloom project
    When I run "ctxloom doctor"
    Then the command succeeds
    And the output contains "DOCTOR-CHECK-DEPS-a1"
    And the output contains "DOCTOR-CHECK-AGENTS-b2"
    And the output contains "DOCTOR-CHECK-VERSION-c3"
    And the output contains "DOCTOR-CHECK-HOOKS-TRUST-d4"

  Scenario: Doctor's JSON form carries the same structured checks
    Given an initialized ctxloom project
    When I run "ctxloom --format json doctor"
    Then the command succeeds
    And the output contains "DOCTOR-CHECK-DEPS-a1"
