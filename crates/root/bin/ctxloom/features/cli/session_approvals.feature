# world: base
# NOT ported: nothing; Go's approvals were its coordinator's, and these are new.

@doc
Feature: session approvals — a child's permission ask, answered by the user

  A session the daemon runs has no one at its terminal: a child, or a
  one-shot. When its engine's own permissions say to ask before a tool
  runs, the ask becomes an approval request: listed under the root of its
  session tree (where the human is), answered by the user from any
  terminal, and denied when nobody answers in time. The engine waits for
  the answer. Only the user answers: ctxloom gives agents no answer tool,
  and on the host it cannot tell the human from her own processes. Asks are
  rare: an agent's own engine permissions let its routine work run without
  asking.

  Surfaces covered here:

    ctxloom session approvals [<harp>]                          (bare: lists this project's waiting asks)
    ctxloom session approvals answer <ask|harp> <option> ["<message>"] [--yes]

  AN ASK'S NAME is `ask-` and a harp of its own (`ask-tidy-wren`), minted
  when it is asked and shown in the listing; no session's name can begin
  `ask-`. An ask is named by it, or by the asking session's harp when that
  session has exactly one ask waiting (a second one makes the harp
  ambiguous, and the answer is refused naming each ask).

  THE OPTIONS are the engine's, listed with each ask, and the answer names
  one, in one vocabulary for every engine: `allow-once`, `allow-session`,
  `allow-always`, `deny`, `deny-always`. A claude ask offers, built by
  ctxloom, about the rule the ask implies:

    allow-once      run it, this once
    allow-session   run it, and the same call again, unasked, for the rest of
                    the session (claude keeps the rule; nothing is written)
    allow-always    run it, the same call for the rest of the session, and
                    write the rule into the asking agent's
                    `agents.<a>.engines.claude-code.permissions` `allow`, so
                    every later launch carries it: previewed first, as any
                    config write is, and applied with `--yes`
    deny            do not run it; the message, when given, is said to the
                    model

  The rule is the tool and its exact input: `Bash(<command>)` for Bash, and
  `<Tool>(<path>)` for a tool that takes a file's path (Read, Write, Edit,
  MultiEdit, NotebookEdit). Any other tool's ask offers `allow-once` and
  `deny` alone. `allow-always` writes where it takes effect: into the
  project's definition of the agent when the project defines it (which
  replaces the home's of the same name whole), else into the home's; it
  starts from that definition's own permissions document, and one with none
  starts from its engine's default one, so nothing of that is lost; and the config is written before the ask is
  answered, so an ask that is settled in between keeps the written rule and
  says so.

  IN TIME. An ask waits for its engine's `engines.<e>.approval_timeout`, a
  span (`90s`, `5m`), 300 seconds when unset; then it is denied, and the
  engine is told no answer came in time. `approval_timeout: now` denies
  every ask at once. An ask whose turn is interrupted (a steer) is withdrawn
  at once: claude cancels its call; it is listed no more, and an answer to
  it is refused. An ask whose session ends without withdrawing it waits
  out its time.

  WHERE AN ASK IS HEARD, by engine:

    claude-code child or one-shot   `--permission-prompt-tool mcp__ctxloom__approval_prompt`,
                                    a tool of the ctxloom MCP server the engine
                                    already runs, which waits on the daemon
                                    (with `MCP_TOOL_TIMEOUT` past the ask's
                                    time when that is over 300 seconds)
    no hook point                   the mock engine, and every interactive
                                    session: the human answers on the
                                    terminal, and nothing is routed

  Whether an engine asks at all is its own permissions document's to say;
  ctxloom passes that document through and decides none of it.

  An ask is a fact of the approvals aggregate, recorded once however often
  it is delivered; an answer is its command, sent through the daemon's
  command coordinator, and a second answer is refused. A draining daemon
  still takes asks and answers: they are work it already holds.

  Every ask below is asked by the engine itself, through the path that
  engine uses (a scripted claude calling `ctxloom mcp serve`'s approval
  prompt); an ask's name is read from the listing (`I note ...`), never
  chosen.

  Rule: An ask waits, listed with its options, until it is answered

    Scenario: A child's ask is listed under its root, with what it asks to run and the options it offers
      # new: the asks listed, with claude's four options and the ask's own name.
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" of "amber-quiet-heron" whose engine has asked to run "Bash" with "rm -rf build/"
      When I run "ctxloom session approvals amber-quiet-heron --format json"
      Then the command succeeds
      And the output reports "approvals[0].name" containing "/approvals/ask-"
      And the output reports "approvals[0].child" containing "/sessions/brisk-copper-moth"
      And the output reports "approvals[0].tool_name" as "Bash"
      And the output reports "approvals[0].input_excerpt" containing "rm -rf build/"
      And the output reports "approvals[0].options" having "4" entries
      And the output reports "approvals[0].options[0].id" as "allow-once"
      And the output reports "approvals[0].options[1].id" as "allow-session"
      And the output reports "approvals[0].options[2].id" as "allow-always"
      And the output reports "approvals[0].options[2].rule" as "Bash(rm -rf build/)"
      And the output reports "approvals[0].options[3].id" as "deny"

    Scenario Outline: A path tool's ask offers its rule by path
      # new: the rule a claude ask implies.
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" of "amber-quiet-heron" whose engine has asked to run "<tool>" with "<input>"
      When I run "ctxloom session approvals amber-quiet-heron --format json"
      Then the command succeeds
      And the output reports "approvals[0].options" having "4" entries
      And the output reports "approvals[0].options[1].rule" as "<rule>"

      Examples:
        | tool  | input       | rule               |
        | Edit  | src/main.rs | Edit(src/main.rs)  |
        | Write | NOTES.md    | Write(NOTES.md)    |

    Scenario: Any other tool's ask offers allow-once and deny alone
      # new: no rule, so no session or always option.
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" of "amber-quiet-heron" whose engine has asked to run "mcp__github__create_issue" with "{\"title\":\"flaky test\"}"
      When I run "ctxloom session approvals amber-quiet-heron --format json"
      Then the command succeeds
      And the output reports "approvals[0].options" having "2" entries
      And the output reports "approvals[0].options[0].id" as "allow-once"
      And the output reports "approvals[0].options[1].id" as "deny"

    Scenario: Bare, it lists every ask waiting in this project
      # new: bare `session approvals`, this project's asks.
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" of "amber-quiet-heron" whose engine has asked to run "Bash" with "rm -rf build/"
      And a child "quiet-silver-fern" of "steady-lunar-pike" whose engine has asked to run "Bash" with "make release"
      When I run "ctxloom session approvals --format json"
      Then the command succeeds
      And the output reports "approvals" having "2" entries

    Scenario Outline: The user's answer reaches the waiting tool
      # new: session approvals answer, by the asking child's harp.
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" of "amber-quiet-heron" whose engine has asked to run "Bash" with "rm -rf build/"
      When I run 'ctxloom session approvals answer brisk-copper-moth <answer>'
      Then the command succeeds
      And the child's engine was told <told>
      And no ask is waiting under "amber-quiet-heron"

      Examples:
        | answer         | told                                                                         |
        | allow-once     | to run the tool                                                              |
        | allow-session  | to run the tool, and to allow "Bash(rm -rf build/)" for the rest of its session |
        | deny "not yet" | not to run it, with the message "not yet"                                    |

    Scenario: An ask is answered by its own name
      # new: an ask named by its name.
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" of "amber-quiet-heron" whose engine has asked to run "Bash" with "rm -rf build/"
      When I run "ctxloom session approvals amber-quiet-heron --format json"
      Then the command succeeds
      And I note the ask named by "approvals[0].name" as "the ask"
      When I run "ctxloom session approvals answer {the ask} allow-once"
      Then the command succeeds
      And the child's engine was told to run the tool

    Scenario: A harp with two asks waiting names neither, and the answer is refused naming both
      # new: an ask named by its harp, or by its own name.
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" of "amber-quiet-heron" whose engine has asked to run "Bash" with "rm -rf build/"
      And "brisk-copper-moth"'s engine has also asked to run "Bash" with "make release"
      When I run "ctxloom session approvals amber-quiet-heron --format json"
      Then the command succeeds
      And I note the ask named by "approvals[0].name" as "the first ask"
      And I note the ask named by "approvals[1].name" as "the second ask"
      When I run "ctxloom session approvals answer brisk-copper-moth allow-once"
      Then the command exits with code 20
      And the refusal is "daemon-api-approval-ambiguous"
      And the output contains "{the first ask}"
      And the output contains "{the second ask}"
      When I run "ctxloom session approvals answer {the first ask} allow-once"
      Then the command succeeds
      And the child's engine was told to run "rm -rf build/"
      And an ask is still waiting under "amber-quiet-heron"

    Scenario: An answer naming a harp with no ask waiting is refused
      # new: a harp names an ask only while one waits.
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" of "amber-quiet-heron" that is open and idle
      When I run "ctxloom session approvals answer brisk-copper-moth allow-once"
      Then the command exits with code 20
      And the refusal is "daemon-api-approval-none-waiting"
      And the output contains "brisk-copper-moth"

    Scenario: An answer naming an option the ask does not offer is refused, and the ask still waits
      # new: the answer names one of the ask's own options.
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" of "amber-quiet-heron" whose engine has asked to run "Bash" with "rm -rf build/"
      When I run "ctxloom session approvals answer brisk-copper-moth deny-always"
      Then the command exits with code 20
      And the refusal is "approvals-option-not-offered"
      And the output contains "allow-once, allow-session, allow-always, deny"
      And an ask is still waiting under "amber-quiet-heron"

    Scenario: A second answer to one ask is refused
      # new: an ask is answered once.
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" of "amber-quiet-heron" whose engine has asked to run "Bash" with "rm -rf build/"
      And I run "ctxloom session approvals amber-quiet-heron --format json"
      And I note the ask named by "approvals[0].name" as "the ask"
      And I run "ctxloom session approvals answer {the ask} allow-once"
      When I run "ctxloom session approvals answer {the ask} deny"
      Then the command exits with code 20
      And the refusal is "approvals-approval-settled"
      And the output contains "answered"

  Rule: An ask nobody answers in time is denied, and one whose turn is interrupted is withdrawn

    Scenario: An ask nobody answers within its engine's approval_timeout is denied, and the engine is told so
      # new: the timeout, through the scheduler's alarm.
      Given an initialized ctxloom project whose project config says "engines.claude-code.approval_timeout: 2s"
      And a child "brisk-copper-moth" of "amber-quiet-heron" whose engine has asked to run "Bash" with "rm -rf build/"
      And I run "ctxloom session approvals amber-quiet-heron --format json"
      And I note the ask named by "approvals[0].name" as "the ask"
      When the ask's time runs out
      Then the child's engine was told not to run it, because no answer came in time
      And no ask is waiting under "amber-quiet-heron"
      When I run "ctxloom session approvals answer {the ask} allow-once"
      Then the command exits with code 20
      And the refusal is "approvals-approval-settled"
      And the output contains "expired"

    Scenario: An ask waits 300 seconds when its engine sets no approval_timeout
      # new: the default time.
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" of "amber-quiet-heron" whose engine has asked to run "Bash" with "rm -rf build/"
      When I run "ctxloom session approvals amber-quiet-heron --format json"
      Then the command succeeds
      And the output reports the ask's "expire_time" as 300 seconds after its "ask_time"

    Scenario: With approval_timeout now, every ask is denied at once and never waits
      # new: `approval_timeout: now`.
      Given an initialized ctxloom project whose project config says "engines.claude-code.approval_timeout: now"
      And a child "brisk-copper-moth" of "amber-quiet-heron" that is open and idle
      When "brisk-copper-moth"'s engine asks to run "Bash" with "rm -rf build/"
      Then the child's engine was told not to run it, because every ask is denied at once
      And no ask is waiting under "amber-quiet-heron"

    Scenario: A claude ask whose turn is steered is withdrawn at once, and an answer to it is refused
      # new: a cancelled ask is withdrawn (claude's `notifications/cancelled`).
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" of "amber-quiet-heron" whose engine has asked to run "Bash" with "rm -rf build/"
      And I run "ctxloom session approvals amber-quiet-heron --format json"
      And I note the ask named by "approvals[0].name" as "the ask"
      When I run 'ctxloom session send brisk-copper-moth steer "stop and look at main.rs"'
      Then the command succeeds
      And the child's engine cancelled its approval prompt call
      And no ask is waiting under "amber-quiet-heron"
      When I run "ctxloom session approvals answer {the ask} allow-once"
      Then the command exits with code 20
      And the refusal is "approvals-approval-settled"
      And the output contains "withdrawn"

    Scenario: An ask whose session ends without withdrawing it is denied when its time runs out
      # new: an ask's alarm ends it.
      Given an initialized ctxloom project whose project config says "engines.claude-code.approval_timeout: 2s"
      And a child "brisk-copper-moth" of "amber-quiet-heron" whose engine has asked to run "Bash" with "rm -rf build/"
      And I run "ctxloom session approvals amber-quiet-heron --format json"
      And I note the ask named by "approvals[0].name" as "the ask"
      When "brisk-copper-moth"'s engine exits without cancelling its approval prompt call
      Then an ask is still waiting under "amber-quiet-heron"
      When the ask's time runs out
      Then no ask is waiting under "amber-quiet-heron"
      When I run "ctxloom session approvals answer {the ask} allow-once"
      Then the command exits with code 20
      And the refusal is "approvals-approval-settled"
      And the output contains "expired"

  Rule: allow-always writes its rule into the agent's permissions, previewed first

    Scenario: Off a terminal, allow-always previews the config it would write, and changes nothing
      # new: an "always" persists into ctxloom's config, with a preview.
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" of "amber-quiet-heron" whose engine has asked to run "Bash" with "rm -rf build/"
      When I run "ctxloom session approvals answer brisk-copper-moth allow-always --format json --detailed-exitcode"
      Then the command exits with code 1
      And the output reports "applied" as "false"
      And the output reports "apply" containing "--yes"
      And the output contains ".ctxloom/config.yaml"
      And the output contains "agents.default.engines.claude-code.permissions"
      And the output contains "Bash(rm -rf build/)"
      And the output contains "acceptEdits"
      And the file ".ctxloom/config.yaml" does not contain "Bash(rm -rf build/)"
      And an ask is still waiting under "amber-quiet-heron"

    Scenario: allow-always with --yes writes the rule over the engine's default document, runs the tool, and every later launch carries it
      # new: an "always" persists into ctxloom's config.
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" of "amber-quiet-heron" whose engine has asked to run "Bash" with "rm -rf build/"
      When I run "ctxloom session approvals answer brisk-copper-moth allow-always --yes"
      Then the command succeeds
      And the child's engine was told to run the tool, and to allow "Bash(rm -rf build/)" for the rest of its session
      And no ask is waiting under "amber-quiet-heron"
      And the file ".ctxloom/config.yaml" contains "Bash(rm -rf build/)"
      And the file ".ctxloom/config.yaml" contains "defaultMode: acceptEdits"
      When I run "ctxloom agent show default --format json"
      Then the command succeeds
      And the output contains "Bash(rm -rf build/)"

    Scenario: allow-always writes into the project's definition of the agent, which replaces the home's
      # new: where an "always" is written (owner ruling after U4: where it takes effect).
      Given an initialized ctxloom project
      And the home already has the file ".ctxloom/config.yaml":
        """
        schema_version: 9
        agents:
          default:
            engines:
              claude-code:
                permissions:
                  defaultMode: default
        """
      And a child "brisk-copper-moth" of "amber-quiet-heron" whose engine has asked to run "Bash" with "rm -rf build/"
      When I run "ctxloom session approvals answer brisk-copper-moth allow-always --yes"
      Then the command succeeds
      And the file ".ctxloom/config.yaml" contains "Bash(rm -rf build/)"
      And the file ".ctxloom/config.yaml" contains "defaultMode: acceptEdits"
      And the home file ".ctxloom/config.yaml" does not contain "Bash(rm -rf build/)"
      And the home file ".ctxloom/config.yaml" contains "defaultMode: default"

    Scenario: allow-always writes into the home's definition of the agent when the project defines none
      # new: where an "always" is written (owner ruling after U4: where it takes effect).
      Given an initialized ctxloom project
      And the home already has the file ".ctxloom/config.yaml":
        """
        schema_version: 9
        agents:
          default:
            engine: claude-code
            engines:
              claude-code:
                permissions:
                  defaultMode: default
        """
      And the project's config defines no agent of its own
      And a child "brisk-copper-moth" of "amber-quiet-heron" whose engine has asked to run "Bash" with "rm -rf build/"
      When I run "ctxloom session approvals answer brisk-copper-moth allow-always --yes"
      Then the command succeeds
      And the home file ".ctxloom/config.yaml" contains "Bash(rm -rf build/)"
      And the home file ".ctxloom/config.yaml" contains "defaultMode: default"
      And the file ".ctxloom/config.yaml" does not contain "Bash(rm -rf build/)"

  Rule: Each engine is asked through its own hook point

    Scenario: A claude child is launched with ctxloom's approval tool as its permission prompt
      # new: claude's hook point.
      Given an initialized ctxloom project
      And Alice has exported her assistant's token
      And a session "amber-quiet-heron" that is still running
      When I run 'ctxloom session launch amber-quiet-heron --name brisk-copper-moth "review the change"'
      Then the child's claude was launched with "--permission-prompt-tool" "mcp__ctxloom__approval_prompt"
      And the child's claude was not given "MCP_TOOL_TIMEOUT"
      And the child's session home names ctxloom's MCP server
      And the child's session home registers no PermissionRequest hook of ctxloom's

    Scenario: A claude child whose asks wait over 300 seconds is given an MCP tool timeout past them
      # new: a long approval_timeout outlasts claude's MCP call limit.
      Given an initialized ctxloom project whose project config says "engines.claude-code.approval_timeout: 1h"
      And Alice has exported her assistant's token
      And a session "amber-quiet-heron" that is still running
      When I run 'ctxloom session launch amber-quiet-heron --name brisk-copper-moth "review the change"'
      Then the child's claude was given "MCP_TOOL_TIMEOUT" above "3600000"

    Scenario: A claude one-shot is launched with ctxloom's approval tool as its permission prompt
      # new: one-shots ask too.
      Given an initialized ctxloom project
      And Alice has exported her assistant's token
      When I run 'ctxloom run --one-shot "hello"'
      Then the command succeeds
      And the one-shot's claude was launched with "--permission-prompt-tool" "mcp__ctxloom__approval_prompt"
      And the one-shot's session home names ctxloom's MCP server

    Scenario: A claude one-shot's ask is answered from another terminal, and the one-shot goes on
      # new: one-shots ask too.
      Given an initialized ctxloom project
      And Alice has exported her assistant's token
      When I start 'ctxloom run --one-shot "touch hi.txt"', whose engine asks to run "Bash" with "touch hi.txt"
      And I run "ctxloom session approvals --format json"
      Then the command succeeds
      And the output reports "approvals[0].tool_name" as "Bash"
      And I note the ask named by "approvals[0].name" as "the ask"
      When I run "ctxloom session approvals answer {the ask} allow-once"
      Then the command succeeds
      And the started command succeeds, its engine told to run the tool

  Rule: A draining daemon still takes asks and answers

    Scenario: While the daemon drains, an ask is still recorded, listed and answered
      # new: approvals are work the daemon already holds.
      Given an initialized ctxloom project
      And the home config says "daemon.drain_timeout: 1h"
      And a ctxloom daemon is serving for this home, hosting a child "brisk-copper-moth" that does not end its turn
      When the daemon is sent SIGTERM
      And "brisk-copper-moth"'s engine asks to run "Bash" with "rm -rf build/"
      And I run "ctxloom session approvals --format json"
      Then the command succeeds
      And the output reports "approvals[0].tool_name" as "Bash"
      When I run "ctxloom session approvals answer brisk-copper-moth allow-once"
      Then the command succeeds
      And the child's engine was told to run the tool
