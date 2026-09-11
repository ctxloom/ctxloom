Feature: The TurnEnd hook captures what the agent was about to do next, and the last turn wins

  `ctxloom hook next-step` runs at the end of every turn, invoked by the host
  engine, and stores the turn's closing assistant message under the session's
  harp. Nothing reads that file during the session; it exists for the
  distiller, which uses it as a task hint (see distill_task_hint.feature) so
  the essence keeps what the resuming session will need.

  Like every hook in session_hooks.feature it NEVER fails the turn: it exits
  0 whatever went wrong, and says why on the diagnostic channel. So exit
  status proves nothing here, and a capture that wrote zero bytes looks
  exactly like one that worked unless something reads the file back. Every
  scenario below asserts the CAPTURED CONTENT, and every refusal is asserted
  as the survival of an earlier capture rather than as the absence of a file
  — an absent file is also what a fixture that never captured anything looks
  like.

  Background:
    Given an initialized ctxloom project

  # The claim is the text, verbatim. A hook that resolved the transcript, read
  # it, and then stored the wrong entry (the user's prompt, an earlier reply)
  # would still exit 0 with a file on disk.
  Scenario: A finished turn leaves its closing statement under the harp
    Given the session harp is "brisk-copper-moth"
    And the session index records harp "brisk-copper-moth" on engine "claude-code"
    And a "claude-code" transcript at "turn.jsonl" whose turn ends with:
      """
      Next I will run the lint gate and then merge the branch.
      """
    When I run "ctxloom hook next-step" with input:
      """
      {"session_id":"vendor-session-1","hook_event_name":"Stop","transcript_path":"$PROJECT_DIR/turn.jsonl"}
      """
    Then the command succeeds
    And the captured next step for harp "brisk-copper-moth" is:
      """
      Next I will run the lint gate and then merge the branch.
      """

  # The overwrite IS the mechanism. Nothing detects the session ending; the
  # last turn's statement survives only because every turn replaces the one
  # before it. A capture that skipped the write when a file already existed
  # would leave the FIRST turn's intention as the session's last word.
  Scenario: A later turn replaces the earlier turn's next step
    Given the session harp is "brisk-copper-moth"
    And the session index records harp "brisk-copper-moth" on engine "claude-code"
    And a "claude-code" transcript at "turn-one.jsonl" whose turn ends with:
      """
      First I will read the failing test.
      """
    And a "claude-code" transcript at "turn-two.jsonl" whose turn ends with:
      """
      Now I will fix the off-by-one in the paginator.
      """
    When I run "ctxloom hook next-step" with input:
      """
      {"session_id":"vendor-session-1","hook_event_name":"Stop","transcript_path":"$PROJECT_DIR/turn-one.jsonl"}
      """
    And I run "ctxloom hook next-step" with input:
      """
      {"session_id":"vendor-session-1","hook_event_name":"Stop","transcript_path":"$PROJECT_DIR/turn-two.jsonl"}
      """
    Then the command succeeds
    And the captured next step for harp "brisk-copper-moth" is:
      """
      Now I will fix the off-by-one in the paginator.
      """

  # The stored text is bounded (memory.MaxNextStepBytes). The hook copies an
  # assistant message verbatim, so a turn that ends by pasting a whole file
  # into its reply would otherwise leave that file on disk and hand it to the
  # distiller as a "hint" that swamps the prompt it is meant to steer.
  Scenario: A closing statement larger than the bound is stored truncated, not whole
    Given the session harp is "brisk-copper-moth"
    And the session index records harp "brisk-copper-moth" on engine "claude-code"
    And a "claude-code" transcript at "long.jsonl" whose turn ends with a closing text longer than the next-step bound
    When I run "ctxloom hook next-step" with input:
      """
      {"session_id":"vendor-session-1","hook_event_name":"Stop","transcript_path":"$PROJECT_DIR/long.jsonl"}
      """
    Then the command succeeds
    And the captured next step for harp "brisk-copper-moth" is the bounded head of that closing text

  # A turn that had nothing to say must not erase the turn that did. The file
  # is overwritten every turn, so accepting an empty write would replace a good
  # capture with one that reads, downstream, as "this session intends nothing"
  # — indistinguishable from a session that never captured a next step at all.
  Scenario: A turn whose closing message is only whitespace leaves the previous capture standing
    Given the session harp is "brisk-copper-moth"
    And the session index records harp "brisk-copper-moth" on engine "claude-code"
    And a "claude-code" transcript at "turn-one.jsonl" whose turn ends with:
      """
      Then I will bisect the regression.
      """
    And a "claude-code" transcript at "blank.jsonl" whose turn ends with only whitespace
    When I run "ctxloom hook next-step" with input:
      """
      {"session_id":"vendor-session-1","hook_event_name":"Stop","transcript_path":"$PROJECT_DIR/turn-one.jsonl"}
      """
    And I run "ctxloom hook next-step" with input:
      """
      {"session_id":"vendor-session-1","hook_event_name":"Stop","transcript_path":"$PROJECT_DIR/blank.jsonl"}
      """
    Then the command succeeds
    And the captured next step for harp "brisk-copper-moth" is:
      """
      Then I will bisect the regression.
      """

  # Capture is routed by the session's OWN engine, through the same reader
  # registry that converts its transcript. The hook is installed on every
  # hooking engine, and a capture that assumed one engine's format would fire
  # every turn on the others and store nothing. The mock engine's transcript
  # shares no line shape with claude-code's, so this passes only if the
  # session's recorded engine chose the reader.
  Scenario: A session on another engine is captured through that engine's own reader
    Given the session harp is "brisk-copper-moth"
    And the session index records harp "brisk-copper-moth" on engine "mock"
    And a "mock" transcript at "turn.jsonl" whose turn ends with:
      """
      Next I will wire the mock reader's next step through.
      """
    When I run "ctxloom hook next-step" with input:
      """
      {"session_id":"vendor-session-1","hook_event_name":"Stop","transcript_path":"$PROJECT_DIR/turn.jsonl"}
      """
    Then the command succeeds
    And the captured next step for harp "brisk-copper-moth" is:
      """
      Next I will wire the mock reader's next step through.
      """

  # A session whose engine version was never recorded is REFUSED rather than
  # read with a guessed reader: a guessed parser hands the distiller a hint
  # that looks fine and is not. The first turn is captured with the version on
  # record so the refusal is measured against a capture that demonstrably
  # works — a bare "no file" would also be satisfied by a fixture that could
  # never capture at all.
  Scenario: A session with no recorded engine version is refused, and the earlier capture survives
    Given the session harp is "brisk-copper-moth"
    And the session index records harp "brisk-copper-moth" on engine "claude-code"
    And a "claude-code" transcript at "turn-one.jsonl" whose turn ends with:
      """
      I will capture this one under a known engine version.
      """
    And a "claude-code" transcript at "turn-two.jsonl" whose turn ends with:
      """
      This must never be stored: nobody knows which reader to use.
      """
    When I run "ctxloom hook next-step" with input:
      """
      {"session_id":"vendor-session-1","hook_event_name":"Stop","transcript_path":"$PROJECT_DIR/turn-one.jsonl"}
      """
    Given the session index records harp "brisk-copper-moth" on engine "claude-code" with no engine version
    When I run "ctxloom hook next-step" with input:
      """
      {"session_id":"vendor-session-1","hook_event_name":"Stop","transcript_path":"$PROJECT_DIR/turn-two.jsonl"}
      """
    Then the command succeeds
    And the captured next step for harp "brisk-copper-moth" is:
      """
      I will capture this one under a known engine version.
      """
