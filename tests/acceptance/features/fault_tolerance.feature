Feature: Fault tolerance
  ctxloom is fault tolerant: a missing reference or an unresolvable remote
  produces a warning, never a crash, and read commands still work. One thing
  is not degraded: a config.yaml that is PRESENT and cannot be parsed is
  refused by name, because no command can know what a file it could not read
  was asking for — an absent config is the shipped default, a broken one is
  a fault to fix.

  Scenario: A malformed config is refused by name, not read around
    Given an initialized ctxloom project
    And a bundle "demo" exists
    And a malformed ctxloom config
    When I run "ctxloom bundle list"
    Then the command fails
    And the output contains "cannot be parsed"
    And the output contains "config.yaml"

  Scenario: A profile referencing a missing bundle warns and continues
    Given an initialized ctxloom project
    And a profile "broken" referencing a missing bundle
    When I run "ctxloom run --dry-run --profile broken hello"
    Then the command succeeds
    And the output contains "warning"

  # "Cleanly" is the whole claim, and an exit code cannot express it: a Go
  # panic in the resolution path also exits non-zero, so `the command fails`
  # alone was satisfied by a stack trace. The message is the contract — it
  # names the missing item AND what is available instead, which is what makes
  # the failure recoverable rather than merely non-zero.
  Scenario: An unknown fragment reference fails cleanly
    Given an initialized ctxloom project
    And a bundle "demo" exists
    When I run "ctxloom fragment show demo#fragments/does-not-exist"
    Then the command fails
    And the output contains "item not found"
    And the output contains "Available fragments: example"

  # Same shape: the reference never parsed, so the message must teach the
  # grammar rather than dump a stack.
  Scenario: A garbage reference fails cleanly
    Given an initialized ctxloom project
    And a bundle "demo" exists
    When I run "ctxloom fragment show not-a-real-reference"
    Then the command fails
    And the output contains "invalid reference format: expected bundle#fragments/name"

  # This scenario used to REGISTER the unreachable remote and never depend on
  # it, so `deps pull` printed "No remote dependencies to pull." and exited
  # 0 — the subject never ran, and the whole Then block was that exit code.
  # An early `return nil` in the pull path was indistinguishable from a pull
  # that degraded gracefully. A profile referencing broken/demo puts the
  # unreachable remote INTO the dependency closure, so the clone is really
  # attempted; the assertions then name the failing reference, the count, and
  # the underlying git cause — a swallowed diagnostic or a skipped fetch
  # cannot produce any of them.
  Scenario: An unreachable remote fails the pull cleanly rather than crashing
    Given an initialized ctxloom project
    And I run "ctxloom remote create broken file:///nonexistent/ctxloom-repo.git --forge git"
    And I run "ctxloom profile create dev --bundle broken/demo"
    When I run "ctxloom deps pull"
    Then the command fails
    And the output contains "Failed: 1"
    And the output contains "@bundles/demo"
    And the output contains "does not appear to be a git repository"
