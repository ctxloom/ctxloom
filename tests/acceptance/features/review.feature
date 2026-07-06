@remote
Feature: Review pending items
  `ctxloom review` is the single review porcelain of the three-state model:
  a third-party bundle's items are born pending and withheld from every
  exposure surface until a human accepts them. Off a TTY, `review --list`
  prints the pending table; the scriptable plumbing (`trust`/`blacklist`)
  records the same states the interactive porcelain writes.

  Scenario: Pending items are listed, withheld, and exposed once accepted
    Given an initialized ctxloom project
    And a git remote "origin" serving a ctxloom bundle
    And I run "ctxloom remote default origin"
    And I run "ctxloom profile create dev --bundle demo"
    And I run "ctxloom remote pull"
    # The pinned third-party bundle's items are born pending: review names them…
    When I run "ctxloom review --list"
    Then the command succeeds
    And the output contains "fragments/demo-frag"
    And the output contains "skills/demo-skill"
    And the output contains "new"
    # …and the content gate withholds them from the materialized context.
    When I run "ctxloom profile materialize dev --target out"
    Then the command succeeds
    And the file "out/CLAUDE.md" does not contain "Demo fragment content"
    # Accepting through the plumbing records the same accepted state the
    # porcelain writes: the item leaves the pending set and reaches the agent.
    When I accept the pending item "demo#fragments/demo-frag" from remote "origin"
    Then the command succeeds
    And the output contains "Accepted"
    When I run "ctxloom review --list"
    Then the command succeeds
    And the output does not contain "fragments/demo-frag"
    And the output contains "skills/demo-skill"
    When I run "ctxloom profile materialize dev --target out"
    Then the command succeeds
    And the file "out/CLAUDE.md" contains "Demo fragment content"

  Scenario: A rejected item stays withheld and leaves the pending set
    Given an initialized ctxloom project
    And a git remote "origin" serving a ctxloom bundle
    And I run "ctxloom remote default origin"
    And I run "ctxloom profile create dev --bundle demo"
    And I run "ctxloom remote pull"
    When I reject the pending item "demo#fragments/demo-frag" from remote "origin"
    Then the command succeeds
    And the output contains "Rejected"
    When I run "ctxloom review --list"
    Then the command succeeds
    And the output does not contain "fragments/demo-frag"
    When I run "ctxloom profile materialize dev --target out"
    Then the command succeeds
    And the file "out/CLAUDE.md" does not contain "Demo fragment content"

  Scenario: Nothing pending reads as a clean slate
    Given an initialized ctxloom project
    When I run "ctxloom review --list"
    Then the command succeeds
    And the output contains "Nothing is pending review."
