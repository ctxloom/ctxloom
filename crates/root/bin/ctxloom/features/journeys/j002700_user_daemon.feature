# world: base
# NOT ported: nothing; the journey is new, and Go had no daemon.

@doc
Feature: One daemon of her own does ctxloom's work, wherever Alice types

  Alice works in several projects at once, often with an assistant open in
  each, and her assistants hand work to children of their own. She never
  starts or minds a server: the first ctxloom command she types starts one
  for her, quietly, and every command after it, in any project, finds that
  same one and lets it do the work. Her terminal keeps only what must be on
  her terminal: the assistant's own screen.

  It is hers alone: nothing on her machine that is not her can find it or
  speak to it as her. It may die at any moment, even killed outright, and
  she loses nothing for it: her assistants keep working, what they report
  reaches the record once, and her next command brings it back. When she
  stops working, it goes away by itself.

  Background:
    # new: the user's daemon, from Alice's side.
    Given Alice has a project that has never used ctxloom
    And Alice has exported her assistant's token
    And Alice has set ctxloom up in her project

  # Nothing serves for her yet. The first command finds nothing, starts her
  # daemon and waits for it; the next command finds it and starts nothing.
  Scenario: The first command Alice types starts her daemon, and the next ones use it
    # new: spawn on demand, then discovery.
    Given no ctxloom daemon is serving for Alice
    When Alice lists her sessions:
      """
      ctxloom session list
      """
    Then the command succeeds
    And a ctxloom daemon is serving for Alice, started by that command
    When Alice lists her sessions again:
      """
      ctxloom session list
      """
    Then the command succeeds
    And that command used the same daemon, and started none

  # Alice has an assistant open in two projects at once. Both are clients of
  # the one daemon, and each one's record stays in its own project.
  Scenario: Two interactive sessions from two projects share one daemon
    # new: one daemon per user, routing by project.
    Given Alice has a second project, "api", set up with ctxloom
    When Alice runs her assistant interactively in both projects at once:
      """
      ctxloom run
      """
    Then both sessions were served by the one daemon
    And every call each session made named its own project and its own session
    And each project lists its own session, and not the other's

  # The command Alice types for a session only puts her assistant on her
  # screen. Her daemon decided everything about it beforehand and prepared
  # its home; the command started it exactly as told.
  Scenario: Alice's daemon prepares her session, and the command she typed only starts her assistant on her terminal
    # new: the launch is the daemon's; the frontend starts the TUI.
    When Alice runs her assistant interactively, and it fires its session's hooks:
      """
      ctxloom run
      """
    Then her assistant ran on her terminal exactly as her daemon prepared it
    And the command she typed recorded nothing itself: her daemon recorded the session and prepared its session home
    And the session's transcript holds what her assistant's hooks delivered to her daemon, each once

  # A one-shot needs no terminal, so her daemon runs the assistant itself;
  # the command she typed waits for the reply and prints it.
  Scenario: Alice's daemon answers her one-shot, and the command she typed prints the reply
    # new: a one-shot is a daemon-hosted child.
    When Alice asks her assistant one thing, which her daemon runs:
      """
      ctxloom run --one-shot "Run the shell command: echo hi"
      """
    Then the command succeeds
    And the assistant's reply is printed
    And her assistant ran under her daemon, not under the command she typed

  # Alice changes her mind and presses Ctrl-C. Her daemon stops the
  # assistant, the session ends, and the command exits as a shell reports
  # the interrupt.
  Scenario: Ctrl-C stops a one-shot her daemon is running
    # new: the stop of a hosted one-shot.
    When Alice asks her assistant one thing, and presses Ctrl-C while it works:
      """
      ctxloom run --one-shot "Run the shell command: sleep 30"
      """
    Then her daemon stopped her assistant, and it was gone before the command exited
    And ctxloom exited 130, saying the run was stopped by SIGINT
    And after it ended, its session is listed as ended under its own name

  # Alice hands a review to a child of her running session, gives it a
  # follow-up when it is done, and closes it when she no longer needs it.
  Scenario: Alice launches a child of her session, sends it more work, and closes it
    # new: the child lifecycle, as the user.
    Given Alice's session "amber-quiet-heron" is running
    When Alice hands a review to a child of her session:
      """
      ctxloom session launch amber-quiet-heron --name brisk-copper-moth "review the change"
      """
    Then the command succeeds
    And "brisk-copper-moth" is listed as a child of "amber-quiet-heron", run by her daemon
    And "brisk-copper-moth"'s first turn was "review the change"
    When Alice sends the child a follow-up:
      """
      ctxloom session send brisk-copper-moth wake "now check the tests too"
      """
    Then the command succeeds
    And "brisk-copper-moth"'s next turn was "now check the tests too"
    When Alice closes the child:
      """
      ctxloom session close brisk-copper-moth
      """
    Then the command succeeds
    And "brisk-copper-moth" is listed as closed, and its assistant has exited
    And nothing "brisk-copper-moth" was launched with is left at rest

  # The child wants to run something its permissions say to ask about. No
  # one is at its terminal, so the ask comes to Alice, wherever she types;
  # she allows it, and the child goes on.
  Scenario: Alice answers her child's permission ask from any terminal
    # new: approvals.
    Given Alice's session "amber-quiet-heron" is running, with a child "brisk-copper-moth" working for it
    And "brisk-copper-moth" asks to run "Bash" with "make release"
    When Alice looks at what her session's children are waiting on:
      """
      ctxloom session approvals amber-quiet-heron
      """
    Then the command succeeds
    And she sees "brisk-copper-moth" asking to run "Bash" with "make release"
    When Alice allows it:
      """
      ctxloom session approvals answer brisk-copper-moth allow
      """
    Then the command succeeds
    And "brisk-copper-moth" ran "make release", and nothing waits for her any more

  # Her daemon is killed outright while her assistant works, and her
  # assistant's hooks go on firing. Her next command brings a daemon back:
  # the session never stopped, everything it reported reached the record
  # once, and the child picks up its own conversation where it was.
  Scenario: A daemon killed mid-session comes back on the next command, and nothing is lost
    # new: the daemon's crash tolerance.
    Given Alice's session "amber-quiet-heron" is running, with a child "brisk-copper-moth" working for it
    When Alice's daemon is killed outright while her assistant works, and her assistant goes on firing its hooks
    And Alice lists her sessions:
      """
      ctxloom session list
      """
    Then the command succeeds
    And a new ctxloom daemon is serving for Alice
    And "amber-quiet-heron" is listed as live, and its assistant never stopped
    And "amber-quiet-heron" found the new daemon and delivered again what the old one had not answered, and the record holds each once
    And "brisk-copper-moth" is working again in its own conversation, with what it was launched with

  # Alice stops working. With nothing running, nothing hosted and no command
  # open, her daemon goes away by itself; her next command starts a new one,
  # which knows her by a new credential.
  Scenario: An idle daemon exits, and the next command starts a new one
    # new: the idle exit.
    Given Alice's daemon has nothing to do: no session runs, no child is hosted and no command is open
    When her daemon's idle time passes
    Then her daemon has exited, and nothing says it serves
    When Alice lists her sessions:
      """
      ctxloom session list
      """
    Then the command succeeds
    And a ctxloom daemon is serving for Alice, started by that command
    And it proves itself with a new certificate, and knows her by a new credential
