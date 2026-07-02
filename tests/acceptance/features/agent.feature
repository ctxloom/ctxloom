Feature: Agent bindings and container tooling
  Agents are LOCAL engine↔profile bindings (config `agents:`) with an optional
  runtime axis; `agent setup` emits the interview prompt; `tooling` collects
  trusted bundles' agent-image tool declarations; `container scaffold` makes
  the base Containerfile editable; `container check` is a read-only capability
  diagnosis; `acp agents` advertises one ACP server entry per binding.

  Scenario: Agent set, list, show, and remove lifecycle
    Given an initialized ctxloom project
    And a profile "dev" exists
    When I run "ctxloom agent set developer --profiles dev --runtime container"
    Then the command succeeds
    When I run "ctxloom agent list"
    Then the output contains "developer"
    And the output contains "runtime: container"
    When I run "ctxloom agent show developer"
    Then the output contains "Runtime: container"
    When I run "ctxloom agent remove developer"
    Then the command succeeds
    When I run "ctxloom agent list"
    Then the output contains "No agents defined"

  Scenario: Agent setup emits the interview prompt
    Given an initialized ctxloom project
    When I run "ctxloom agent setup"
    Then the command succeeds
    And the output contains "SCAN"

  Scenario: Tooling reports when no trusted bundle declares any
    Given an initialized ctxloom project
    When I run "ctxloom tooling"
    Then the command succeeds
    And the output contains "No trusted bundles declare container tooling"

  Scenario: Scaffold materializes the editable base Containerfile
    Given an initialized ctxloom project
    When I run "ctxloom container scaffold"
    Then the command succeeds
    And the file ".ctxloom/base.Containerfile" contains "FROM"
    And the file ".ctxloom/config.yaml" contains "isolation_base_containerfile"

  Scenario: Container capability check is diagnostic-only
    Given an initialized ctxloom project
    When I run "ctxloom container check claude-code"
    Then the command succeeds
    And the output contains "Container capability"

  Scenario: ACP agent entries advertise per-binding servers
    Given an initialized ctxloom project
    And a profile "dev" exists
    And I run "ctxloom agent set developer --profiles dev"
    When I run "ctxloom acp agents"
    Then the command succeeds
    And the output contains "agent_servers"
    And the output contains "developer"
