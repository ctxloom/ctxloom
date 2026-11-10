# world: base
# Ported from journeys/j001000_transcript_capture.feature (rev f36e8dfb2)
# NOT ported: the @live scenario (it needs a real engine); the scenarios that
#   follow it are new, and prove the same capture with stub engines: a claude
#   that fires the hooks the session registered, and an opencode that runs the
#   plugin its session home holds (under node).

@doc
Feature: Cross-engine transcript capture — every engine's conversation becomes one record you own

  Alice drives whichever engine she likes, through its own native screen, and
  still keeps a lasting memory of what happened. Each engine keeps its
  conversations in its own private store, in its own format, and that store
  changes or disappears when the engine does.

  ctxloom keeps its own record instead. Whatever engine Alice ran, when the
  session ends its conversation is in ctxloom's record of the session, in one
  form, so reading it back, searching it or resuming it later never depends on
  which engine she happened to use that day.

  Nobody runs an import. The record is there when the session ends, and it is
  brought up to date whenever Alice reads or resumes the session, even one
  that did not end cleanly.

  Background:
    Given an initialized ctxloom project

  # Only a real engine, driven through its own screen, can show this end to
  # end.
  @live @pending
  Scenario Outline: An interactive engine run's own log is captured when it exits
    # substitution: Alice reads the session back through ctxloom, as she would, instead of the record's lines being inspected.
    Given a real <agent> agent is available
    When Alice runs her <agent> agent interactively to completion:
      """
      ctxloom run --agent <agent> "leave a mark"
      """
    Then ctxloom's record of that session holds the conversation she just had
    And the record names "<engine>" as the engine she had it with

    Examples:
      | agent       | engine      |
      | claude      | claude-code |

  # opencode has no hook commands: it is heard through the plugin ctxloom
  # writes into its session home, which forwards opencode's own bus events, one
  # at a time and in order, to `ctxloom hook opencode execute <Event>`. The
  # stub opencode loads that plugin (under node, as opencode loads it under
  # Bun) and feeds it the events a real opencode emits for a prompt, a tool
  # call and a reply; its turn also idles, which ends the turn a second time.
  # opencode says nothing of a session's end: ctxloom records it at the exit.
  Scenario: The events an interactive opencode session emits become the session's transcript
    # new: ruling 58, with a stub opencode on PATH running the real plugin.
    Given Alice has a project that has never used ctxloom
    And Alice has set ctxloom up in her project
    When Alice runs her assistant on opencode interactively, and its plugin forwards the session's events:
      """
      ctxloom run --engine opencode
      """
    Then opencode ran in its session home, with what its plugin's hooks need to reach ctxloom and without OPENCODE_PURE
    And every hook the plugin ran left nothing unrecorded
    And the session's transcript holds her prompt, the tool call with its status, the assistant's words, the turn's end and the session's end at the exit, each once

  # The interactive engine reports what happens through the hooks its session
  # home registers; the stub engine fires them as separate processes, with the
  # environment a real engine gives its hooks, and each one twice.
  Scenario: The hooks an interactive engine fires become the session's transcript
    # new: the end-to-end proof of hook capture, with a stub engine on PATH.
    # The project is hers, never used before: the scenario starts from setup.
    Given Alice has a project that has never used ctxloom
    And Alice has set ctxloom up in her project
    When Alice runs her assistant interactively, and it fires its session's hooks twice each:
      """
      ctxloom run
      """
    Then the assistant's environment held what its hooks need to reach ctxloom
    And every hook it fired answered, said nothing was left unrecorded, and exited 0
    And the session's transcript holds her prompt, the assistant's words in order, the tool call as a digest, the turn's end and the session's end, each once
    And the transcript was written when the run ended, not while the engine ran
    And a hook fired after the run has ended is refused without failing, and adds nothing to the transcript
