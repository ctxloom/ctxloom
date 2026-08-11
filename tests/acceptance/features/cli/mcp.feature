@doc
Feature: mcp — registering MCP servers ctxloom hands to every engine

  An MCP server is a tool process — a filesystem walker, a database client, a
  search index — that an AI assistant can call. `ctxloom mcp server` is the
  noun that owns registering one: a server named once in the project's config
  is what every backend's own MCP configuration is generated from, and what
  an agent sees listed as a resource inside the running session.

  This is the comprehensive per-noun spec: what the noun DOES, leaf by leaf,
  including the refusals and the shapes that only matter to a machine. The
  narrative version — Carol's team sharing one profile that reaches four
  engines in their own native format — is
  journeys/j000400_multi_engine.feature, which asserts what a PERSON sees.

  Rule: Registering a server makes its command reachable from every axis ctxloom exposes

    # "tools" is the argument the command line passed, and it is the config
    # key, the listed name, and the resource entry alike — so an
    # implementation that stored the name but dropped the command string
    # would still satisfy every check that only looks for "tools". The
    # command's own executable, "echo", is checked at each axis alongside the
    # name for exactly that reason: a name echoed back three times proves
    # nothing about whether the command it names actually landed.
    Scenario: Alice registers a shared MCP server
      Given an initialized ctxloom project
      When Alice registers an MCP server for her team:
        """
        ctxloom mcp server create tools -c echo
        """
      Then the command succeeds
      And the file ".ctxloom/config.yaml" contains "echo"
      When I run "ctxloom mcp server list"
      Then the command succeeds
      And the output contains "tools"
      When the agent reads resource "ctxloom://mcp-servers"
      Then the resource contains "tools"
      And the resource contains "echo"

  Rule: Showing a server surfaces its stored command, not just its name

    # `show` prints the server's own name as a header, echoed straight from
    # the argument the scenario already passed — so a command of "" would
    # still satisfy an assertion that only checks for "tools". "echo" exists
    # nowhere in a fresh project except inside the server's own stored
    # command, so only a real round trip through config satisfies it.
    Scenario: Showing an MCP server's configuration
      Given an initialized ctxloom project
      And I run "ctxloom mcp server create tools -c echo"
      When Alice inspects the server's configuration:
        """
        ctxloom mcp server show tools
        """
      Then the command succeeds
      And the output contains "echo"

  Rule: Removing a server removes it from the roster

    # Bare `remove` is a preview: it must leave the server configured. A guard
    # that quietly destroyed anyway would still pass a scenario that only
    # checked exit code — the follow-up `mcp server list` is what actually
    # catches that.
    Scenario: Bare mcp server remove reports and destroys nothing
      Given an initialized ctxloom project
      And I run "ctxloom mcp server create tools -c echo"
      When I run "ctxloom mcp server remove tools"
      Then the command succeeds
      And the output contains "Nothing was removed"
      And the output contains "--yes"
      When I run "ctxloom mcp server list"
      Then the output contains "tools"

    Scenario: Removing an MCP server
      Given an initialized ctxloom project
      And I run "ctxloom mcp server create tools -c echo"
      When Alice removes the server she no longer needs:
        """
        ctxloom mcp server remove tools --yes
        """
      Then the command succeeds
      When I run "ctxloom mcp server list"
      Then the output does not contain "tools"

  Rule: Editing a bundle's server is a round trip through the user's editor

    # `mcp server edit` is the only leaf here that hands control to an
    # external program and takes the result back, so the claim is the ROUND
    # TRIP: what the editor wrote must reach the bundle on disk. A scenario
    # asserting exit 0 would pass against an editor that was never launched.
    #
    # The fixture authors the bundle's mcp section directly, and that is not
    # laziness: `mcp server create` cannot produce a server this command can
    # address. create takes a bare <name> and stores it in the project
    # config — passing it the documented "<bundle>#mcp/<name>" ref simply
    # makes that whole string the name — while edit resolves the ref and
    # looks in the bundle. The two never meet (taskloom filed separately).
    # Authoring the manifest is the only way to reach this code path today.
    Scenario: Editing a bundle's MCP server writes the editor's result back
      Given a ctxloom project with a command-rewriting editor
      And the project already has the file ".ctxloom/content/bundles/demo.yaml":
        """
        version: 1.0.0
        description: mcp edit fixture
        mcp:
            tools:
                command: echo
                args:
                    - KEEP-THIS-ARGUMENT
        """
      When Alice edits a bundle-scoped MCP server:
        """
        ctxloom mcp server edit demo#mcp/tools
        """
      Then the command succeeds
      And the output contains "Updated MCP server"
      And the file ".ctxloom/content/bundles/demo.yaml" contains "EDITED-BY-TEST"
      # The rest of the entry must survive the round trip. An implementation
      # that rewrote the manifest from just the edited field would satisfy
      # the line above while silently dropping the arguments.
      And the file ".ctxloom/content/bundles/demo.yaml" contains "KEEP-THIS-ARGUMENT"

    # The refusal path. A ref naming a server that is not there must fail
    # rather than launch an editor on an empty buffer and write a new entry
    # from it — which is how a typo'd ref becomes a silently created server.
    Scenario: Editing an MCP server that does not exist fails instead of creating one
      Given a ctxloom project with a command-rewriting editor
      When I run "ctxloom mcp server edit demo#mcp/absent"
      Then the command fails
      And the output contains "not found"

  Rule: A server registered once reaches every engine in its own native configuration file

    # PAYLOAD, NOT EXISTENCE. Every row PARSES the generated file in its own
    # format (JSON for claude/kiro, TOML for codex) and asserts
    # the actual command field under the actual server name — never a bare
    # file-exists and never a substring of a key name (the vacuousness a
    # ".mcp.json" contains "ctxloom" check would carry). claude and kiro
    # share one JSON "mcpServers" table shape; codex has no MCP
    # file of its own — its servers fold into the same config.toml
    # that carries its hooks, under "mcp_servers", inside the project-scoped
    # $CODEX_HOME ctxloom points it at (internal/codex.StateHome).
    #
    # EVERY ROW IS UNTAGGED because materializing a profile only WRITES
    # files; it never launches an engine, so no credential is needed and
    # nothing here gates behind @live.
    Scenario Outline: A shared MCP server materializes into <engine>'s own configuration file
      Given Carol's team profile carries a shared fragment, command, MCP server, and hook
      When Alice materializes the team profile for <engine>
      Then the materialized <engine> MCP configuration carries the shared server's command, in its own native shape

      Examples:
        | engine      |
        | claude-code |
        | kiro        |
        | codex       |

  Rule: What an engine is told to launch is `mcp serve`, and nothing else

    An engine reads its MCP configuration, launches the command it finds, and
    waits for a JSON-RPC handshake. Which ctxloom SUBCOMMAND that entry names
    is therefore the whole of whether the engine ends up with ctxloom's tools —
    and the difference is invisible to every check that only asks whether the
    entry is there.

    # ARGV, NOT PRESENCE. Every other assertion in this file is satisfied by an
    # entry naming any subcommand at all. This one reads the subcommand out of
    # each engine's own native shape and pins it. opencode's shape differs (it
    # folds the binary and its arguments into ONE `command` array rather than
    # carrying a separate `args`); the rows below mirror the materialize
    # outline above, and the opencode shape is pinned in the doctor check's own
    # tests instead.
    Scenario Outline: <engine>'s generated configuration launches the protocol server
      Given Carol's team profile carries a shared fragment, command, MCP server, and hook
      When Alice materializes the team profile for <engine>
      Then the materialized <engine> MCP configuration invokes ctxloom's own server as "mcp serve"

      Examples:
        | engine      |
        | claude-code |
        | kiro        |
        | codex       |

  Rule: The bare noun answers a person and refuses a protocol client

    `ctxloom mcp` on its own lists this project's configured MCP servers, the
    way every other noun answers its own bare form. A caller that is NOT a
    person at a terminal is almost always an engine that has opened a pipe and
    is waiting for JSON-RPC, and a server listing written into that pipe is
    indistinguishable from a hang: nothing frames, nothing errors, the session
    comes up with no ctxloom tools and no cause named anywhere. So off a
    terminal the bare noun refuses, and says what to run instead.

    # Every command in this suite is a subprocess on pipes, which IS the
    # machine side — the harness has no terminal to offer. The human half, the
    # listing itself, is driven in internal/cli's mcp_bare_test.go, where the
    # terminal predicate can be presented either way.
    # The invocation is asserted BACKTICKED. A bare "ctxloom mcp serve" is a
    # substring of "ctxloom mcp server list", which this same message also
    # names, so an undelimited assertion stays green against a message that
    # stopped naming the server at all — measured, by deleting exactly that.
    Scenario: A client pointed at the bare noun is told which invocation speaks the protocol
      Given an initialized ctxloom project
      When I run "ctxloom mcp"
      Then the command fails
      And the output contains "`ctxloom mcp serve`"

    # The shape a script reaches for next. Asking for JSON does not make a
    # listing safe to hand a caller that wanted a protocol stream, so the
    # refusal holds and names the leaf that produces the servers as data.
    Scenario: Asking the bare noun for JSON is refused and points at the listing leaf
      Given an initialized ctxloom project
      When I run "ctxloom --format json mcp"
      Then the command fails
      And the output contains "ctxloom mcp server list"
