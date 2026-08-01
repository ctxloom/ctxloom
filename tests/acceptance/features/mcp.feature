Feature: MCP server configuration
  manage mcp servers manages the MCP servers ctxloom injects into backend settings. A server
  added on the CLI is listed there and exposed to the agent through the
  mcp-servers resource.

  Scenario: Add an MCP server and see it across axes
    Given an initialized ctxloom project
    When I run "ctxloom mcp server create tools -c echo"
    Then the command succeeds
    When I run "ctxloom mcp server list"
    Then the command succeeds
    And the output contains "tools"
    When the agent reads resource "ctxloom://mcp-servers"
    Then the resource contains "tools"

  Scenario: Show an MCP server's configuration
    Given an initialized ctxloom project
    And I run "ctxloom mcp server create tools -c echo"
    When I run "ctxloom mcp server show tools"
    Then the command succeeds
    And the output contains "echo"

  Scenario: Remove an MCP server
    Given an initialized ctxloom project
    And I run "ctxloom mcp server create tools -c echo"
    When I run "ctxloom mcp server delete tools"
    Then the command succeeds
    When I run "ctxloom mcp server list"
    Then the output does not contain "tools"
