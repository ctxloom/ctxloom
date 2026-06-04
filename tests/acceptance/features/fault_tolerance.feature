Feature: Fault tolerance
  ctxloom is fault tolerant above all else: misconfiguration and missing
  references produce warnings, not crashes, and read commands still work. These
  scenarios exercise the degradation paths CLAUDE.md names as the top priority.

  Scenario: A malformed config warns but does not block read commands
    Given an initialized ctxloom project
    And a bundle "demo" exists
    And a malformed ctxloom config
    When I run "ctxloom bundle list"
    Then the command succeeds
    And the output contains "warn"

  Scenario: A profile referencing a missing bundle warns and continues
    Given an initialized ctxloom project
    And a profile "broken" referencing a missing bundle
    When I run "ctxloom run --dry-run --profile broken hello"
    Then the command succeeds
    And the output contains "warning"

  Scenario: An unknown fragment reference fails cleanly
    Given an initialized ctxloom project
    And a bundle "demo" exists
    When I run "ctxloom fragment show demo#fragments/does-not-exist"
    Then the command fails

  Scenario: A garbage reference fails cleanly
    Given an initialized ctxloom project
    And a bundle "demo" exists
    When I run "ctxloom fragment show not-a-real-reference"
    Then the command fails

  Scenario: An unreachable remote does not crash sync
    Given an initialized ctxloom project
    And I run "ctxloom remote add broken file:///nonexistent/ctxloom-repo.git --forge git"
    When I run "ctxloom remote sync"
    Then the command succeeds
