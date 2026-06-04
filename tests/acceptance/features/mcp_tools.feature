Feature: MCP tools
  The agent drives ctxloom through tools. These exercise the context, search, and
  task tools end to end. Session tools (compact/load/recover) are covered under
  the @live distillation suite.

  Scenario: Assemble context from a profile
    Given an initialized ctxloom project
    And a bundle "demo" exists
    And a profile "dev" with bundle "demo"
    When the agent calls tool "assemble_context" with:
      | profile | dev |
    Then the tool call succeeds
    And the tool result contains "fragments_loaded"

  Scenario: Search content over MCP
    Given an initialized ctxloom project
    And a bundle "demo" exists
    And a fragment "testing" in bundle "demo" exists
    When the agent calls tool "search_content" with:
      | query | testing |
    Then the tool call succeeds
    And the tool result contains "testing"

  Scenario: Search the installable library degrades gracefully with no remotes
    Given an initialized ctxloom project
    When the agent calls tool "search_library" with:
      | query | anything |
    Then the tool call succeeds

  Scenario: Load a prior session's essence over MCP
    Given an initialized ctxloom project
    And a recorded session "amber-swift-owl"
    When the agent calls tool "load_session" with:
      | harp_name | amber-swift-owl |
    Then the tool call succeeds
    And the tool result contains "Seeded essence"

  Scenario: Add and list tasks over MCP
    Given an initialized ctxloom project
    When the agent calls tool "task_add" with:
      | text | via mcp |
    Then the tool result field "task.harp_id" is set
    When the agent calls tool "task_list"
    Then the tool result contains "via mcp"

  Scenario: Change task status and edit text over MCP
    Given an initialized ctxloom project
    When the agent calls tool "task_add" with:
      | text | mutate me |
    And the agent sets the last task to "In Progress"
    Then the tool result field "task.status" equals "In Progress"
    When the agent edits the last task to "mutated"
    Then the tool result contains "mutated"
