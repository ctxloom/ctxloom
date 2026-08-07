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

  # `mcp server edit` is the only leaf here that hands control to an external
  # program and takes the result back, so the claim is the ROUND TRIP: what the
  # editor wrote must reach the bundle on disk. A scenario asserting exit 0
  # would pass against an editor that was never launched.
  #
  # The fixture authors the bundle's mcp section directly, and that is not
  # laziness: `mcp server create` cannot produce a server this command can
  # address. create takes a bare <name> and stores it in the project config —
  # passing it the documented "<bundle>#mcp/<name>" ref simply makes that whole
  # string the name — while edit resolves the ref and looks in the bundle. The
  # two never meet (taskloom filed separately). Authoring the manifest is the
  # only way to reach this code path today.
  Scenario: Editing a bundle's MCP server writes the editor's result back
    Given a ctxloom project with a command-rewriting editor
    And the project already has the file ".ctxloom/content/bundles/demo.yaml":
      """
      version: 1.0.0
      description: mcp edit fixture
      mcp:
          tools:
              command: echo
              args:
                  - KEEP-THIS-ARGUMENT
      """
        When I run "ctxloom mcp server edit demo#mcp/tools"
    Then the command succeeds
    And the output contains "Updated MCP server"
    And the file ".ctxloom/content/bundles/demo.yaml" contains "EDITED-BY-TEST"
    # The rest of the entry must survive the round trip. An implementation that
    # rewrote the manifest from just the edited field would satisfy the line
    # above while silently dropping the arguments.
    And the file ".ctxloom/content/bundles/demo.yaml" contains "KEEP-THIS-ARGUMENT"

  # The refusal path. A ref naming a server that is not there must fail rather
  # than launch an editor on an empty buffer and write a new entry from it —
  # which is how a typo'd ref becomes a silently created server.
  Scenario: Editing an MCP server that does not exist fails instead of creating one
    Given a ctxloom project with a command-rewriting editor
    When I run "ctxloom mcp server edit demo#mcp/absent"
    Then the command fails
    And the output contains "not found"
