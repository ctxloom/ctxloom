Feature: MCP resources
  The agent reads ctxloom state through read-only resources. Each returns a typed
  payload reflecting on-disk state.

  Scenario: The help resource documents the resource surface
    Given an initialized ctxloom project
    When the agent reads resource "ctxloom://help"
    Then the resource MIME type is "text/markdown"
    And the resource contains "ctxloom://"

  Scenario: The fragments resource reflects created fragments
    Given an initialized ctxloom project
    And a bundle "demo" exists
    And a fragment "testing" in bundle "demo" exists
    When the agent reads resource "ctxloom://fragments"
    Then the resource MIME type is "application/yaml"
    And the resource contains "testing"

  Scenario: The skills resource reflects created prompts
    Given an initialized ctxloom project
    And a bundle "demo" exists
    And a skill "review" in bundle "demo" exists
    When the agent reads resource "ctxloom://skills"
    Then the resource MIME type is "application/yaml"
    And the resource contains "review"

  Scenario: The profiles resource reflects created profiles
    Given an initialized ctxloom project
    And a bundle "demo" exists
    And a profile "dev" with bundle "demo"
    When the agent reads resource "ctxloom://profiles"
    Then the resource MIME type is "application/yaml"
    And the resource contains "dev"

  Scenario: The remotes resource is readable
    Given an initialized ctxloom project
    When the agent reads resource "ctxloom://remotes"
    Then the resource MIME type is "application/yaml"

  Scenario: The mcp-servers resource reflects configured servers
    Given an initialized ctxloom project
    And I run "ctxloom manage mcp servers add tools -c echo"
    When the agent reads resource "ctxloom://mcp-servers"
    Then the resource MIME type is "application/yaml"
    And the resource contains "tools"

  Scenario: The sessions resources are readable
    Given an initialized ctxloom project
    When the agent reads resource "ctxloom://sessions"
    Then the resource MIME type is "application/yaml"
    When the agent reads resource "ctxloom://sessions/recent"
    Then the resource MIME type is "application/yaml"

