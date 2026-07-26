Feature: MCP tools
  The agent drives ctxloom through tools. These exercise the context and search
  tools end to end, plus load_session and recover_session (below).
  compact_session and get_previous_session are still NOT covered here or
  anywhere else — the live distillation suite tagged live in
  distill_live.feature never touches the session tools, it only exercises
  fragment/command/bundle distillation. That is a real, tracked gap (see
  tests/acceptance/completeness_test.go), not something to paper over with a
  false "covered elsewhere" claim.

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

  # quit-eagle: recover_session once returned a ~381,000-char essence for a
  # large session — the map/reduce distillation pipeline "succeeded" (every
  # LLM call exited 0) but never actually compressed enough, and there was no
  # ceiling anywhere to catch it. This drives recover_session through the
  # REAL MCP surface against a synthetic session sized and shaped like a
  # genuine large capture (see steps_recover_session.go), with the mock LLM
  # standing in for a distillation that fails to compress. If the bound
  # regressed, this comes back with the oversized text (or times out); with
  # the fix, it comes back small no matter how large the input was.
  Scenario: recover_session bounds an oversized distillation instead of passing it through
    Given an initialized ctxloom project
    And a captured session "big-session-harp" with a large canonical transcript
    And the mock LLM responds with an oversized distillation
    When the agent calls tool "recover_session" with:
      | session_id | big-session-harp |
    Then the tool call succeeds
    And the tool result is under 5000 bytes
    And the tool result field "loaded" equals "false"
