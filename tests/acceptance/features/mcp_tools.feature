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
  # pass. The `contains "fragments_loaded"` line those two replaced is gone:
  # it could not fail independently of them, and a line that reads as an
  # assertion while being incapable of failing is worse than no line. (An
  # exact-list assertion on the field was considered and rejected — the
  # always-on builtin companion fragments make its contents depend on what the
  # machine has installed, which is how a green-for-the-wrong-reason scenario
  # is born.)
  Scenario: Assemble context from a profile
    Given an initialized ctxloom project
    And a bundle "demo" exists
    And a fragment "testing" in bundle "demo" exists
    And a profile "dev" with bundle "demo"
    When the agent calls tool "assemble_context" with:
      | profile | dev |
    Then the tool call succeeds
    And the tool result contains "demo#fragments/testing"
    And the tool result contains "FRAGMENT-BODY-testing"

  # SearchContentResult.Query has no omitempty, so the QUERY is echoed into
  # the envelope whether or not the search matched anything: `the tool result
  # contains "testing"` was satisfied by {"results":null,"count":0,
  # "query":"testing"} — a search that found nothing. count is the
  # load-bearing half (SearchResult carries Type/Name/Source but no body), so
  # this asserts it, exactly as the sibling search_library scenario below does.
  Scenario: Search content over MCP
    Given an initialized ctxloom project
    And a bundle "demo" exists
    And a fragment "testing" in bundle "demo" exists
    When the agent calls tool "search_content" with:
      | query | testing |
    Then the tool call succeeds
    And the tool result field "count" equals "1"
    And the tool result contains "demo"

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
  # And then it has to ask WHERE. The handler used to key everything — the
  # heal, the cache read, the distillation, and the essence's own path — off
  # the CALLING session's harp, consulting session_id only in a fallback
  # branch, and even there filing the result under the caller. So a call
  # naming another session returned that id in its result while writing that
  # session's distilled memory under the MCP server's own harp: right content,
  # wrong session, no error anywhere. A later load_session for the named
  # session reads ~/.ctxloom/sessions/<harp>/essence.md, finds nothing, and
  # re-derives the whole distillation — every explicit compact_session call
  # paying for work that is then thrown away — while the caller's own essence
  # is quietly overwritten by a session that is not theirs.
  #
  # The two harps here are deliberately DIFFERENT: the caller is
  # host-caller-thistle and the session it names is quiet-ember-drift,
  # addressed by the backend-native id its index entry binds. A scenario where
  # the caller compacts itself is satisfied by the defect just as well as by
  # the fix, because both harps are the same string.
  #
  # The distillation is proven END TO END rather than by the essence's content
  # alone, because either half can be faked on its own: RECOVER-IDENTITY-
  # ROUND-TRIP lives only in the seeded transcript, so the distiller RECEIVING
  # it proves the right session's history was read; the mock's canned reply
  # appears nowhere else, so the essence CARRYING it proves that distiller's
  # output is what landed at the reported path — rather than a placeholder
  # dump from an empty session, which fills in every bookkeeping field just as
  # convincingly.
  Scenario: compact_session files the essence under the session it was handed, not the caller's
    Given an initialized ctxloom project
    And the session harp is "host-caller-thistle"
    And a captured session "quiet-ember-drift" bound to a backend-native session id
    And the mock LLM responds "COMPACT-DISTILLED-THE-NAMED-SESSION"
    When the agent calls tool "compact_session" with:
      | session_id | seeded-quiet-ember-drift |
    Then the tool call succeeds
    And the tool result field "session_id" equals "seeded-quiet-ember-drift"
    And the essence the tool reports writing is filed under session "quiet-ember-drift"
    And no essence was written under session "host-caller-thistle"
    And the mock recorded input contains "RECOVER-IDENTITY-ROUND-TRIP"
    And the essence the tool reports writing contains "COMPACT-DISTILLED-THE-NAMED-SESSION"

  # The CACHE hit asks the same identity question of a different line of code.
  # A cached compact_session returns early, having run no distillation to take
  # an identity from, so it composes one itself — and that line used to compose
  # it from the CALLER's harp. An agent asking twice about another session was
  # told, the second time, that it had compacted itself: a well-formed answer
  # naming the wrong session, and the branch a scenario that only ever calls
  # once can never reach.
  Scenario: a cached compact_session still answers for the session it was asked about
    Given an initialized ctxloom project
    And the session harp is "host-caller-thistle"
    And a captured session "quiet-ember-drift" bound to a backend-native session id
    And the mock LLM responds "COMPACT-DISTILLED-THE-NAMED-SESSION"
    When the agent calls tool "compact_session" with:
      | session_id | seeded-quiet-ember-drift |
    And the agent calls tool "compact_session" with:
      | session_id | seeded-quiet-ember-drift |
    Then the tool call succeeds
    And the tool result field "was_cached" equals "true"
    And the tool result field "session_id" equals "seeded-quiet-ember-drift"

  # THE OTHER LEG of the same resolution, and a genuinely different line of
  # code. session_id is whatever the caller has to hand, and the two forms it
  # accepts do not share a lookup: operations.GetSession matches on HarpName
  # alone (sessions.Manager.Find), so a caller that names the HARP is answered
  # by compactionTargetHarp's first branch and never reaches
  # operations.HarpForSession, which is the only branch the scenario above
  # exercises. A harp-addressed call was therefore resolved by code no
  # scenario ran.
  #
  # Same two-harp discipline as above, and for the same reason: the caller is
  # host-caller-thistle and the session it names is quiet-ember-drift, so a
  # branch that answers with the caller's own identity — or with the entry's
  # backend-native id rather than its harp — files the essence somewhere
  # quiet-ember-drift will never read it from, and every field of the result
  # envelope still looks right.
  Scenario: compact_session resolves a session named by its harp, not only by its bound id
    Given an initialized ctxloom project
    And the session harp is "host-caller-thistle"
    And a captured session "quiet-ember-drift" bound to a backend-native session id
    And the mock LLM responds "COMPACT-DISTILLED-THE-HARP-NAMED-SESSION"
    When the agent calls tool "compact_session" with:
      | session_id | quiet-ember-drift |
    Then the tool call succeeds
    And the essence the tool reports writing is filed under session "quiet-ember-drift"
    And no essence was written under session "host-caller-thistle"
    And the mock recorded input contains "RECOVER-IDENTITY-ROUND-TRIP"
    And the essence the tool reports writing contains "COMPACT-DISTILLED-THE-HARP-NAMED-SESSION"

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

  # evaluate_triggers asks a model whether each Deferred task's revive
  # condition has fired, with the repository's recent commits as evidence. Its
  # result is mostly COUNTERS — evaluated, cache_hits, cache_misses, omitted —
  # and a run that produced no usable verdict fills those in exactly as
  # convincingly as one that worked. The tool's own `omitted` field exists
  # because a model chunk can come back well-formed while silently not
  # mentioning a task, so the claim here is the verdict's ATTRIBUTION, not the
  # count.
  #
  # Single-round only, deliberately: a "needs-investigation" answer carries
  # follow-up queries that ctxloom runs before asking again, and a canned
  # response would hand back the same array in round two — testing an
  # escalation loop against a stub that cannot escalate. That path needs a
  # fixture that can answer differently the second time.
  Scenario: A deferred task's trigger verdict reaches the caller, attributed to it
    Given an initialized ctxloom project
    And the mock LLM responds "placeholder, replaced once the task's harp exists"
    And a fresh taskloom store
    And a deferred task "revisit the cache eviction policy" waiting on "the v2 API ships"
    And the trigger model answers "fired" for that task
    When the agent calls tool "evaluate_triggers"
    Then the tool call succeeds
    And the tool result field "evaluated" equals "1"
    And the verdict is attributed to that task, with outcome "fired"
    And the tool result contains "TRIGGER-VERDICT-REACHED-THE-CALLER"

  # The empty case, which must NOT reach the model at all: with nothing
  # deferred there is nothing to judge, and a run that called the LLM anyway
  # would burn a request per invocation on a backlog that asks no questions.
  # evaluated=0 is the observable for that.
  Scenario: With nothing deferred, evaluate_triggers judges nothing
    Given an initialized ctxloom project
    And a fresh taskloom store
    When the agent calls tool "evaluate_triggers"
    Then the tool call succeeds
    And the tool result field "evaluated" equals "0"
