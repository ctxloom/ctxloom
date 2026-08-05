Feature: MCP tools
  The agent drives ctxloom through tools. These exercise the context and search
  tools end to end, plus load_session and recover_session (below).
  compact_session and get_previous_session are still NOT covered here or
  anywhere else — the live distillation suite tagged live in
  distill_live.feature never touches the session tools, it only exercises
  fragment/command/bundle distillation. That is a real, tracked gap (see
  tests/acceptance/completeness_test.go), not something to paper over with a
  false "covered elsewhere" claim.

  # `fragments_loaded` has no omitempty, so the KEY is present even when the
  # value is null: asserting it proved only that the envelope had a field,
  # which assembly delivering zero fragments and an empty context satisfies
  # perfectly. The assertions name the fragment that must have been resolved
  # and the marker from inside its stored body, so an empty assembly cannot
  # pass.
  Scenario: Assemble context from a profile
    Given an initialized ctxloom project
    And a bundle "demo" exists
    And a fragment "testing" in bundle "demo" exists
    And a profile "dev" with bundle "demo"
    When the agent calls tool "assemble_context" with:
      | profile | dev |
    Then the tool call succeeds
    And the tool result contains "fragments_loaded"
    And the tool result contains "demo#fragments/testing"
    And the tool result contains "FRAGMENT-BODY-testing"

  Scenario: Search content over MCP
    Given an initialized ctxloom project
    And a bundle "demo" exists
    And a fragment "testing" in bundle "demo" exists
    When the agent calls tool "search_content" with:
      | query | testing |
    Then the tool call succeeds
    And the tool result contains "testing"

  # "Degrades gracefully" means it RAN and found nothing, not that it exited 0.
  # A handler that never called SearchRemotes at all — and so never surfaced an
  # error either — satisfied the bare exit-code assertion. The decoded result
  # fields can only be there if a real SearchRemotesResult came back.
  Scenario: Search the installable library degrades gracefully with no remotes
    Given an initialized ctxloom project
    When the agent calls tool "search_library" with:
      | query | anything |
    Then the tool call succeeds
    And the tool result field "query" equals "anything"
    And the tool result field "count" equals "0"

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
  # REAL MCP surface against a synthetic session sized like the original
  # incident (see steps_recover_session.go), with the mock LLM standing in
  # for a distillation that never actually compresses (its default response
  # echoes the prompt it was sent verbatim — exit 0, zero compression). If
  # the bound regressed, this comes back with the oversized text; with the
  # fix, it comes back small — an honest failure — no matter how large the
  # input was.
  Scenario: recover_session bounds an oversized distillation instead of passing it through
    Given an initialized ctxloom project
    And a captured session "big-session-harp" with a large canonical transcript
    And the compaction LLM is a mock that never compresses
    When the agent calls tool "recover_session" with:
      | session_id | big-session-harp |
    Then the tool call succeeds
    And the tool result is under 5000 bytes
    And the tool result field "loaded" equals "false"
