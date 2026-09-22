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

  Rule: ctxloom's own MCP server ships in ctxloom's own companion loadout, so it needs no configuration

    # The entry whose absence costs the user every ctxloom tool. Nothing in
    # the project asks for it: ctxloom is its own companion, and a companion's
    # servers are registered in every session unconditionally, which is what
    # makes "the loadout registers the server" true by default. The SOURCE is
    # asserted alongside the name — a name alone would still be satisfied by an
    # implementation that hard-coded the entry back into the writers.
    Scenario: A project that configures nothing still registers ctxloom's own server
      Given an initialized ctxloom project
      When I run "ctxloom mcp server list"
      Then the command succeeds
      And the output contains "ctxloom"
      And the output contains "ctxloom+companion:ctxloom"

  Rule: Showing a server surfaces how it is reached, not just its name

    # `show` prints the server's own name as a header, echoed straight from
    # the argument the scenario already passed — so an entry with nothing in
    # it would still satisfy an assertion that only checks for the name. The
    # bundle identity exists nowhere in the output except because the lookup
    # really resolved the registered server.
    #
    # ctxloom's own entry is the companion's DYNAMIC declaration: served by
    # the running session's endpoint, with nothing executable on it. At rest
    # `show` DESCRIBES that — there is no command to print, and printing one
    # would be describing a server that does not exist.
    # Tabled by format: `mcp server show` is wired to emit(), so off a
    # terminal (which this harness always is) the no-flag row gets the JSON
    # GetMCPServerResult, not the text lines.
    Scenario Outline: Showing an MCP server's configuration
      Given an initialized ctxloom project
      When Alice inspects the server's configuration:
        """
        ctxloom <flags> mcp server show ctxloom
        """
      Then the command succeeds
      And the output reports "entries.0.source" as "<names the bundle>"
      And the output reports "entries.0.served_by" as "<names the endpoint>"
      And the output does not contain "mcp serve"

      Examples: no --format at all takes the derived default off a terminal; an explicit one wins in both directions
        | flags         | names the bundle            | names the endpoint                        |
        |               | ctxloom+companion:ctxloom   | session-endpoint                          |
        | --format json | ctxloom+companion:ctxloom   | session-endpoint                          |
        | --format text | ctxloom+companion:ctxloom   | Served by: the running session's endpoint |

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
      And the project already has the file ".ctxloom/content/bundles/v2/demo.yaml":
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
      And the file ".ctxloom/content/bundles/v2/demo.yaml" contains "EDITED-BY-TEST"
      # The rest of the entry must survive the round trip. An implementation
      # that rewrote the manifest from just the edited field would satisfy
      # the line above while silently dropping the arguments.
      And the file ".ctxloom/content/bundles/v2/demo.yaml" contains "KEEP-THIS-ARGUMENT"

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
    # uses a JSON "mcpServers" table shape.
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

  Rule: A materialized registry launches ctxloom as nothing — the session serves ctxloom's own server

    An engine reads its MCP configuration and launches every command it
    finds. ctxloom's own server is not one of them: it is served by the
    running session's endpoint, injected into the session's own registry at
    session start with that session's URL and bearer. A registry materialized
    AT REST therefore carries the shared servers a bundle declares and NO
    entry under ctxloom's name — an entry there would name a command that
    speaks no protocol, and the engine launching it would come up with none
    of ctxloom's tools and nothing saying why.

    # ABSENCE UNDER THE KEY, NOT PRESENCE OF ANYTHING. The shared server's
    # presence is asserted by the Rule above; this one reads the registry in
    # the engine's own native shape and pins that ctxloom's key is not in it.
    Scenario Outline: <engine>'s materialized configuration registers no ctxloom server
      Given Carol's team profile carries a shared fragment, command, MCP server, and hook
      When Alice materializes the team profile for <engine>
      Then the materialized <engine> MCP configuration registers no server under ctxloom's own name

      Examples:
        | engine      |
        | claude-code |

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
    # listing itself, is driven in internal/adapters/cli's mcp_bare_test.go, where the
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
