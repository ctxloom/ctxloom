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

  ctxloom keeps its own record instead. Whatever engine Alice ran, its
  conversation is in ctxloom's record of the session, in one form, so reading
  it back, searching it or resuming it later never depends on which engine she
  happened to use that day.

  Nobody runs an import, and no file is written for it: each thing the engine
  reports is recorded as it happens, so the record can be read while the
  session is still running, from another terminal, as well as after it ends.

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
    And while the engine ran, its session's transcript already held what its hooks had delivered
    And the session's directory holds its session home and nothing projected from its events
    And a hook fired after the run has ended is refused without failing, and adds nothing to the transcript

  # A session opened with a prompt on its command line: the engine submits it
  # as its first prompt, and its UserPromptSubmit hook records it. Hooks are
  # the only source of the conversation (ruling 26's principle), so the
  # launcher does not record it a second time.
  Scenario: An interactive session opened with a prompt records that prompt once
    # new: the opening prompt was recorded twice, by the launcher and the hook.
    Given Alice has a project that has never used ctxloom
    And Alice has set ctxloom up in her project
    When Alice runs her assistant interactively on an opening prompt, and it fires its session's hooks twice each:
      """
      ctxloom run "Run the shell command: echo hi"
      """
    Then the assistant's environment held what its hooks need to reach ctxloom
    And every hook it fired answered, said nothing was left unrecorded, and exited 0
    And the session's transcript holds her prompt, the assistant's words in order, the tool call as a digest, the turn's end and the session's end, each once

  # A one-shot's assistant is a child turn (`claude -p`), heard through the
  # same hooks (owner ruling 51): its session home registers the per-turn
  # events, which call back to Alice's daemon, as every hook does. The stub
  # engine fires them as the interactive one does, each twice.
  Scenario: The hooks a one-shot's assistant fires become the session's transcript
    # new: hook capture of a child turn, with a stub engine on PATH.
    Given Alice has a project that has never used ctxloom
    And Alice has exported her assistant's token
    And Alice has set ctxloom up in her project
    When Alice asks her assistant one thing, and it fires its turn's hooks twice each:
      """
      ctxloom run --one-shot "Run the shell command: echo hi"
      """
    Then the assistant's reply is printed
    And the assistant's turn held what its hooks need to reach ctxloom, and not the primary's token
    And the assistant's turn registered the hooks of a turn and not those of a session
    And every hook it fired answered, said nothing was left unrecorded, and exited 0
    And the session's transcript holds her prompt, the assistant's words in order, the tool call as a digest and the turn's end, each once
    And the session's directory holds its session home and nothing projected from its events

  # Hooks are the only source of a one-shot's conversation: one whose hooks
  # never fire keeps none (the accepted cost), and still answers.
  Scenario: A one-shot whose hooks never fire still answers, and keeps no conversation
    # new: the cost of ruling 26, stated.
    Given Alice has a project that has never used ctxloom
    And Alice has exported her assistant's token
    And Alice has set ctxloom up in her project
    When Alice asks her assistant one thing, and it fires no hooks:
      """
      ctxloom run --one-shot "Run the shell command: echo hi"
      """
    Then the assistant's reply is printed
    And the session's transcript holds no prompt and no words of the assistant's

  # Ruling 114: a ctxloom asked to stop while its assistant works (a SIGTERM,
  # a SIGHUP, or a SIGINT for a one-shot) passes the stop on to the assistant,
  # gives it a bounded grace to exit, records the session's end and exits as
  # a shell reports the signal, instead of dying and leaving the assistant
  # working with no one listening.
  Scenario: A one-shot asked to stop stops its assistant first and ends its session
    # new: signal forwarding, with a stub engine on PATH.
    Given Alice has a project that has never used ctxloom
    And Alice has exported her assistant's token
    And Alice has set ctxloom up in her project
    When Alice asks her assistant one thing, and stops ctxloom with a SIGTERM while the assistant works:
      """
      ctxloom run --one-shot "Run the shell command: sleep 30"
      """
    Then the assistant was asked to stop, and was gone before ctxloom exited
    And ctxloom exited 143, saying the run was stopped by SIGTERM
    And after it ended, its session is listed as ended under its own name
