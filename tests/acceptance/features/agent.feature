Feature: Agent bindings and container tooling
  Agents are LOCAL engine↔profile bindings (config `agents:`) with an optional
  runtime axis; `init prompt` emits the interview prompt; `container tooling`
  collects trusted bundles' agent-image tool declarations; `container scaffold`
  makes the base Containerfile editable; `container check` is a read-only
  capability diagnosis; `acp list` advertises one ACP server entry per binding.

  Scenario: Agent create, list, show, edit, and delete lifecycle
    Given an initialized ctxloom project
    And a profile "dev" exists
    When I run "ctxloom agent create developer --profiles dev --runtime container"
    Then the command succeeds
    When I run "ctxloom agent list"
    Then the output contains "developer"
    And the output contains "runtime: container"
    When I run "ctxloom agent show developer"
    Then the output contains "Runtime: container"
    When I run "ctxloom agent edit developer --runtime host"
    Then the command succeeds
    When I run "ctxloom agent list"
    Then the output contains "runtime: host"
    # The profiles survived an edit that named only --runtime: `agent edit`
    # merges per field. The upsert it replaced used to wipe every unnamed one.
    And the output contains "profiles: dev"
    When I run "ctxloom agent delete developer"
    Then the command succeeds
    When I run "ctxloom agent list"
    Then the output contains "No agents defined"

  # create and edit are NOT an upsert: each refuses the case the other owns.
  # The upsert `agent set` they replaced silently minted a new agent on a
  # typo'd name and silently overwrote a live one on a reused name.
  Scenario: Create refuses an existing name and edit refuses an absent one
    Given an initialized ctxloom project
    And a profile "dev" exists
    When I run "ctxloom agent create developer --profiles dev"
    Then the command succeeds
    When I run "ctxloom agent create developer --profiles dev"
    Then the command fails
    And the output contains "already exists"
    When I run "ctxloom agent edit nosuchagent --profiles dev"
    Then the command fails
    And the output contains "no agent named"

  Scenario: Init prompt emits the interview prompt
    Given an initialized ctxloom project
    When I run "ctxloom init prompt"
    Then the command succeeds
    And the output contains "SCAN"

  Scenario: Container tooling reports when no trusted bundle declares any
    Given an initialized ctxloom project
    When I run "ctxloom container tooling"
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
    And I run "ctxloom agent create developer --profiles dev"
    When I run "ctxloom acp list"
    Then the command succeeds
    And the output contains "agent_servers"
    And the output contains "developer"
