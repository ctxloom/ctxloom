Feature: Task tracking across file, CLI, and agent
  ctxloom's task store is written by the CLI and read by the agent over MCP.
  A task is the same fact through all three axes.

  Scenario: A task added on the CLI is visible to the mock agent
    Given an initialized ctxloom project
    When I run "ctxloom tasks add write the spine test"
    Then the command succeeds
    When the agent calls tool "task_list"
    Then the tool result contains "write the spine test"
    When I run "ctxloom tasks list"
    Then the command succeeds
    And the output contains "write the spine test"

  Scenario: A task added by the mock agent is visible on the CLI
    Given an initialized ctxloom project
    When the agent calls tool "task_add" with:
      | text   | added by the agent |
      | status | To Do              |
    Then the tool result field "task.harp_id" is set
    When I run "ctxloom tasks list"
    Then the command succeeds
    And the output contains "added by the agent"

  Scenario: The agent moves a task to a new status
    Given an initialized ctxloom project
    When the agent calls tool "task_add" with:
      | text | move me |
    And the agent sets the last task to "In Progress"
    Then the tool result field "task.status" equals "In Progress"

  Scenario: The agent edits a task in place
    Given an initialized ctxloom project
    When the agent calls tool "task_add" with:
      | text | original text |
    And the agent edits the last task to "revised text"
    Then the tool result contains "revised text"
    When I run "ctxloom tasks list"
    Then the output contains "revised text"

  Scenario: Change task status and text from the CLI
    Given an initialized ctxloom project
    When the agent calls tool "task_add" with:
      | text | cli mutation target |
    Then the tool result field "task.harp_id" is set
    When I run "ctxloom tasks status {task} Done"
    Then the command succeeds
    When I run "ctxloom tasks edit {task} reworded"
    Then the command succeeds
    When I run "ctxloom tasks list --all"
    Then the output contains "reworded"

  Scenario: A deferred task carries a revive trigger
    Given an initialized ctxloom project
    When I run "ctxloom tasks add park me --status Deferred --trigger when-ready"
    Then the command succeeds
    When I run "ctxloom tasks list --status Deferred"
    Then the output contains "park me"

  Scenario: Task summary and JSON output
    Given an initialized ctxloom project
    When I run "ctxloom tasks add count me"
    Then the command succeeds
    When I run "ctxloom tasks summary"
    Then the command succeeds
    When I run "ctxloom tasks list --json"
    Then the command succeeds
    And the output contains "count me"
