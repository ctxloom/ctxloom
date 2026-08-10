Feature: ctxloom doctor — deterministic setup checks

  `ctxloom doctor` is the thin, deterministic half of setup diagnosis: PATH
  binaries, whether configured agents resolve, hook-surface presence, and
  trust-store signer count. Each line carries a DOCTOR-CHECK-* marker — the
  SAME vocabulary the "ctxloom-doctor" Agent Skill (j000600_agent_skill.feature)
  uses, so a human or an LLM reading either surface sees one language.
  Version currency is deliberately skill-guided (no automated check yet),
  so its line is informational, not pass/fail.

  Scenario: Doctor reports every check with the shared DOCTOR-CHECK-* marker vocabulary
    Given an initialized ctxloom project
    When I run "ctxloom doctor"
    Then the command succeeds
    And the output contains "DOCTOR-CHECK-DEPS-a1"
    And the output contains "DOCTOR-CHECK-AGENTS-b2"
    And the output contains "DOCTOR-CHECK-VERSION-c3"
    And the output contains "DOCTOR-CHECK-HOOKS-TRUST-d4"

  # Every marker this could substring-match is in the TEXT rendering too (the
  # scenario above asserts the same one there), so nothing here said the output
  # was json: a doctor that rendered human text under --format json passed.
  # Parse it, and assert the decoded structure the text form cannot produce.
  Scenario: Doctor's JSON form carries the same structured checks
    Given an initialized ctxloom project
    When I run "ctxloom --format json doctor"
    Then the command succeeds
    And the output is valid JSON
    And the JSON output array "checks" contains an object whose "marker" is "DOCTOR-CHECK-DEPS-a1"
    And the JSON output array "checks" contains an object whose "marker" is "DOCTOR-CHECK-AGENTS-b2"
    And the JSON output array "checks" contains an object whose "marker" is "DOCTOR-CHECK-VERSION-c3"
    And the JSON output array "checks" contains an object whose "marker" is "DOCTOR-CHECK-HOOKS-TRUST-d4"
    And every object in the JSON output array "checks" has a non-empty "status"
    And every object in the JSON output array "checks" has a non-empty "detail"

  # A settings file naming a ctxloom invocation that does not speak MCP is the
  # one broken state nothing else in the system can see: the entry is PRESENT,
  # so every wiring check reports it healthy, and the engine starts fine. What
  # fails is invisible — the client waits on a handshake that never arrives,
  # the session comes up with none of ctxloom's tools, and nothing says why.
  # Reading the argv is the only way to tell a working entry from that one.
  Scenario: Doctor names a settings file whose ctxloom entry cannot speak the protocol
    Given an initialized ctxloom project
    And the project already has the file ".mcp.json":
      """
      {
        "mcpServers": {
          "ctxloom": {"command": "/usr/local/bin/ctxloom", "args": ["mcp"]}
        }
      }
      """
    When I run "ctxloom doctor"
    Then the command succeeds
    And the output contains "DOCTOR-CHECK-MCP-INVOCATION-g7"
    And the output contains ".mcp.json"
    And the output contains "ctxloom init"

  # The paired negative, and the one that keeps the check honest: a report that
  # warned on every project would be indistinguishable from one that works, and
  # would teach a user to skip the line.
  Scenario: Doctor stays quiet about a settings file that names the protocol server
    Given an initialized ctxloom project
    And the project already has the file ".mcp.json":
      """
      {
        "mcpServers": {
          "ctxloom": {"command": "/usr/local/bin/ctxloom", "args": ["mcp", "serve"]}
        }
      }
      """
    When I run "ctxloom --format json doctor"
    Then the command succeeds
    And the JSON output array "checks" contains an object whose "marker" is "DOCTOR-CHECK-MCP-INVOCATION-g7" and whose "status" is "ok"
