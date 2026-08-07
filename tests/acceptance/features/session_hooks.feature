Feature: The machine callbacks every session fires — do they deliver, and do they stay on their channel?

  Four hidden commands run on every ctxloom session, invoked by the host
  engine rather than by a person: `hook inject-context` hands the engine the
  project's assembled context at SessionStart, `hook session-bind` records
  which backend session belongs to which harp, `hook stamp-plan` marks a plan
  file with the session that edited it, and `hook hud` renders the statusline.
  Nobody types any of them, so nobody notices when one stops working.

  They share a contract that makes them unusually easy to break quietly. A
  hook must NEVER fail the host engine's startup, so every one of them warns
  and exits 0 on any failure it can foresee — a missing context file, an
  unparseable payload, an absent harp. Exit code therefore says nothing at
  all here, and a hook that delivers nothing is indistinguishable from a hook
  that delivered, unless something reads what it actually produced.

  The second half of the contract is the CHANNEL: stdout is the engine's
  input, and a diagnostic printed there is not a warning the user sees but
  corruption of the payload the engine parses. So the stdout assertions below
  are made by parsing stdout ALONE as JSON, never the combined stream — a
  test reading combined output would pass happily while ctxloom fed an engine
  a warning line spliced into its context envelope.

  These were invisible to the coverage gate itself until recently: registered
  Hidden, they failed the walk that discovers leaves, so nothing could even
  report them as uncovered. They are listed explicitly as required leaves now,
  and this feature is what answers that requirement.

  Background:
    Given an initialized ctxloom project

  # The claim is the CONTENT, not the envelope. The envelope is a static
  # wrapper the command emits unconditionally — even for a context file that
  # does not exist — so asserting its shape proves only that the command ran.
  # The marker text exists nowhere else in the project.
  Scenario: The SessionStart hook hands the engine the project's own context
    Given the project already has the file ".ctxloom/cache/context/abc123.md":
      """
      HOOK-DELIVERED-THIS-EXACT-TEXT and nothing else in this project says so.
      """
    When I run "ctxloom hook inject-context abc123" with input:
      """
      {"session_id":"vendor-session-1","hook_event_name":"SessionStart"}
      """
    Then the command succeeds
    And the hook's additionalContext contains "HOOK-DELIVERED-THIS-EXACT-TEXT"

  # The failure path, which is the shape this whole feature exists for: a
  # context file that is missing must still leave the engine with parseable
  # JSON and a zero exit, and must say WHY on the diagnostic channel. Asserting
  # the empty context is what stops this from being satisfied by a hook that
  # delivers nothing in the success case too.
  Scenario: A missing context file still leaves the engine parseable JSON, and says why
    When I run "ctxloom hook inject-context no-such-hash" with input:
      """
      {"session_id":"vendor-session-1","hook_event_name":"SessionStart"}
      """
    Then the command succeeds
    And the hook's additionalContext is empty
    And the output contains "failed to read context file"

  # Two effects from one invocation, and they fail independently: the marker
  # is the only statement of harp ownership that does not depend on the index,
  # while the bind is the index entry itself. A regression in either one alone
  # leaves the other passing.
  Scenario: Binding a session records the harp both in the transcript and in the index
    Given the session harp is "brisk-copper-moth"
    And the session index has an unbound entry for harp "brisk-copper-moth"
    When I run "ctxloom hook session-bind" with input:
      """
      {"session_id":"vendor-session-42","transcript_path":"/tmp/vendor/transcript.jsonl","hook_event_name":"SessionStart"}
      """
    Then the command succeeds
    And the hook's additionalContext contains "brisk-copper-moth"
    And the session index binds harp "brisk-copper-moth" to session "vendor-session-42"

  # With no harp there is nothing truthful to emit, so the hook must emit
  # NOTHING on stdout rather than a marker naming an empty harp — an engine
  # would inject that verbatim into the transcript. The warning is what turns
  # an untraceable session into a diagnosable one.
  Scenario: With no active harp the hook stays silent on stdout and warns instead
    When I run "ctxloom hook session-bind" with input:
      """
      {"session_id":"vendor-session-42","hook_event_name":"SessionStart"}
      """
    Then the command succeeds
    And the hook writes nothing to stdout
    And the output contains "no usable harp"

  # The payload names the edited file the way a host engine's file-edit hook
  # does. Production sends an ABSOLUTE path; this sends a relative one, which
  # takes the same leg of IsPlanFile — the basename test, which fires on
  # "plan-of-attack.md" either way — so the path shape is not what is under
  # test here and a relative path keeps the fixture readable.
  Scenario: Editing a plan file stamps the editing session into its frontmatter
    Given the session harp is "brisk-copper-moth"
    And the project already has the file "plan-of-attack.md":
      """
      ---
      title: rollout
      ---

      Step one.
      """
    When I run "ctxloom hook stamp-plan" with input:
      """
      {"tool_input":{"file_path":"plan-of-attack.md"}}
      """
    Then the command succeeds
    And the file "plan-of-attack.md" contains "brisk-copper-moth"

  # The hook fires on EVERY file edit, so the negative case is the common one
  # by far: an ordinary file must come back untouched. Without this, a stamper
  # that ignored IsPlanFile entirely would pass the scenario above and quietly
  # write frontmatter into every source file the session edited.
  Scenario: Editing an ordinary file leaves it untouched
    Given the session harp is "brisk-copper-moth"
    And the project already has the file "notes.md":
      """
      Just notes.
      """
    When I run "ctxloom hook stamp-plan" with input:
      """
      {"tool_input":{"file_path":"notes.md"}}
      """
    Then the command succeeds
    And the file "notes.md" does not contain "brisk-copper-moth"
    And the file "notes.md" contains "Just notes."

  # The statusline combines what the ENGINE reported (model, context usage)
  # with what CTXLOOM knows (the session's harp). Asserting one of each is the
  # point: a HUD that rendered only its own half would still look plausible.
  Scenario: The statusline reports the engine's session and ctxloom's own
    Given the session harp is "brisk-copper-moth"
    When I run "ctxloom hook hud" with input:
      """
      {"model":{"display_name":"Opus 5"},"context_window":{"used_percentage":42}}
      """
    Then the command succeeds
    And the output contains "Opus 5"
    And the output contains "42%"
    And the output contains "brisk-copper-moth"
