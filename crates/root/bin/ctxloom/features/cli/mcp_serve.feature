# world: base
# NOT ported: nothing; ctxloom's own MCP server is new (Go had one; this one is
#   a client of the daemon). The Rule "A containerized engine reaches the daemon
#   through its MCP server" is pending for step 7 (OD-48, containers).

@doc
Feature: ctxloom's own MCP server — an engine's way to the daemon, through the same API as the CLI

  Every session home ctxloom writes registers ctxloom's own MCP server,
  `ctxloom mcp serve`, which the engine starts over stdio. It holds no store
  and no state: like the CLI, it is a client of the user's daemon, calling
  the same resources, sending the same domain commands through the same
  command coordinator, and presenting the daemon's questions, as MCP
  elicitations, to the engine. A tool's write carries the id the server
  chose, and is retried once on a new daemon.

  Its tools: a child's launch (in the owner's project, or in another by
  `project`, a directory, relative to the calling session's project root),
  send, close and clear; a session's list,
  show and transcript; the assembled context; and the approval prompt that
  the permission asks of a claude child or one-shot are routed through
  (`--permission-prompt-tool mcp__ctxloom__approval_prompt`): claude calls
  it with `{tool_name, input, tool_use_id}` and waits, however long the
  user takes within the ask's time, for its answer, the JSON text
  `{"behavior":"allow", ...}` or `{"behavior":"deny","message": ...}`. A
  call claude cancels (`notifications/cancelled`) withdraws its ask. The
  server answers claude's `server/discover` probe, sent before
  `initialize`.

  It is also how an engine running in a container reaches ctxloom: through
  its MCP server and the hook endpoint, over TCP and TLS, never through a
  ctxloom binary on the host.

  `ctxloom mcp serve` is hidden: an engine starts it, nobody types it. It is
  built on `rmcp`, the official Rust MCP SDK, pinned exactly, its default
  features trimmed to the stdio transport; before it is adopted its licence
  is checked against the deny list, its elicitation support confirmed, and
  its dependencies held to ring (no aws-lc).

  Rule: Every session's engine is given ctxloom's MCP server

    Scenario Outline: A session home registers ctxloom's MCP server for its engine
      # new: the server in each engine's config. A claude session the daemon
      # hosts starts it without the terminal wake's poster (`--no-wake`): the
      # daemon delivers to it, and one session has one delivery mechanism.
      Given an initialized ctxloom project
      And Alice has exported her assistant's token
      And a <kind> session on <engine>
      Then its session home registers "<command>" as the MCP server "ctxloom"

      Examples:
        | kind        | engine      | command                     |
        | child       | claude-code | ctxloom mcp serve --no-wake |
        | interactive | claude-code | ctxloom mcp serve           |
        | child       | opencode    | ctxloom mcp serve           |
        | interactive | opencode    | ctxloom mcp serve           |

    Scenario: The server offers the child, session, context and approval tools
      # new: the tool list.
      Given an initialized ctxloom project
      And a session "amber-quiet-heron" that is still running
      When its engine lists the tools of ctxloom's MCP server
      Then the tools are "child_launch", "child_send", "child_close", "child_clear", "session_list", "session_show", "transcript_show", "context_assemble" and "approval_prompt"

    Scenario: The server answers claude's discovery probe before it is initialized
      # new: claude sends `server/discover`, with a string id, before `initialize`.
      Given an initialized ctxloom project
      And a session "amber-quiet-heron" that is still running
      When its engine sends "server/discover" to ctxloom's MCP server before "initialize"
      Then the server answered it with an empty result
      And the engine went on to initialize the server and list its tools

  Rule: A tool is a client of the daemon, and holds nothing itself

    Scenario: A tool's write is the domain's command through the daemon's coordinator, applied once
      # new: the same path as the CLI.
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" of "amber-quiet-heron" that is open and idle
      When the engine of "amber-quiet-heron" calls "child_send" for "brisk-copper-moth" with wake "check the tests"
      Then the tool answered with the child's session as the daemon's read model has it after the message
      And the message was sent once, under the id the server chose

    Scenario: child_launch with a project launches the child to work in that project
      # new: the child_launch tool's project (step 8: the cross-repository surface).
      Given an initialized ctxloom project
      And Alice has exported her assistant's token
      And a session "amber-quiet-heron" that is still running
      And a second project "api" beside this one, initialized with ctxloom
      And I run "ctxloom trust --yes" in the project "api"
      When the engine of "amber-quiet-heron" calls "child_launch" for "brisk-copper-moth" with project "../api" and task "review the change"
      Then the tool answered with the child's session, named under the project "api"
      And the child "brisk-copper-moth" of "amber-quiet-heron" ran its first turn on "review the change", in the root of "api"

    Scenario: A tool whose daemon is killed under it is retried once on a new daemon
      # new: the retry is the server's too.
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" of "amber-quiet-heron" that is open and idle
      And the daemon will be killed outright after the write is applied, before it is answered
      When the engine of "amber-quiet-heron" calls "child_close" for "brisk-copper-moth"
      Then the tool answered once the new daemon had the close, and the close was applied once

    Scenario: An edit the daemon asks for is answered with the document's new text
      # new: an Edit need to a client that is not a terminal.
      # step 10 U11: green with the elicitations; the tool is the test build's (QUESTIONS.md 7).
      Given an initialized ctxloom project
      And a session "amber-quiet-heron" that is still running
      When its engine calls a tool whose change asks for an edited document, and answers the elicitation with new text
      Then the daemon applied the new text, and no editor was started

  Rule: A close that takes children with it asks first, as an elicitation

    Scenario Outline: child_close of a child that has children of its own asks to confirm the cascade
      # new: step 10 D16 (S40): the cascade is the preview's Confirm need, elicited from the engine.
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" of "amber-quiet-heron" that is open and idle
      And a child "olive-quiet-finch" of "brisk-copper-moth" that is open and idle
      When the engine of "amber-quiet-heron" calls "child_close" for "brisk-copper-moth", and answers its elicitation <answer>
      Then the elicitation asked to confirm closing "brisk-copper-moth" and, with it, "olive-quiet-finch"
      And <outcome>

      Examples:
        | answer  | outcome                                                                       |
        | yes     | "brisk-copper-moth" and "olive-quiet-finch" are closed, and the tool said so   |
        | no      | neither is closed, and the tool answered that the close was declined         |

    Scenario: child_close of a child with no children of its own closes it without asking
      # new: step 10 D16, the paired negative; no cascade, no question.
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" of "amber-quiet-heron" that is open and idle
      When the engine of "amber-quiet-heron" calls "child_close" for "brisk-copper-moth"
      Then no elicitation was sent
      And "brisk-copper-moth" is closed, and the tool said so

  Rule: The approval prompt waits for the human

    Scenario Outline: A claude child's permission ask is answered through the approval prompt
      # new: claude's hook point.
      Given an initialized ctxloom project
      And a claude child "brisk-copper-moth" of "amber-quiet-heron" whose engine calls "approval_prompt" for "Bash" with "rm -rf build/"
      When <who>
      Then the tool answered the engine <answer>

      Examples:
        | who                                                                                 | answer                                                                                     |
        | Alice runs "ctxloom session approvals answer brisk-copper-moth allow-once"          | {"behavior":"allow"} with the input unchanged                                              |
        | Alice runs "ctxloom session approvals answer brisk-copper-moth allow-session"       | {"behavior":"allow"} with the input unchanged, and a session rule for "Bash" "rm -rf build/" |
        | Alice runs "ctxloom session approvals answer brisk-copper-moth allow-always --yes"  | {"behavior":"allow"} with the input unchanged, and a session rule for "Bash" "rm -rf build/" |
        | Alice runs 'ctxloom session approvals answer brisk-copper-moth deny "not yet"'      | {"behavior":"deny"} with the message "not yet"                                             |
        | Alice runs "ctxloom session approvals answer brisk-copper-moth deny"                | {"behavior":"deny"}, saying the user denied it                                             |

    Scenario: A claude child's permission ask nobody answers in time is denied through the approval prompt
      # new: the timeout, through the scheduler's alarm (a short time of its own, so the rows above never race it).
      Given an initialized ctxloom project whose project config says "engines.claude-code.approval_timeout: 2s"
      And a claude child "brisk-copper-moth" of "amber-quiet-heron" whose engine calls "approval_prompt" for "Bash" with "rm -rf build/"
      When nobody answers within the ask's time
      Then the tool answered the engine {"behavior":"deny"}, saying no answer came in time

    Scenario: A call claude cancels withdraws its ask, and the server answers nothing for it
      # new: a cancelled ask is withdrawn at once.
      Given an initialized ctxloom project
      And a claude child "brisk-copper-moth" of "amber-quiet-heron" whose engine calls "approval_prompt" for "Bash" with "rm -rf build/"
      When the engine sends "notifications/cancelled" for that call
      Then no ask is waiting under "amber-quiet-heron"
      And the server sent no answer for the cancelled call, and still serves the engine's next call

  # Pending (OD-48, containers): no run launches into a container yet (step 7).
  @pending
  Rule: A containerized engine reaches the daemon through its MCP server

    Scenario: An engine in a container reaches ctxloom only through its MCP server and the hook endpoint
      # new: the container's path to the daemon.
      Given an initialized ctxloom project
      And Alice has exported her assistant's token
      And a child "brisk-copper-moth" whose engine runs in a container
      Then the container holds no ctxloom binary of the host's, and its MCP server and hooks reach the daemon over TCP and TLS
