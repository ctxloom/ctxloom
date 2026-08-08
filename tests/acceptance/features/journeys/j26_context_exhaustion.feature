Feature: Running out of context mid-task — clearing, and getting the thread back

  Dana is four hours into a refactor. The work is not finished, the reasoning
  behind it lives entirely in this conversation, and her context window is
  nearly full. She has two ways out and they are not the same tool.

  Compaction rewrites the conversation in place: the assistant summarises
  itself, in the window, using the context it is already short of, and what it
  leaves behind is what it chose to keep. Clearing throws the window away
  entirely — and the session does not end. Its transcript is still on disk, and
  is still growing. `/recover` distils that transcript in a separate process,
  against the whole record, on a fresh budget, and hands back one bounded
  artifact.

  So the trade is not "summary versus summary". It is who does the summarising,
  what they can see while doing it, and whether the original survives. After a
  compaction the detail is gone from the conversation; after a clear it is still
  on disk and can be asked for again — differently, or twice.

  # This journey owns the SAME-SESSION loop: the nudge that follows a clear, the
  # recovery that answers it, and the contrast with compaction. It does not
  # re-drive capture (j12), plain distillation (j14), or recall of an EARLIER
  # session (j23) — /clear keeps the same session alive, which is exactly why
  # recover_session and not get_previous_session is the post-clear path.
  #
  # NOT TAGGED @doc, deliberately, and not because it lacks a story. The
  # living-docs generator refuses a scenario whose assertion steps carry no
  # captured evidence, and every Then below reads the hook's envelope rather
  # than running a command of its own. Tagging it would add a second feature to
  # the refusal that already keeps the docs site from publishing
  # (taskloom subzero-avalanche). It gets its page when that is settled.

  Background:
    Given an initialized ctxloom project

  # The discovery beat. Dana does not know /recover exists, and the moment she
  # needs it is the moment she has least to work with — so ctxloom says so, on
  # the channel the user reads rather than the one the model reads.
  Scenario: Clearing tells Dana the context she just threw away can be brought back
    Given Dana has been working long enough that her context is nearly full
    When her assistant starts a fresh turn after she clears the context
    Then the command succeeds
    And Dana is told her cleared context can be brought back

  # The distinction this whole journey is about, and ctxloom can draw it because
  # the harness reports WHY a session started. After a compaction the summary is
  # already in the window: there is nothing to bring back and offering to would
  # be noise interrupting someone who has just resumed working.
  Scenario: Compacting does not offer a recovery, because nothing was thrown away
    Given Dana has been working long enough that her context is nearly full
    When her assistant starts a fresh turn after the harness compacts the conversation
    Then the command succeeds
    And Dana is told nothing about recovery

  # The negative control, without which the nudge has never been shown capable
  # of staying quiet. A session that recorded nothing has nothing to recover,
  # and promising a recovery that would come back empty is worse than silence:
  # it spends the user's one moment of attention on a dead end.
  Scenario: A cleared session that recorded nothing stays quiet rather than promising a recovery
    Given Dana's session has recorded nothing worth recovering
    When her assistant starts a fresh turn after she clears the context
    Then the command succeeds
    And Dana is told nothing about recovery

  # Same claim, different absence. A transcript that was never written at all
  # must be as quiet as one that is empty — asserting only the empty-file case
  # would leave the missing-file path free to throw or to promise.
  Scenario: A cleared session with no transcript at all also stays quiet
    When her assistant starts a fresh turn after she clears a session with no transcript at all
    Then the command succeeds
    And Dana is told nothing about recovery

  # The payoff. Recovery reads the session Dana is sitting in — not the one
  # before it — and returns what was concluded rather than what was said. The
  # marker exists only inside this session's own transcript, so an answer
  # carrying it cannot have come from anywhere else.
  Scenario: Recovery hands back this session's own thread, distilled
    Given a captured session "steady-vellum-crane" bound to a backend-native session id
    And the compaction LLM is a mock that never compresses
    When the agent calls tool "recover_session" with:
      | session_id | seeded-steady-vellum-crane |
    Then the tool call succeeds
    And the tool result field "loaded" equals "true"
    And the tool result contains "RECOVER-IDENTITY-ROUND-TRIP"
