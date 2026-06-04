Feature: Hooks
  manage hooks install wires ctxloom into the backend so it injects context at session
  start. Applying writes the backend settings and the MCP server registration.

  Scenario: Apply hooks writes backend settings and MCP registration
    Given an initialized ctxloom project
    When I run "ctxloom manage hooks install"
    Then the command succeeds
    And the file ".claude/settings.json" exists
    And the file ".mcp.json" exists
    And the file ".mcp.json" contains "ctxloom"
