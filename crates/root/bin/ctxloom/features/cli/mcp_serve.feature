# world: base
# NOT ported: nothing; ctxloom's own MCP server is new (Go had one; this one is
#   a client of the daemon).

@doc
Feature: ctxloom's own MCP server — an engine's way to the daemon, through the same API as the CLI

  Every session home ctxloom writes registers ctxloom's own MCP server,
  `ctxloom mcp serve`, which the engine starts over stdio. It holds no store
  and no state: like the CLI, it is a client of the user's daemon, calling
  the same resources, sending the same domain commands through the same
  command coordinator, and presenting the daemon's questions, as MCP
  elicitations, to the engine. A tool's write carries the id the server
  chose, and is retried once on a new daemon.

  Its tools: a child's launch, send, close and clear; a session's list,
  show and transcript; the assembled context; and the approval prompt that
  a claude child's permission asks are routed through
  (`--permission-prompt-tool mcp__ctxloom__approval_prompt`).

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
      # new: the server in each engine's config.
      Given an initialized ctxloom project
      And Alice has exported her assistant's token
      And a <kind> session on <engine>
      Then its session home registers "ctxloom mcp serve" as the MCP server "ctxloom"

      Examples:
        | kind        | engine      |
        | child       | claude-code |
        | interactive | claude-code |
        | child       | opencode    |
        | interactive | opencode    |

    Scenario: The server offers the child, session, context and approval tools
      # new: the tool list.
      Given an initialized ctxloom project
      And a session "amber-quiet-heron" that is still running
      When its engine lists the tools of ctxloom's MCP server
      Then the tools are "child_launch", "child_send", "child_close", "child_clear", "session_list", "session_show", "transcript_show", "context_assemble" and "approval_prompt"

  Rule: A tool is a client of the daemon, and holds nothing itself

    Scenario: A tool's write is the domain's command through the daemon's coordinator, applied once
      # new: the same path as the CLI.
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" of "amber-quiet-heron" that is open and idle
      When the engine of "amber-quiet-heron" calls "child_send" for "brisk-copper-moth" with wake "check the tests"
      Then the tool answered with the child's session as the daemon's read model has it after the message
      And the message was sent once, under the id the server chose

    Scenario: A tool whose daemon is killed under it is retried once on a new daemon
      # new: the retry is the server's too.
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" of "amber-quiet-heron" that is open and idle
      And the daemon will be killed outright after the write is applied, before it is answered
      When the engine of "amber-quiet-heron" calls "child_close" for "brisk-copper-moth"
      Then the tool answered once the new daemon had the close, and the close was applied once

    Scenario: An edit the daemon asks for is answered with the document's new text
      # new: an Edit need to a client that is not a terminal.
      Given an initialized ctxloom project
      And a session "amber-quiet-heron" that is still running
      When its engine calls a tool whose change asks for an edited document, and answers the elicitation with new text
      Then the daemon applied the new text, and no editor was started

  Rule: The approval prompt waits for the human

    Scenario Outline: A claude child's permission ask is answered through the approval prompt
      # new: claude's hook point.
      Given an initialized ctxloom project
      And a claude child "brisk-copper-moth" of "amber-quiet-heron" whose engine calls "approval_prompt" for "Bash" with "rm -rf build/"
      When <who>
      Then the tool answered the engine <answer>

      Examples:
        | who                                                           | answer                                              |
        | Alice runs "ctxloom session approvals answer brisk-copper-moth allow" | {"behavior":"allow"} with the input unchanged       |
        | Alice runs "ctxloom session approvals answer brisk-copper-moth deny"  | {"behavior":"deny"} with a message                  |
        | nobody answers within the ask's time                          | {"behavior":"deny"}, saying no answer came in time  |

  Rule: A containerized engine reaches the daemon through its MCP server

    Scenario: An engine in a container reaches ctxloom only through its MCP server and the hook endpoint
      # new: the container's path to the daemon.
      Given an initialized ctxloom project
      And Alice has exported her assistant's token
      And a child "brisk-copper-moth" whose engine runs in a container
      Then the container holds no ctxloom binary of the host's, and its MCP server and hooks reach the daemon over TCP and TLS
