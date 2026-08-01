Feature: Sessions
  session reads and manages the harp-keyed session index. With an index entry
  seeded, the lifecycle operations are observable without launching a backend.

  Scenario: List shows nothing for a fresh project
    Given an initialized ctxloom project
    When I run "ctxloom session list"
    Then the command succeeds
    And the output contains "no sessions"

  Scenario: A recorded session is listed
    Given an initialized ctxloom project
    And a recorded session "amber-swift-owl"
    When I run "ctxloom session list --all"
    Then the command succeeds
    And the output contains "amber-swift-owl"

  Scenario: Rename a session in the index
    Given an initialized ctxloom project
    And a recorded session "amber-swift-owl"
    When I run "ctxloom session rename amber-swift-owl bright-keen-hawk"
    Then the command succeeds
    When I run "ctxloom session list --all"
    Then the output contains "bright-keen-hawk"
    And the output does not contain "amber-swift-owl"

  Scenario: Show a recorded session's essence
    Given an initialized ctxloom project
    And a recorded session "amber-swift-owl"
    When I run "ctxloom session show amber-swift-owl"
    Then the command succeeds
    And the output contains "Seeded essence"

  Scenario: Forget a session drops it from the index
    Given an initialized ctxloom project
    And a recorded session "amber-swift-owl"
    When I run "ctxloom session delete amber-swift-owl"
    Then the command succeeds
    When I run "ctxloom session list --all"
    Then the output does not contain "amber-swift-owl"
