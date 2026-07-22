@doc
Feature: Cross-session memory — distilling a session so a later one can pick it up

  A coding session ends and everything the assistant worked out — the shape of a
  refactor, the value it finally pinned down, what is still open — dies with the
  process. The next session starts cold and the developer pays for it again: they
  re-explain, or the assistant re-derives, or worse, quietly guesses. ctxloom's
  `memory` surface is how that knowledge survives the session boundary. It reads a
  session's complete transcript OUT OF BAND — a fresh process with a full budget,
  not a context-starved agent summarising itself on its way out the door — and
  distils it into a stored essence a later session can recall. Without it, memory
  is whatever a running agent can hold in one context window, and nothing crosses
  the gap between sessions.

  # WHAT THIS JOURNEY CAN AND CANNOT SEE (honesty, mirroring j9_isolation's own
  # note). Grounded in source, not the command's name.
  #
  #   1. `ctxloom memory` is an ADMINISTRATIVE surface, not a delivery one. It has
  #      exactly three subcommands — compact (distil), list, show — see
  #      internal/cli/memory.go:44-73. compact PRODUCES a stored essence; list and
  #      show REPORT on what's stored. None of them injects memory into a running
  #      engine's assembled context. So this journey proves the STORE/RECALL half
  #      of cross-session memory honestly, and does not pretend the CLI itself
  #      hands memory to an assistant.
  #
  #   2. Delivery of a stored essence INTO a fresh session's payload is a
  #      DIFFERENT surface and is deliberately NOT claimed here: the SessionStart
  #      `ctxloom hook inject-context` hook (internal/cli/hook_inject_context.go)
  #      and the MCP recover_session / get_previous_session / load_session tools
  #      (internal/cli/mcp_tools_memory.go) are what put distilled content in front
  #      of an assistant. J1's restart pattern already proves configured context
  #      reaching a restarted engine; the memory-into-new-session delivery path is
  #      deferred to that surface, not asserted through `ctxloom memory`.
  #
  #   3. What compact CAN be seen doing hermetically: it distils by spawning the
  #      configured COMPACTION backend (config.GetCompactionLLM(), memory.go:297)
  #      as a one-shot subprocess, sending the transcript wrapped in <session_log>
  #      as that subprocess's PROMPT with SkipSetup=true (compactor.go:795-818).
  #      When the mock backend IS the compaction engine, that prompt lands in the
  #      mock record (CTXLOOM_MOCK_RECORD_FILE — internal/lm/backends/mock.go:69,108)
  #      under "=== Prompt ===". THAT is the honest signal that the real transcript
  #      reached the distiller — the same mock-record technique j1/j9 read. The
  #      mock's response becomes the essence body, which `memory show` reads back
  #      from disk (memory.go:272-284) and `memory list` marks as compacted
  #      (memory.go:117-127, loadDistilledSet).
  #
  #   4. THE STANDING CAVEAT — a transcript must EXIST to be compacted. compact,
  #      list, and show all read a session's PERSISTED transcript through
  #      resolveSessionSource -> CanonicalFallbackSource (memory.go:179-199). The
  #      mock backend has NO session history of its own (mock.go:47 returns
  #      NilSessionHistory), so hermetically the only transcript source is
  #      ctxloom's OWN captured transcript.acp.jsonl (canonical_source.go:16),
  #      seeded into ~/.ctxloom/sessions/<harp>/ plus a session-index binding.
  #      This is exactly why the completeness suite currently EXCLUDES these
  #      commands as "not reproducible hermetically" (completeness_test.go:224-226).
  #      Every scenario below therefore assumes a seeded canonical-transcript
  #      fixture (the "recorded prior session" Given). If that fixture proves
  #      infeasible to seed, these scenarios downgrade to @live against a real
  #      backend that produced a genuine transcript — they do NOT get faked green.
  #
  #   5. Memory is LOCAL to the developer's machine (~/.ctxloom/sessions/<harp>/),
  #      not shared with teammates. There is no cross-developer memory handoff to
  #      prove, so this journey has no Bob scenario — Alice's earlier self is the
  #      only party her later self recovers from.

  Background:
    Given Alice has a project whose mock agent is both its primary and its compaction engine
    And an earlier session "auth-refactor" left a recorded transcript noting "the token TTL is 900 seconds"

  # The core store claim, and the honest payload assertion: compacting drives the
  # real transcript through the distiller (proven by the mock record the
  # compaction subprocess writes), and what comes back is persisted as this
  # session's memory (proven by reading the stored essence, not by "a file
  # exists"). The compaction engine's response is fixed to a known distilled
  # summary so the recalled content is deterministic.
  Scenario: Compacting a session distils its transcript into stored memory
    Given the compaction engine distils to the summary "auth tokens rotate; TTL pinned at 900s"
    When Alice compacts the "auth-refactor" session
    Then the compaction engine's payload contains the recorded fact "the token TTL is 900 seconds"
    And the stored memory for "auth-refactor" recalls "auth tokens rotate; TTL pinned at 900s"

  # Recall is a distinct act from distillation: once an essence is on disk, `memory
  # show` reads it back without re-running the LLM. This proves the persisted
  # essence survives the compacting process's exit and is retrievable in a later
  # invocation — the actual cross-session-memory promise, minus the engine-delivery
  # surface (see honesty note 2).
  Scenario: Stored memory is recalled after the session is over
    Given the "auth-refactor" session has already been compacted to "auth tokens rotate; TTL pinned at 900s"
    When Alice shows the "auth-refactor" memory
    Then she is shown the distilled summary "auth tokens rotate; TTL pinned at 900s"

  # The listing's STATUS column is the developer's map of which sessions already
  # carry recoverable memory and which are still raw. This proves the compacted /
  # pending distinction reflects real on-disk essence state, not just row count.
  Scenario: The memory listing marks which sessions carry distilled memory
    Given a compacted session "auth-refactor" and an un-compacted session "spike"
    When Alice lists her session memory
    Then "auth-refactor" is listed as compacted
    And "spike" is listed as pending

  # DEFERRED — memory reaching a NEW session's assembled context. This is the
  # payoff a reader expects, but it is NOT the `ctxloom memory` surface: it flows
  # through the SessionStart inject-context hook / the MCP recover tools (honesty
  # note 2). Asserting it here would test a different command through this feature.
  # Left to J1's restart pattern and a future @live memory-recovery scenario rather
  # than faked in as a weak `memory`-CLI claim.
