# world: base
# NOT ported: nothing; Go's approvals were its coordinator's, and these are new.

@doc
Feature: session approvals — a child's permission ask, answered by the user or its owner

  A child runs with no one at its terminal. When its engine's own
  permissions say to ask before a tool runs, the ask becomes an approval
  request: listed under the root of its session tree (where the human is),
  answered from the user's CLI (ctxloom gives agents no answer tool; on the
  host it cannot tell the human from her own processes), and denied when
  nobody answers in time (300 seconds). The child's tool waits
  for the answer. Asks are rare: an agent's own engine permissions let its
  routine work run without asking.

  Surfaces covered here:

    ctxloom session approvals [<harp>]                        (bare: lists the asks waiting)
    ctxloom session approvals answer <ask|harp> {allow|deny} ["<message>"]

  An ask is named by its id, or by the asking child's harp when that child
  has exactly one ask waiting (a second one makes the harp ambiguous, and
  the answer is refused naming each id).

  WHERE AN ASK IS HEARD, by engine:

    claude-code child   `--permission-prompt-tool mcp__ctxloom__approval_prompt`,
                        a tool of the ctxloom MCP server the child already runs,
                        which waits on the daemon for the answer
    opencode child      ACP `session/request_permission`, answered by the daemon
                        with the request's allow-once or reject-once option
    no hook point       a per-turn `opencode run` child (unverified), the mock
                        engine, and every interactive session: the human
                        answers on the terminal, and nothing is routed

  Whether an engine asks at all is its own permissions document's to say;
  ctxloom passes that document through and decides none of it.

  An ask is a fact of the approvals aggregate, recorded once however often
  it is delivered; an answer is its command, sent through the daemon's
  command coordinator, and a second answer is refused.

  Rule: An ask waits, listed, until it is answered or its time runs out

    Scenario: A child's ask is listed under its root, with what it asks to run
      # new: the asks listed.
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" of "amber-quiet-heron" whose engine has asked to run "Bash" with "rm -rf build/"
      When I run "ctxloom session approvals amber-quiet-heron --format json"
      Then the command succeeds
      And the output reports "approvals[0].child" containing "/sessions/brisk-copper-moth"
      And the output reports "approvals[0].tool_name" as "Bash"
      And the output reports "approvals[0].input_excerpt" containing "rm -rf build/"

    Scenario Outline: The user's answer reaches the waiting tool
      # new: session approvals answer.
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" of "amber-quiet-heron" whose engine has asked to run "Bash" with "rm -rf build/"
      When I run 'ctxloom session approvals answer <ask> <decision> "not yet"'
      Then the command succeeds
      And the child's engine was told <told>
      And no ask is waiting under "amber-quiet-heron"

      Examples:
        | ask           | decision | told                                         |
        | brisk-copper-moth | allow    | to run the tool                              |
        | brisk-copper-moth | deny     | not to run it, with the message "not yet"    |

    Scenario: A second answer to one ask is refused
      # new: an ask is answered once.
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" of "amber-quiet-heron" whose ask has been answered
      When I run "ctxloom session approvals answer brisk-copper-moth allow"
      Then the command exits with code 20
      And the refusal is "approvals-approval-settled"

    Scenario: An ask nobody answers in time is denied, and the engine is told so
      # new: the timeout.
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" of "amber-quiet-heron" whose engine has asked to run "Bash" with "rm -rf build/"
      When the ask's time runs out
      Then the child's engine was told not to run it, because no answer came in time
      And no ask is waiting under "amber-quiet-heron"

  Rule: Each engine is asked through its own hook point

    Scenario: A claude child is launched with ctxloom's approval tool as its permission prompt
      # new: claude's hook point.
      Given an initialized ctxloom project
      And Alice has exported her assistant's token
      And a session "amber-quiet-heron" that is still running
      When I run 'ctxloom session launch amber-quiet-heron --name brisk-copper-moth "review the change"'
      Then the child's claude was launched with "--permission-prompt-tool" "mcp__ctxloom__approval_prompt"
      And the child's session home names ctxloom's MCP server

    Scenario: An opencode child's permission request is an ask, answered on its ACP connection
      # new: opencode's hook point.
      Given an initialized ctxloom project
      And an opencode child "brisk-copper-moth" of "amber-quiet-heron" that sends a permission request for "bash"
      When I run "ctxloom session approvals answer brisk-copper-moth allow"
      Then the child's opencode was answered with its request's allow-once option
