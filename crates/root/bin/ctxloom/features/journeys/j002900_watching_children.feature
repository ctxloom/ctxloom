# world: base
# NOT ported: nothing; the journey is new, and Go had no daemon to watch.
#
# DRAFT: the whole journey is a draft for the owner's review. Its steps are
# new and undefined, so every scenario is red until `ctxloom top` is built.

@doc
Feature: Alice watches what her daemon hosts, and answers a child's ask from another terminal

  Alice's assistant hands work to children of its own, and her daemon runs
  them while she does something else. She wants one place to look: which
  sessions are running, whose child each one is, which of them is stuck
  waiting for her to say yes, and what each is doing right now.

  `ctxloom top` is that place, and it works like her email client, with
  sessions for messages. At a terminal it is a full screen: an inbox on
  top, one row per session, sortable and filterable, the selected row
  highlighted; and below it the reading pane of that row: the permission
  asks waiting on her, which she answers there, and the session's log. Off a
  terminal, or with `--format json`, it prints the same news one JSON line
  at a time, for her editor's plugin to follow. The plugin need not follow
  everything: `ctxloom top <harp>` follows one session, and `ctxloom top
  <harp> --tree` a session and everything under it.

  She answers as herself: the same answer `ctxloom session approvals
  answer` gives, from the screen or from any other terminal. Nothing in
  `top` sends, stops or closes a session; that stays with the session verbs.

  Background:
    # new: `ctxloom top`, from Alice's side.
    Given Alice has a project that has never used ctxloom
    And Alice has exported her assistant's token
    And Alice has set ctxloom up in her project
    And Alice's orchestrator "amber-quiet-heron" has launched "brisk-copper-moth" on "reviewer" and "quiet-silver-fern" on "fixer"

  # Alice opens it and sees her orchestrator with its two children under it,
  # each with the agent, engine and state her daemon holds for it.
  Scenario: Alice opens top and sees her sessions as a tree, one row each
    # new: the table, owner and children.
    When Alice opens `ctxloom top` at her terminal
    Then the table's top part has a row for "amber-quiet-heron" with its children "brisk-copper-moth" and "quiet-silver-fern" under it
    And each row shows its state, its agent, its engine and its owner
    And one row is selected, and the bottom part shows that session's asks and log

  # A child's engine wants to run a command that its permissions say to ask
  # about. The ask shows up on the child's row and in its detail; Alice
  # answers it from a second terminal, and the first screen sees it settle.
  Scenario: A child's ask appears in top, and Alice answers it from another terminal
    # new: an ask seen in top, answered with `session approvals answer`.
    Given Alice has `ctxloom top` open at her terminal
    When the engine of "brisk-copper-moth" asks to run "Bash" with "rm -rf build/"
    Then the row of "brisk-copper-moth" shows one ask waiting
    When Alice selects "brisk-copper-moth"
    Then the bottom part lists the ask, what it would run, and the options "allow-once", "allow-session", "allow-always" and "deny"
    When Alice answers it from another terminal:
      """
      ctxloom session approvals answer brisk-copper-moth allow-once
      """
    Then the command succeeds
    And her first screen shows the ask settled, answered "allow-once", and no ask waiting on the row
    And the child's engine was told to run the tool

  # She would rather not leave the screen: she answers there.
  Scenario: Alice answers a child's ask on the top screen itself
    # new: the answer from within top, as the user.
    Given Alice has `ctxloom top` open at her terminal, with "brisk-copper-moth" selected
    And the engine of "brisk-copper-moth" has asked to run "Bash" with "make release"
    When Alice chooses "deny" for the ask on the screen
    Then the bottom part shows the ask settled, answered "deny", by her
    And the child's engine was told not to run the tool

  # What a session is doing is its log: what it said, and what her daemon did
  # with it, in the order they happened.
  Scenario: The selected session's log shows its words and what her daemon did
    # new: the log pane, transcript lines beside the daemon's events.
    Given Alice has `ctxloom top` open at her terminal, with "quiet-silver-fern" selected
    When "quiet-silver-fern" says "the migration is reversible" and its run then ends
    Then the log pane shows what it said, then that its run ended, in that order

  # Nobody answers, so the ask runs out, and top says so rather than leaving
  # it on the screen.
  Scenario: An ask nobody answers expires on the screen
    # new: an ask expiring seen in top.
    Given the engine "claude-code" is set to ask for at most 2 seconds
    And Alice has `ctxloom top` open at her terminal
    And the engine of "brisk-copper-moth" has asked to run "Bash" with "rm -rf build/"
    When the ask's time runs out
    Then her screen shows the ask expired, and no ask waiting on the row of "brisk-copper-moth"

  # Her editor shows only the session she is working with: it asks for that
  # one, and hears nothing of her others.
  Scenario: Alice's editor follows one session alone
    # new: the scoped watch.
    When her editor's plugin runs `ctxloom top brisk-copper-moth --format json` and reads it as it streams:
      """
      ctxloom top brisk-copper-moth --format json
      """
    Then its first line is a snapshot holding "brisk-copper-moth" alone
    When the engine of "quiet-silver-fern" asks to run "Bash" with "make release"
    And the engine of "brisk-copper-moth" asks to run "Bash" with "rm -rf build/"
    Then the next line says an ask arrived for "brisk-copper-moth", and none was said of "quiet-silver-fern"

  # Her editor's plugin follows the same news, a line at a time.
  Scenario: Alice's editor follows the same news as JSON lines
    # new: the plugin's stream.
    When her editor's plugin runs `ctxloom top --format json` and reads it as it streams:
      """
      ctxloom top --format json
      """
    Then its first line is a snapshot of her sessions
    When the engine of "brisk-copper-moth" asks to run "Bash" with "rm -rf build/"
    Then the next line says an ask arrived for "brisk-copper-moth"
    When Alice answers it from another terminal:
      """
      ctxloom session approvals answer brisk-copper-moth allow-once
      """
    Then the next line says the ask was answered "allow-once"

  # Alice opens it before her daemon is running. It waits instead of
  # refusing, and fills in the moment her work starts one.
  Scenario: With no daemon serving, top waits for one and shows her sessions when it comes
    # new: top waits; it never starts a daemon.
    Given no ctxloom daemon is serving for Alice
    When Alice opens `ctxloom top` at her terminal
    Then it says it is waiting for a ctxloom daemon, and no ctxloom daemon was started
    When Alice's orchestrator "amber-quiet-heron" has launched "brisk-copper-moth" on "reviewer"
    Then her screen shows "amber-quiet-heron" and "brisk-copper-moth", and no longer says it is waiting
