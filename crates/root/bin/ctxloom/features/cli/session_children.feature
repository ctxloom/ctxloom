# world: base
# NOT ported: nothing; the verbs are new, and Go had an inbox instead.

@doc
Feature: session launch, send, clear and close — a child of a session, run by the user's daemon

  A child is a session of its own, launched for an owner session, run by the
  user's daemon (never on a terminal), and kept open between tasks until it
  is closed. The owner agent orchestrates its own children through ctxloom's
  MCP server; the user does the same from any terminal with these verbs, as
  herself.

  Surfaces covered here:

    ctxloom session launch <owner> [--name <harp>] [--agent <agent>] [--engine <engine>] ["<task>"]
    ctxloom session send <harp> "<text>" [--urgency inform|wake|steer] [--id <id>]
    ctxloom session clear <harp>
    ctxloom session close <harp>

  WHAT A CHILD IS LAUNCHED WITH. The launching command sends its own
  environment with the launch; the daemon keeps what the child may be given
  of it (the engine's allowlist, the engine block's `env`, the credential
  source's variables, the permits) in a hand-off file, 0600 in the 0700
  `~/.ctxloom/daemon/launch/`, named by the child's stable id. The file
  lives as long as the child is open: each start of the child reads it,
  including a start after a daemon restart, which resumes the child's own
  conversation. It is deleted when the child is closed, and a file whose
  child is no longer open is swept. The child sees only its environment,
  never the file.

  URGENCY. `inform` waits for the child's next turn, whoever starts it;
  `wake` (the default) starts a turn when the child is idle; `steer`
  interrupts the turn in flight and runs next. A message waiting for a turn
  is listed by `session show`.

  Rule: A launch starts a child of the owner, with its task

    Scenario: A launch names the child, runs its task as its first turn, and lists it under its owner
      # new: session launch.
      Given an initialized ctxloom project
      And Alice has exported her assistant's token
      And a session "amber-quiet-heron" that is still running
      When I run 'ctxloom session launch amber-quiet-heron --name brisk-copper-moth --format json "review the change"'
      Then the command succeeds
      And the output reports "harp" as "brisk-copper-moth"
      And the child "brisk-copper-moth" of "amber-quiet-heron" ran its first turn on "review the change"
      And the child's hand-off file is readable by its owner alone while the child is open

    Scenario: A launch for an owner nobody recorded is refused
      # new: session launch.
      Given an initialized ctxloom project
      When I run 'ctxloom session launch nobody-ever-ran "review"'
      Then the command exits with code 2
      And the output contains "there is no session named nobody-ever-ran"
      And no hand-off file was left behind

    Scenario: A launch with no credential for the child is refused, and leaves nothing
      # new: a child needs a credential in its environment.
      Given an initialized ctxloom project
      And a session "amber-quiet-heron" that is still running
      When I run 'ctxloom session launch amber-quiet-heron --name brisk-copper-moth "review"'
      Then the command exits with code 2
      And the output contains "needs a credential in the environment"
      And no hand-off file was left behind

  Rule: A message reaches the child by its urgency, once

    Scenario Outline: A message to an idle child is acted on as its urgency says
      # new: session send.
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" that is open and idle
      When I run 'ctxloom session send brisk-copper-moth "check the tests" --urgency <urgency>'
      Then the command succeeds
      And the child <outcome>

      Examples:
        | urgency | outcome                                                      |
        | wake    | ran a turn on "check the tests"                              |
        | inform  | ran no turn, and lists "check the tests" as waiting for one  |

    Scenario: A steer interrupts the child's turn in flight and runs next
      # new: session send --urgency steer.
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" that is in the middle of a turn
      When I run 'ctxloom session send brisk-copper-moth "stop and look at main.rs" --urgency steer'
      Then the command succeeds
      And the child's turn in flight was interrupted, and its next turn was "stop and look at main.rs"

    Scenario: The same message id sent twice is one message
      # new: a send is keyed by its id.
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" that is open and idle
      When I run 'ctxloom session send brisk-copper-moth "check the tests" --id m1'
      And I run 'ctxloom session send brisk-copper-moth "check the tests" --id m1'
      Then the command succeeds
      And the child ran one turn on "check the tests"

    Scenario: A message to a closed child is refused
      # new: a closed session takes no message.
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" that has been closed
      When I run 'ctxloom session send brisk-copper-moth "one more thing"'
      Then the command exits with code 2
      And the output contains "session brisk-copper-moth is closed"

  Rule: A clear starts the child's conversation again, in the same session

    Scenario: A clear rotates the child's conversation and keeps its session
      # new: session clear.
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" that is open and idle
      When I run "ctxloom session clear brisk-copper-moth"
      Then the command succeeds
      And the child's next turn runs in a new conversation of the same session

  Rule: A close ends the child for good

    Scenario: A close ends the child's engine, deletes its hand-off, and is said once
      # new: session close.
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" that is open and idle
      When I run "ctxloom session close brisk-copper-moth"
      Then the command succeeds
      And the child "brisk-copper-moth" is listed as closed, its engine has exited and its hand-off file is gone
      When I run "ctxloom session close brisk-copper-moth"
      Then the command succeeds
      And the output contains "already closed"

    Scenario: Closing an owner closes the children it still holds
      # new: the cascade.
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" of "amber-quiet-heron" that is open and idle
      When I run "ctxloom session close amber-quiet-heron"
      Then the command succeeds
      And the child "brisk-copper-moth" is listed as closed, its engine has exited and its hand-off file is gone

    Scenario: A hand-off file whose child is no longer open is swept
      # new: the sweep.
      Given an initialized ctxloom project
      And a hand-off file is left for a child that is no longer open
      When I run "ctxloom session list"
      Then the command succeeds
      And no hand-off file was left behind
