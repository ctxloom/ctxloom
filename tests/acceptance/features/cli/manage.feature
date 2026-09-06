@doc
Feature: manage — wiring ctxloom into a project, and taking it back out

  `ctxloom manage` is the noun that owns a project's HARNESS: the `.ctxloom`
  scaffold, the engine's own hooks and statusline, MCP registration, and the
  gitignore entries that keep ctxloom's private state out of source control.
  It is the first command anyone runs and the one they run again when they
  want to leave.

  This is the comprehensive per-noun spec: what the noun DOES, leaf by leaf,
  including the refusals and the shapes that only matter to a machine. The
  narrative version — Alice's first hour, and satisfying herself she can back
  out — is journeys/j000100_adopt_and_back_out.feature, which asserts what a PERSON
  sees.

  Rule: Installing wires the project for one engine, and validates which

    `manage install` scaffolds `.ctxloom` and writes the chosen engine's own
    native configuration. Which files those are differs per engine and is the
    whole point of the engine axis; what they must all do is put the project's
    context in front of that engine and register ctxloom as an MCP server.

    # PAYLOAD, NOT EXISTENCE. An empty file exists just as convincingly as a
    # wired one, so every row names content the surface must actually carry.
    #
    # EVERY ROW IS UNTAGGED because every row is hermetic: `manage install`
    # WRITES an engine's config files and never launches the engine, so no
    # credential is needed and nothing gates behind @live. `mock` is a
    # first-class row — a registered engine with real surfaces of its own.
    #
    # CONTEXT ARRIVES TWO WAYS, and the table says which each engine uses. An
    # engine with a session hook gets the context injected at session start,
    # which is live: it reflects the profile as composed at launch. An engine
    # without one reads a file ctxloom materialized earlier. Where the hook is
    # available it is what this asserts, because it is the delivery path that
    # actually runs.
    Scenario Outline: Alice wires an empty project for her engine
      Given an empty project directory
      When Alice installs ctxloom for <engine>:
        """
        ctxloom manage install --engine <engine>
        """
      Then the command succeeds
      And the file ".ctxloom/config.yaml" is valid YAML
      And the file "<context_surface>" contains "<context_marker>"
      And the file ".ctxloom/.gitignore" contains "ctxloom"

      Examples: engines with a session hook — context is injected at launch
        | engine      | context_surface       | context_marker      |
        | claude-code | .claude/settings.json | hook inject-context |

      Examples: engines without one — context is read from a materialized file
        | engine      | context_surface                 | context_marker         |
        | mock        | MOCK_CONTEXT.md                 | ctxloom:context:begin  |

    # ONE CLAIM, MANY SHAPES. Every engine ctxloom drives gets the SAME
    # registration — ctxloom as an MCP server, launched by the ctxloom binary
    # with the `mcp` subcommand — and each writes it into a different file in
    # a different dialect. Some fold it into a config file they already own;
    # others give it a file of its own.
    #
    # The marker is a field from INSIDE the server's body, never the key that
    # names it: a registration with the right key and an empty body gives the
    # engine a server with no command to launch, and a key-only assertion
    # cannot tell that apart from a working one.
    # TWO engines carry an MCP surface, which is what makes this a fan-out
    # rather than a single-row claim that proves no dialect at all.
    Scenario Outline: Every engine gets ctxloom registered as an MCP server, in its own dialect
      Given an empty project directory
      When Alice installs ctxloom for <engine>:
        """
        ctxloom manage install --engine <engine>
        """
      Then the command succeeds
      And the file "<mcp_surface>" contains "<server_key>"
      And the file "<mcp_surface>" contains "<launch_marker>"

      # No "folded into a config the engine already owns" block: no shipped
      # engine folds its MCP registry into another surface today. The block is
      # restored, not invented, when one does.
      Examples: a file of its own
        | engine      | mcp_surface     | server_key | launch_marker |
        | claude-code | .mcp.json       | mcpServers | ${CLAUDE_PROJECT_DIR} |
        | mock        | .mock/mcp.json  | mcpServers | ctxloom |

    # THE COMMAND SURFACE, the third thing install writes. ctxloom ships
    # first-party commands, and every engine gets them in its own idiom: a flat
    # markdown file for the engines that have slash-commands, a SKILL.md
    # package directory for the engines whose equivalent is an Agent Skill.
    # The path column is where the idioms differ; the claim does not.
    #
    # ONE marker for every row, and it is the command's BODY — not its name,
    # not its frontmatter. A materializer that creates the right paths and
    # writes only frontmatter into them satisfies a name check and delivers a
    # command that does nothing.
    #
    # mock has no row: it is a registered engine with context and skills
    # surfaces but no command surface of its own, and asserting an absent
    # surface would be asserting nothing.
    #
    # Two engines carry a command surface, which is what makes "in its own
    # idiom" a claim two rows can disagree about.
    Scenario Outline: Every engine gets ctxloom's shipped commands in its own idiom
      Given an empty project directory
      When Alice installs ctxloom for <engine>:
        """
        ctxloom manage install --engine <engine>
        """
      Then the command succeeds
      And the file "<command_surface>" contains "Scan the current project and discover matching ctxloom content"

      # An engine whose prompts are home-global — delivered into a session's
      # own home at launch — has no row: a static install writes none.
      Examples: a flat command file
        | engine      | command_surface              |
        | claude-code | .claude/commands/discover.md |
        | mock        | .mock/commands/discover.md   |

    # NOTHING IS WRITTEN UNTIL THE ARGUMENT IS UNDERSTOOD. The "does not
    # exist" checks are the load-bearing half — a wrong-but-loud message
    # satisfies the first three assertions on its own, and only the absent
    # files catch an install that scaffolded a project around an engine nobody
    # supports.
    Scenario: A misspelled engine is refused by name, and nothing is scaffolded
      Given an empty project directory
      When Alice mistypes the engine name:
        """
        ctxloom manage install --engine totally-bogus-engine
        """
      Then the command fails
      And the output contains "unknown engine"
      And the output contains "totally-bogus-engine"
      And the output contains "claude-code"
      And the file ".ctxloom/config.yaml" does not exist
      And the file ".mcp.json" does not exist

  Rule: Config create selects the engine without writing any engine files

    `config create` scaffolds `.ctxloom` and records which engine the project
    uses — it writes no engine-native files at all. Every engine therefore
    produces a byte-identical tree apart from config.yaml, so the config's
    CONTENTS are the only place the flag is observable and a file-exists check
    proves nothing about which engine was asked for.

    Scenario: Config create scaffolds a config in a bare project
      Given an empty project directory
      When Alice scaffolds a config without wiring any engine:
        """
        ctxloom config create --engine claude-code
        """
      Then the command succeeds
      And the file ".ctxloom/config.yaml" exists

    Scenario Outline: Config create records which engine the project uses
      Given an empty project directory
      When Alice scaffolds a config for <engine>:
        """
        ctxloom config create --engine <engine>
        """
      Then the command succeeds
      And the file ".ctxloom/config.yaml" contains "llm: <engine>"
      And the file ".ctxloom/config.yaml" contains "type: <engine>"
      And the file ".ctxloom/config.yaml" contains "primary: <engine>"

      Examples:
        | engine |
        | mock   |

  Rule: The wiring can be inspected, and reports per-surface state

    # The engine NAME appears in both "claude-code: not configured" and
    # "claude-code: hooks=true ...", so matching it says nothing about whether
    # anything was wired. The per-surface flags are what "wired" means, so the
    # assertion is a regex over them.
    Scenario Outline: Check reports which surfaces are actually wired
      Given an initialized ctxloom project
      When Alice wires the hooks in and asks what is configured:
        """
        ctxloom manage hooks install
        ctxloom manage check <flags>
        """
      Then the command succeeds
      And the output reports "backends[backend=claude-code].hooks_present" as "<hooks are wired>"
      And the output reports "backends[backend=claude-code].mcp_present" as "<mcp is wired>"

      Examples: no --format at all takes the derived default off a terminal; an explicit one wins in both directions
        | flags         | hooks are wired | mcp is wired |
        |               | true              | true          |
        | --format json | true              | true          |
        | --format text | hooks=true        | mcp=true      |

  Rule: A materialized hook reaches every engine in its own native shape

    # ONE hook, TWO files, TWO formats — PARSED in its own format and
    # asserted on the actual command field under the right event, never a
    # bare file-exists or a substring of a key name.
    Scenario Outline: A team's hook reaches every engine's own hook surface
      Given Carol's team profile carries a shared fragment, command, MCP server, and hook
      When Alice materializes the team profile for <engine>
      Then the materialized <engine> hook configuration carries the shared hook's command, in its own native shape

      Examples:
        | engine      |
        | claude-code |

  Rule: Hooks install, list, and genuinely uninstall

    # A claim scoped to hooks.SessionStart specifically, parsed rather than a
    # whole-file substring: ctxloom's own statusLine command also carries the
    # substring "ctxloom hook" (`ctxloom hook hud`), so a bare `contains`
    # check here is satisfied by the statusline whether or not any hook was
    # ever installed or removed.
    Scenario: Hooks can be installed, inspected, and actually removed
      Given an initialized ctxloom project
      When Alice installs the hooks:
        """
        ctxloom manage hooks install
        """
      Then the command succeeds
      And the file ".claude/settings.json" registers a SessionStart hook whose command contains "hook inject-context"
      When Alice inspects and then removes them:
        """
        ctxloom manage hooks check
        ctxloom manage hooks uninstall
        """
      Then the command succeeds
      And the file ".claude/settings.json" registers no SessionStart hook whose command contains "hook inject-context"

    Scenario: Re-applying hooks does not duplicate them
      Given an initialized ctxloom project
      When Alice installs the hooks twice:
        """
        ctxloom manage hooks install
        ctxloom manage hooks install
        """
      Then the command succeeds
      And the file ".claude/settings.json" contains "ctxloom hook session-bind" exactly 1 times

    # Hooks merge across sources by pure APPEND, and each bundle's `order:`
    # sequences only its own hooks within an event. The fixture makes
    # DECLARATION ORDER DISAGREE with `order:` on purpose — "second" is written
    # first in the YAML and ordered last — so a build that ignored the field
    # prints them the other way round and the regex catches it.
    Scenario Outline: Listing hooks shows the resolved order and where each came from
      Given an initialized ctxloom project
      And the project already has the file ".ctxloom/content/bundles/v1/hooked.yaml":
        """
        name: hooked
        description: ships two pre_tool hooks whose order contradicts their position
        hooks:
          pre_tool:
            - type: command
              command: echo HOOKLIST-SECOND
              order: 200
            - type: command
              command: echo HOOKLIST-FIRST
              order: 100
        """
      And a profile "hooky" with bundle "hooked"
      When Alice asks which hooks run on a tool call:
        """
        ctxloom manage hooks list --event pre_tool --profile hooky <flags>
        """
      Then the command succeeds
      # The order claim stays a plain ordering check: this dev machine's real
      # companions (ltk, taskloom, reprise) also declare pre_tool hooks and
      # are merged in ahead of these two, so a POSITION selector is not safe
      # here — position 1 may be a companion's hook, not either of this
      # fixture's. Selecting by each hook's own unique command text is.
      And the output contains "HOOKLIST-FIRST"
      And the output contains "HOOKLIST-SECOND"
      And the output matches "HOOKLIST-FIRST[\s\S]*HOOKLIST-SECOND"
      # The origin claim, addressed as the two fields a substring of a
      # rendered "bundle X" marker would conflate.
      And the output reports "events[event=pre_tool].hooks[command=echo HOOKLIST-FIRST].source_kind" as "<names the origin kind>"
      And the output reports "events[event=pre_tool].hooks[command=echo HOOKLIST-FIRST].source" as "<names the origin>"

      Examples: no --format at all takes the derived default off a terminal; an explicit one wins in both directions
        | flags         | names the origin kind        | names the origin              |
        |               | bundle                         | ctxloom+local:hooked            |
        | --format json | bundle                         | ctxloom+local:hooked            |
        | --format text | bundle ctxloom+local:hooked    | bundle ctxloom+local:hooked     |

    # Each hook must name ONE place to go. A coarse "local" label covering every
    # declaration site at once leaves a user searching all of them, so the
    # fixture declares hooks in two DIFFERENT kinds of place and requires them to
    # be labelled distinctly: a profile the project authored, and a companion
    # binary found on PATH. Naming the profile is not enough on its own — the
    # label has to carry the KIND, since "which file do I open" and "which
    # profile is it" are different questions.
    Scenario Outline: The hook list names the specific place each hook was declared
      Given an initialized ctxloom project
      And the project already has the file ".ctxloom/profiles/dir-prov.yaml":
        """
        hooks:
          unified:
            pre_tool:
              - type: command
                command: echo PROV-FROM-DIRECTORY-PROFILE
        """
      When Alice asks where each hook came from:
        """
        ctxloom manage hooks list --event pre_tool --profile dir-prov <flags>
        """
      Then the command succeeds
      # Selected by source_kind: this fixture declares exactly one
      # profile-directory hook, so the selection cannot be ambiguous. A
      # companion-sourced hook is NOT addressed by path here — this machine
      # ships multiple first-party companions (ltk, taskloom, reprise), any
      # number of which may resolve exempt, so a [source_kind=companion]
      # selector would be genuinely ambiguous rather than a safe pick; its
      # presence is proven as a plain substring instead.
      And the output reports "events[event=pre_tool].hooks[source_kind=profile-directory].command" as "<names the hook>"
      And the output reports "events[event=pre_tool].hooks[source_kind=profile-directory].source" as "<names the profile>"
      And the output contains "<a companion-sourced hook is present too>"
      And the output does not contain "[local]"
      # The machine form carries the same specific kind, plus the position the
      # MERGE gave each hook — so a later reordering is visible as a move
      # rather than as an unexplained final number.
      When Alice asks for the same answer in machine form:
        """
        ctxloom manage hooks list --event pre_tool --profile dir-prov --format json
        """
      Then the command succeeds
      And the output contains "profile-directory"
      And the output contains "declared"

      Examples: no --format at all takes the derived default off a terminal; an explicit one wins in both directions
        | flags         | names the hook                    | names the profile | a companion-sourced hook is present too |
        |               | echo PROV-FROM-DIRECTORY-PROFILE  | dir-prov            | companion                                 |
        | --format json | echo PROV-FROM-DIRECTORY-PROFILE  | dir-prov            | companion                                 |
        | --format text | PROV-FROM-DIRECTORY-PROFILE       | dir-prov            | companion                                 |

    # Parsing is the claim. Event names alone appear in the human listing too,
    # so matching them would pass against a command that ignored --format json
    # and rendered text.
    Scenario: The hook list has a machine-readable form
      Given an initialized ctxloom project
      When Alice asks for the hook list as JSON:
        """
        ctxloom manage hooks list --format json
        """
      Then the command succeeds
      And the output is valid JSON
      And the JSON output array "events" contains an object whose "event" is "post_file_edit"
      And the JSON output array "events" contains an object whose "event" is "session_start"
      And every object in the JSON output array "events" has a non-empty "event"

    # A typo must not resolve to "nothing runs on that event" — a confident
    # wrong answer from an inspect command is worse than no inspect command.
    Scenario: An unknown event name is refused rather than answered empty
      Given an initialized ctxloom project
      When Alice mistypes an event name:
        """
        ctxloom manage hooks list --event pre_toll
        """
      Then the command fails
      And the output contains "pre_toll"

  Rule: The statusline is controlled separately from the hooks

    Scenario: Statusline can be disabled and re-enabled
      Given an initialized ctxloom project
      When Alice turns the statusline off and re-applies the hooks:
        """
        ctxloom manage statusline uninstall
        ctxloom manage hooks install
        """
      Then the command succeeds
      And the file ".claude/settings.json" does not contain "hook hud"
      When Alice turns it back on and re-applies:
        """
        ctxloom manage statusline install
        ctxloom manage hooks install
        """
      Then the command succeeds
      And the file ".claude/settings.json" contains "hook hud"

  Rule: A file the user wrote by hand is never damaged

    # A rewrite of settings.json only ADDS ctxloom's own keys: whatever the
    # user wrote by hand comes back byte-for-byte, including integers no
    # float64 can hold exactly. The assertion is on the persisted digits,
    # because a round-trip through a generic JSON decoder loses them while
    # still reporting success.
    Scenario: Installing hooks preserves the exact numbers the user wrote by hand
      Given an initialized ctxloom project
      And the project already has the file ".claude/settings.json":
        """
        {
          "awkwardNumber": 1234567890123456789,
          "nested": { "id": -9223372036854775808 },
          "permissions": { "allow": ["Read"], "quota": 18446744073709551615 }
        }
        """
      When Alice installs the hooks over her hand-written settings:
        """
        ctxloom manage hooks install
        """
      Then the command succeeds
      And the file ".claude/settings.json" registers a SessionStart hook whose command contains "hook inject-context"
      And the file ".claude/settings.json" contains "1234567890123456789"
      And the file ".claude/settings.json" contains "-9223372036854775808"
      And the file ".claude/settings.json" contains "18446744073709551615"
      And the file ".claude/settings.json" contains "Read"

    # ctxloom is the wrong party to decide the fate of a setting it just failed
    # to read: the write is refused and the user's file is left exactly as it
    # was.
    #
    # "the command fails" is the load-bearing line here, not the message. This
    # scenario once asserted only the diagnostic and the untouched file --
    # because the exit code could NOT be relied on: a refused engine apply
    # warned and then exited 0, so a script or CI step gating on the exit code
    # believed the hooks were installed. The test was shaped around the bug.
    Scenario: A statusline ctxloom cannot read refuses the write instead of replacing it
      Given an initialized ctxloom project
      And the project already has the file ".claude/settings.json":
        """
        {
          "env": { "A": "b" },
          "statusLine": { "type": "command", "command": { "exec": "ccusage" } }
        }
        """
      When Alice installs the hooks over a statusline ctxloom cannot parse:
        """
        ctxloom manage hooks install
        """
      Then the command fails
      And the output contains "refusing to write settings.json"
      And the file ".claude/settings.json" contains "ccusage"
      And the file ".claude/settings.json" registers no SessionStart hook whose command contains "hook inject-context"

  Rule: ctxloom's private state stays out of source control

    # The private-state rules live in a NESTED, tracked .ctxloom/.gitignore, so
    # they travel with the directory and across worktrees. Root must NOT carry a
    # blanket .ctxloom rule: git does not descend into an ignored directory, so a
    # blanket there would make the nested file unreadable and every rule in it
    # silently dead. Root keeps only the patterns naming paths OUTSIDE .ctxloom,
    # which no nested file can express -- and one of them is a credential, so
    # asserting it stays is not decoration.
    Scenario: Gitignore install excludes ctxloom's private state
      Given an initialized ctxloom project
      When Alice adds ctxloom's gitignore entries:
        """
        ctxloom manage gitignore install
        """
      Then the command succeeds
      And the file ".ctxloom/.gitignore" contains "/cache"
      And the file ".ctxloom/.gitignore" contains "/state"
      And the file ".gitignore" does not exist

    # The shipped defect this replaced: the writer emitted a fresh comment header
    # above only the MISSING patterns, so every run added another header and the
    # copies accumulated, one of them captioning an unrelated entry. Installing
    # twice is what makes the header count meaningful -- a single install would
    # read 1 either way.
    Scenario: Repeated gitignore installs do not stack comment headers
      Given an initialized ctxloom project
      When Alice adds ctxloom's gitignore entries:
        """
        ctxloom manage gitignore install
        """
      And Alice adds ctxloom's gitignore entries:
        """
        ctxloom manage gitignore install
        """
      Then the command succeeds
      And the file ".ctxloom/.gitignore" contains "ctxloom's private working state" exactly 1 times

    # The dirty-tree-commit acknowledgement moved out of config.yaml into its
    # own gitignored state-store record: the value records a prior HUMAN
    # authorization, not configuration, and is ScopeNever — no config layer may
    # set it. `manage commit` is the scriptable counterpart to
    # `ctxloom init`'s interview question, the only other writer.
    # The two states are asserted rather than "absent, then present": a record
    # that reads `approved: true` after granting and `approved: false` after
    # revoking cannot be a file that was already lying there, so the pair
    # proves both commands WRITE. Checking for absence first would prove less
    # and would assert a starting state no command produced.
    Scenario: The dirty-tree-commit acknowledgement can be granted and revoked
      Given an initialized ctxloom project
      When Alice grants the acknowledgement:
        """
        ctxloom manage commit trust
        """
      Then the command succeeds
      And the output contains "granted"
      And the file ".ctxloom/state/dirty_tree_commit_ack.yaml" exists
      And the file ".ctxloom/state/dirty_tree_commit_ack.yaml" contains "approved: true"
      When Alice revokes it again:
        """
        ctxloom manage commit untrust
        """
      Then the command succeeds
      And the output contains "revoked"
      And the file ".ctxloom/state/dirty_tree_commit_ack.yaml" contains "approved: false"

  Rule: Uninstalling removes what ctxloom wired and keeps what the team authored

    # BOTH halves are asserted on payload: the hook command is gone from
    # settings.json, the MCP registration is gone from .mcp.json, and .ctxloom
    # survives. A no-op uninstall that prints its success line goes red on the
    # "does not contain" assertions; one that deletes .ctxloom goes red on the
    # survival assertion. The success message alone distinguishes neither.
    #
    # The precondition and the removal check name the hook and the statusLine
    # SEPARATELY, because `ctxloom hook` reaches only the statusLine — the
    # context hook's command is `'<abs>/ctxloom' hook inject-context`, quoted
    # between the two words. On its own that substring certifies neither half:
    # it is satisfied as a precondition with no context hook ever written, and
    # as a removal check by an uninstall that drops the statusline and leaves
    # the hook behind.
    #
    # The MCP claim PARSES the file rather than checking for the bare
    # substring "ctxloom": .mcp.json containing that word somewhere is
    # satisfied by almost anything, so both the precondition and the removal
    # check name the actual mcpServers registration by its own key.
    Scenario: Uninstall strips the harness but keeps the project's own content
      Given an empty project directory
      When Alice wires ctxloom in:
        """
        ctxloom manage install --engine claude-code
        """
      Then the command succeeds
      And the file ".claude/settings.json" contains "hook inject-context"
      And the file ".claude/settings.json" contains "ctxloom hook hud"
      And the file ".mcp.json" registers an MCP server named "ctxloom"
      When Alice takes it back out:
        """
        ctxloom manage uninstall
        """
      Then the command succeeds
      And the file ".claude/settings.json" does not contain "hook inject-context"
      And the file ".claude/settings.json" registers no SessionStart hook whose command contains "hook inject-context"
      And the file ".mcp.json" registers no MCP server named "ctxloom"
      And the file ".ctxloom/config.yaml" exists
