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

  Scenario: The commands resource reflects created prompts
    Given an initialized ctxloom project
    And a bundle "demo" exists
    And a command "review" in bundle "demo" exists
    When the agent reads resource "ctxloom://commands"
    Then the resource MIME type is "application/yaml"
    And the resource contains "review"

  Scenario: The profiles resource reflects created profiles
    Given an initialized ctxloom project
    And a bundle "demo" exists
    And a profile "dev" with bundle "demo"
    When the agent reads resource "ctxloom://profiles"
    Then the resource MIME type is "application/yaml"
    And the resource contains "dev"

  # "Readable" was asserted as a MIME type on an envelope whose body could be
  # empty. A configured remote gives the resource something it must actually
  # carry.
  Scenario: The remotes resource reflects configured remotes
    Given an initialized ctxloom project
    And I run "ctxloom remote create origin file:///tmp/acceptance-remote.git --forge git"
    When the agent reads resource "ctxloom://remotes"
    Then the resource MIME type is "application/yaml"
    And the resource contains "name: origin"
    And the resource contains "url: file:///tmp/acceptance-remote.git"
    And the resource contains "count: 1"

  Scenario: The mcp-servers resource reflects configured servers
    Given an initialized ctxloom project
    And I run "ctxloom mcp server create tools -c echo"
    When the agent reads resource "ctxloom://mcp-servers"
    Then the resource MIME type is "application/yaml"
    And the resource contains "tools"

  # Same shape as the remotes resource above: a recorded session is payload
  # both views must carry, and `recent` additionally states its own
  # completeness (total/truncated are emitted unconditionally so their absence
  # is never ambiguous).
  Scenario: The sessions resources reflect a recorded session
    Given an initialized ctxloom project
    And a recorded session "amber-swift-owl"
    When the agent reads resource "ctxloom://sessions"
    Then the resource MIME type is "application/yaml"
    And the resource contains "amber-swift-owl"
    When the agent reads resource "ctxloom://sessions/recent"
    Then the resource MIME type is "application/yaml"
    And the resource contains "amber-swift-owl"
    And the resource contains "total: 1"
    And the resource contains "truncated: false"

