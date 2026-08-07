Feature: MCP tools
  The agent drives ctxloom through tools. These exercise the context and search
  tools end to end, plus the four session-memory tools — load_session,
  recover_session, compact_session, get_previous_session — and list_sessions.

  The session tools share one hazard and it is why their assertions look the
  way they do. Every one of them can answer successfully while delivering
  nothing: distillation that ran and was discarded, an essence written under a
  key nobody reads back, a listing of sessions with no titles. So each
  scenario names a marker that exists ONLY in its own fixture's transcript,
  and asserts that marker in the result. An envelope with the right fields is
  not evidence.

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

  # goofy-dingo: a session is normally addressed by the backend-native id its
  # index entry binds, NOT by its harp — and resolving that id goes THROUGH the
  # harp. When that resolution let the harp escape as the session's identity,
  # every downstream key was chosen from it: the essence was written under the
  # harp while the caller read back under the id it passed. The distillation
  # succeeded, the file was on disk, and recover_session still answered
  # "couldn't read it back" — work done, LLM call paid for, result discarded,
  # no error anywhere. One measured incident re-derived 143,878 input tokens
  # this way, on every single attempt, because the cache lookup missed for the
  # same reason.
  #
  # The scenario above cannot see any of this: it addresses the session BY ITS
  # HARP, so both keys are the same string and agree no matter what. This one
  # uses the production shape, where they differ, and asserts the essence comes
  # back with its real content rather than an apology.
  Scenario: A distilled session is readable back by the id the caller passed
    Given an initialized ctxloom project
    And a captured session "steady-vellum-crane" bound to a backend-native session id
    And the compaction LLM is a mock that never compresses
    When the agent calls tool "recover_session" with:
      | session_id | seeded-steady-vellum-crane |
    Then the tool call succeeds
    And the tool result field "loaded" equals "true"
    And the tool result contains "RECOVER-IDENTITY-ROUND-TRIP"
    # The identity claim itself, and the only assertion here that the read-back
    # fix cannot satisfy on its behalf: the tool must report back the session it
    # was ASKED for. When resolution let the harp escape as the session's
    # identity, this came back as "steady-vellum-crane" — a different session
    # than the caller named, silently.
    And the tool result field "session_id" equals "seeded-steady-vellum-crane"

  # list_sessions is the agent's index reader. The claim is not that it
  # returned a list — an empty list is a list — but that it returned BOTH
  # seeded sessions WITH the titles their index entries carry. A handler that
  # listed harps and dropped summaries would still look right to a caller
  # counting rows, and would leave an agent unable to tell one prior session
  # from another, which is the entire point of the tool.
  Scenario: list_sessions returns every session in the index, with its title
    Given an initialized ctxloom project
    And a recorded session "amber-quiet-heron" summarised as "chose the ledger shape"
    And a recorded session "brisk-copper-moth" summarised as "ruled out the shim"
    When the agent calls tool "list_sessions"
    Then the tool call succeeds
    And the tool result contains "amber-quiet-heron"
    And the tool result contains "chose the ledger shape"
    And the tool result contains "brisk-copper-moth"
    And the tool result contains "ruled out the shim"

  # compact_session distills on demand. Its result envelope carries only
  # bookkeeping — chunk count, token counts, a reduction ratio, an output path
  # — and every one of those is reported identically by a compaction that ran
  # against an empty session. So the payload assertion has to follow the
  # output_path the tool reports and read what actually landed there.
  #
  # WHERE it lands is deliberately NOT asserted, because it is currently
  # wrong and that is a decision rather than a fix: the essence is filed under
  # the CALLING session's harp, not the harp of the session named by
  # session_id, while the result echoes back the id that was asked for
  # (taskloom onshore-pardon). Asserting the location would mean either
  # encoding today's behaviour as intended or leaving a red scenario behind;
  # asserting the CONTENT proves the distillation genuinely ran on the seeded
  # transcript either way. Tighten this to the requested session's own path
  # once that decision is made.
  Scenario: compact_session distills the session it was handed
    Given an initialized ctxloom project
    And a captured session "quiet-ember-drift" bound to a backend-native session id
    And the compaction LLM is a mock that never compresses
    When the agent calls tool "compact_session" with:
      | session_id | seeded-quiet-ember-drift |
    Then the tool call succeeds
    And the tool result field "session_id" equals "seeded-quiet-ember-drift"
    And the essence the tool reports writing contains "RECOVER-IDENTITY-ROUND-TRIP"

  # get_previous_session takes no arguments at all: it resolves the previous
  # session itself. That makes it the easiest of these to satisfy vacuously —
  # "no previous session" is a perfectly good answer shape — so the assertion
  # is the seeded transcript's marker coming back through a distillation the
  # tool had to perform, the essence having been deliberately left absent.
  Scenario: get_previous_session finds the prior session and returns its content
    Given an initialized ctxloom project
    And a captured session "wispy-harbor-glint" bound to a backend-native session id
    And the compaction LLM is a mock that never compresses
    When the agent calls tool "get_previous_session"
    Then the tool call succeeds
    And the tool result field "loaded" equals "true"
    And the tool result contains "RECOVER-IDENTITY-ROUND-TRIP"
