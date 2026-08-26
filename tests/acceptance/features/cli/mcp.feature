@doc
Feature: mcp — the MCP servers ctxloom hands to every engine

  An MCP server is a tool process — a filesystem walker, a database client, a
  search index — that an AI assistant can call. Every one of them lives in a
  BUNDLE: composing a bundle that declares an `mcp:` server is what registers
  it, and that one registration is what every backend's own MCP configuration
  is generated from, and what an agent sees listed as a resource inside the
  running session. `ctxloom mcp server` reads that roster; the only write it
  offers is `edit`, which edits the bundle the server lives in.

  This is the comprehensive per-noun spec: what the noun DOES, leaf by leaf,
  including the refusals and the shapes that only matter to a machine. The
  narrative version — Carol's team sharing one profile that reaches four
  engines in their own native format — is
  journeys/j000400_multi_engine.feature, which asserts what a PERSON sees.

  Rule: Composing a bundle that declares a server makes its command reachable from every axis ctxloom exposes

    # "toolserver" is the bundle's key, and it is the listed name and the
    # resource entry alike — so an implementation that stored the name but
    # dropped the command string would still satisfy every check that only
    # looks for "toolserver". The command's own executable is checked at each
    # axis alongside the name for exactly that reason: a name echoed back
    # three times proves nothing about whether the command it names actually
    # landed.
    Scenario: Alice's team bundle registers a shared MCP server
      Given Carol's team profile carries a shared fragment, command, MCP server, and hook
      And I run "ctxloom agent create dev --profiles team"
      And I run "ctxloom agent default dev"
      When I run "ctxloom mcp server list"
      Then the command succeeds
      And the output contains "toolserver"
      When the agent reads resource "ctxloom://mcp-servers"
      Then the resource contains "toolserver"

  Rule: ctxloom's own MCP server ships in a builtin bundle, so it needs no configuration

    # The entry whose absence costs the user every ctxloom tool. Nothing in
    # the project asks for it: the builtin ctxloom bundle is injected into
    # every session unconditionally, which is what makes "composing the bundle
    # registers the server" true by default. The BUNDLE is asserted alongside
    # the name — a name alone would still be satisfied by an implementation
    # that hard-coded the entry back into the writers.
    Scenario: A project that configures nothing still registers ctxloom's own server
      Given an initialized ctxloom project
      When I run "ctxloom mcp server list"
      Then the command succeeds
      And the output contains "ctxloom"
      And the output contains "ctxloom+builtin:ctxloom-mcp"

  Rule: Showing a server surfaces its stored command, not just its name

    # `show` prints the server's own name as a header, echoed straight from
    # the argument the scenario already passed — so a command of "" would
    # still satisfy an assertion that only checks for the name. The bundle
    # identity exists nowhere in the output except because the lookup really
    # resolved the registered server.
    # Tabled by format: `mcp server show` is wired to emit(), so off a
    # terminal (which this harness always is) the no-flag row now gets the
    # JSON GetMCPServerResult, not the "Command:"/"Args:" text lines the old
    # assertion checked unconditionally.
    Scenario Outline: Showing an MCP server's configuration
      Given an initialized ctxloom project
      When Alice inspects the server's configuration:
        """
        ctxloom <flags> mcp server show ctxloom
        """
      Then the command succeeds
      And the output reports "entries.0.source" as "<names the bundle>"
      And the output reports "entries.0.args" containing "<names the verb>"

      Examples: no --format at all takes the derived default off a terminal; an explicit one wins in both directions
        | flags         | names the bundle            | names the verb |
        |               | ctxloom+builtin:ctxloom-mcp | serve           |
        | --format json | ctxloom+builtin:ctxloom-mcp | serve           |
        | --format text | ctxloom+builtin:ctxloom-mcp | mcp serve       |

  Rule: There is no config-level MCP store to create in or remove from

    # The ruling this noun is shaped by: an MCP server lives in a bundle and
    # nowhere else. `create` and `remove` are not hidden or deprecated, they
    # are GONE — a project adds a server by composing a bundle and withholds
    # one with a profile's exclude_mcp.
    Scenario Outline: The config-level write leaves do not exist
      Given an initialized ctxloom project
      When I run "ctxloom mcp server <leaf> tools"
      Then the command fails
      And the output contains "unknown command"

      Examples:
        | leaf   |
        | create |
        | remove |

    # ctxloom's own registration was a config flag with two commands behind
    # it. Withholding the bundle is the replacement, so the toggles go too.
    Scenario Outline: The auto-registration toggles do not exist
      Given an initialized ctxloom project
      When I run "ctxloom mcp <leaf>"
      Then the command fails
      And the output contains "unknown command"

      Examples:
        | leaf       |
        | register   |
        | unregister |

  Rule: A config carrying the retired native mcp: block is refused, not ignored

    # A setting that looks applied and is not is the worse outcome, so a
    # config that still names the retired key fails at load with the key named
    # — the same treatment any key ctxloom does not know gets, because after
    # this ruling those are the same thing. Asserting the KEY, not just the
    # failure, is what separates this from any other reason a load might fail.
    Scenario: A native mcp: block fails the load and names the key
      Given an initialized ctxloom project
      And the project already has the file ".ctxloom/config.yaml":
        """
        version: 6
        mcp:
            auto_register_ctxloom: true
            servers:
                tools:
                    command: echo
        """
      When I run "ctxloom mcp server list"
      Then the output contains "unknown key `mcp`"
      And the output contains "IGNORED"

  Rule: Editing a bundle's server is a round trip through the user's editor

    # `mcp server edit` is the only leaf here that hands control to an
    # external program and takes the result back, so the claim is the ROUND
    # TRIP: what the editor wrote must reach the bundle on disk. A scenario
    # asserting exit 0 would pass against an editor that was never launched.
    #
    # The fixture authors the bundle's mcp section directly because that IS
    # how a server comes to exist: there is no other store to create one in.
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
    # format and asserts the actual command field under the actual server name
    # — never a bare file-exists and never a substring of a key name (the
    # vacuousness a ".mcp.json" contains "ctxloom" check would carry). claude
    # and kiro share one JSON "mcpServers" table shape.
    #
    # codex has NO ROW, and that is a product fact rather than a coverage gap:
    # its servers fold into $CODEX_HOME/config.toml, and the only $CODEX_HOME
    # ctxloom writes is the per-session one an agent launch creates. A static
    # materialize has no session and so no file — the scenario below asserts
    # exactly that, over the whole tree.
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

    # codex's half of the same claim, stated as the absence it is. Both halves
    # are asserted: nothing landed, AND the report says where it does come from
    # — a materialize that silently dropped a team's MCP registration would
    # satisfy the first alone.
    Scenario: A shared MCP server does not materialize for codex, and the report says why
      Given Carol's team profile carries a shared fragment, command, MCP server, and hook
      When Alice materializes the team profile for codex
      Then no codex surface anywhere in the materialized tree carries the shared MCP server's command
      And the materialize report says codex delivers those surfaces per-session at launch

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
    # tests instead. codex is absent for the same reason as in the outline
    # above: a static materialize writes it no MCP registry at all, so there is
    # no argv here to read. Its per-session one is written by the launch path
    # and pinned by internal/codex's own TestWriteSettings_MCPCommandOverride.
    Scenario Outline: <engine>'s generated configuration launches the protocol server
      Given Carol's team profile carries a shared fragment, command, MCP server, and hook
      When Alice materializes the team profile for <engine>
      Then the materialized <engine> MCP configuration invokes ctxloom's own server as "mcp serve"

      Examples:
        | engine      |
        | claude-code |
        | kiro        |

  Rule: The bare noun answers a person and refuses a protocol client

    `ctxloom mcp` on its own lists the MCP servers this project registers, the
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
