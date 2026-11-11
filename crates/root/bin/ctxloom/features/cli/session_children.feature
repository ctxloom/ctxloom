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
    ctxloom session send <harp> {inform|wake|steer} "<text>" [--id <id>]
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

  URGENCY, always said, as the word after the harp: `inform` waits for the
  child's next turn, whoever starts it; `wake` starts a turn when the child
  is idle; `steer` interrupts the turn in flight and runs next. A message
  waiting for a turn is listed by `session show`.

  HOW THEY ARE SENT. A send, a clear, a close and a launch are the session's
  own commands, sent through the daemon's command coordinator under an id
  the command chose (a message's id is `--id`, or one the command mints),
  and rendered from the read models the coordinator waited for. A launch
  first hands the daemon the environment the child is launched with. Every
  refusal exits 2.

  Rule: A launch starts a child of the owner, with its task

    Scenario: A launch names the child, runs its task as its first turn, and lists it under its owner
      # new: session launch.
      Given an initialized ctxloom project
      And Alice has exported her assistant's token
      And a session "amber-quiet-heron" that is still running
      When I run 'ctxloom session launch amber-quiet-heron --name brisk-copper-moth --format json "review the change"'
      Then the command succeeds
      And the output reports "name" containing "/sessions/brisk-copper-moth"
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

    Scenario: A child its orchestrator launches gets its credential from the owner's engine environment alone
      # new: an orchestrator's child is handed the owner's curated environment.
      Given an initialized ctxloom project
      And a session "amber-quiet-heron" whose engine environment names ANTHROPIC_API_KEY through its engine block
      And the daemon was started from a shell holding CLAUDE_CODE_OAUTH_TOKEN
      When its orchestrator launches a child "brisk-copper-moth"
      Then the child "brisk-copper-moth" was given ANTHROPIC_API_KEY, and not CLAUDE_CODE_OAUTH_TOKEN

  Rule: A message reaches the child by its urgency, once

    Scenario: A send with no urgency is a usage error, and starts no daemon
      # new: the urgency is a required word.
      Given an initialized ctxloom project
      And no ctxloom daemon is serving for this home
      When I run 'ctxloom session send brisk-copper-moth "check the tests"'
      Then the command exits with code 2
      And the output contains "inform, wake, steer"
      And no ctxloom daemon was started


    Scenario Outline: A message to an idle child is acted on as its urgency says
      # new: session send.
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" that is open and idle
      When I run 'ctxloom session send brisk-copper-moth <urgency> "check the tests"'
      Then the command succeeds
      And the child <outcome>

      Examples:
        | urgency | outcome                                                      |
        | wake    | ran a turn on "check the tests"                              |
        | inform  | ran no turn, and lists "check the tests" as waiting for one  |

    Scenario: A steer interrupts the child's turn in flight and runs next
      # new: session send steer.
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" that is in the middle of a turn
      When I run 'ctxloom session send brisk-copper-moth steer "stop and look at main.rs"'
      Then the command succeeds
      And the child's turn in flight was interrupted, and its next turn was "stop and look at main.rs"

    Scenario: The same message id sent twice is one message
      # new: a send is keyed by its id.
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" that is open and idle
      When I run 'ctxloom session send brisk-copper-moth wake "check the tests" --id m1'
      And I run 'ctxloom session send brisk-copper-moth wake "check the tests" --id m1'
      Then the command succeeds
      And the child ran one turn on "check the tests"

    Scenario: A message to a closed child is refused
      # new: a closed session takes no message.
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" that has been closed
      When I run 'ctxloom session send brisk-copper-moth wake "one more thing"'
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

    Scenario: Closing a running interactive session stops its assistant on its terminal
      # new: a close reaches the terminal the session runs on.
      Given an initialized ctxloom project
      And an interactive session "amber-quiet-heron" whose assistant is running on its terminal
      When I run "ctxloom session close amber-quiet-heron"
      Then the command succeeds
      And the assistant on its terminal was asked to stop, then killed once its grace had passed
      And "amber-quiet-heron" is listed as closed

    Scenario: A hand-off file whose child is no longer open is swept
      # new: the sweep.
      Given an initialized ctxloom project
      And a hand-off file is left for a child that is no longer open
      When I run "ctxloom session list"
      Then the command succeeds
      And no hand-off file was left behind
