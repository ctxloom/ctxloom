@doc
Feature: agent — the bindings that decide what runs, on what context, and where

  Covers: `ctxloom agent list`, `agent show`, `agent create`, `agent edit`,
  `agent default`, `agent remove`, and the bare `ctxloom agent` form.

  An agent is a NAME for a decision you would otherwise retype: which LLM
  engine, which composed profiles, and — the axis that makes it more than a
  shortcut — which RUNTIME that engine process executes in: host, container-rootless
  or container-rootful.
  `ctxloom run --agent dev` is then one word instead of four flags, and every
  colleague who runs it gets the same four.

  Agents are LOCAL and stay local. They live under the `agents:` key of
  .ctxloom/config.yaml — the one source — and are never
  shipped in a bundle or pulled from a remote: the engine you pay for and the
  runtime you are willing to execute in are yours to choose, and a publisher
  who could choose them for you would be choosing what runs on your machine.

  WHAT A BINDING IS NOT is the other half of the definition. It carries no
  workspace choice — worktree vs. shared directory is a SESSION trait picked at
  invocation (`run --workspace`), because the same agent is run both ways on the
  same day. And a binding is not a session: nothing here starts anything.

  The engine labels these bindings name are the other noun — see cli/llm.feature.

  This is the comprehensive per-noun spec: what the noun DOES, leaf by leaf,
  hermetically against a seeded project.

  Rule: A binding is written down, and read back whole

    Every axis an invocation names is stored, and the listing reads all of them
    back. Asserting only that the name reappears would pass against a writer
    that recorded the name and dropped the engine, the profiles and the
    runtime — which is a binding that binds nothing, reported as success.

    # Emptiness is the one claim the value-addressing steps cannot make, so it
    # is asserted positively: the payload IS empty, and the renderer says so in
    # its own words. Both sentences the text arm owes the reader are kept, each
    # on its own line, because they answer different questions — that there are
    # none, and where you would write one.
    Scenario Outline: A project with no bindings says so, and says where to define one
      Given an initialized ctxloom project
      When Alice asks what agents this project defines:
        """
        ctxloom agent list <flags>
        """
      Then the command succeeds
      And the output reports "$" as empty, saying "No agents defined"
      And the output reports "$" as empty, saying "'agents:' in .ctxloom/config.yaml"

      Examples: no --format at all takes the derived default off a terminal; an explicit one wins in both directions
        | flags         |
        |               |
        | --format json |
        | --format text |

    # The "Created agent" line is composed by the renderer from the request, so
    # it has no field of its own. The structured rows pin the ENTRY the write
    # returned instead — which is the same claim made against the thing that
    # was actually stored, rather than against the echo.
    Scenario Outline: Creating a binding records every axis, and the listing reads each one back
      Given an initialized ctxloom project
      And a profile "dev" exists
      When Alice binds an engine and a runtime to a profile:
        """
        ctxloom agent create developer --llm claude-code --profiles dev --runtime container-rootless <flags>
        """
      Then the command succeeds
      And the output reports "name" as "<the binding written>"
      # The EFFECT, not the echo: the confirmation above is printed from the
      # request, so it is true of a writer that saved nothing.
      And the file ".ctxloom/config.yaml" contains "developer"
      When I run "ctxloom agent list <flags>"
      Then the command succeeds
      And the output reports "[name=developer].llm" as "<the engine>"
      And the output reports "[name=developer].profiles" containing "<the profile>"
      And the output reports "[name=developer].runtime" as "<the runtime>"

      Examples: no --format at all takes the derived default off a terminal; an explicit one wins in both directions
        | flags         | the binding written | the engine       | the profile   | the runtime                   |
        |               | developer           | claude-code      | dev           | container-rootless            |
        | --format json | developer           | claude-code      | dev           | container-rootless            |
        | --format text | Created agent       | llm: claude-code | profiles: dev | runtime: container-rootless   |

    # The bare noun answers the question somebody typing it has, rather than
    # teaching them what they could have typed instead. The help-banner check
    # needs no row of its own: cobra's usage block is prose in every encoding,
    # so its absence is the same assertion whichever one is resolved.
    Scenario Outline: Bare agent lists the bindings
      Given an initialized ctxloom project
      And a profile "dev" exists
      And I run "ctxloom agent create developer --profiles dev"
      When I run "ctxloom agent <flags>"
      Then the command succeeds
      And the output reports "[name=developer].name" as "<the binding listed>"
      And the output does not contain "Available Commands:"

      Examples: no --format at all takes the derived default off a terminal; an explicit one wins in both directions
        | flags         | the binding listed |
        |               | developer          |
        | --format json | developer          |
        | --format text | developer          |

    # `show` answers two questions a listing cannot: what the definition
    # DECLARES, and what that declaration actually RESOLVES to. Both halves are
    # asserted, because a binding whose engine resolves to nothing still lists
    # perfectly.
    # "Composed fragments: N" is a length the renderer computes; the payload
    # carries the fragments themselves and no count. The structured rows name
    # a fragment that must actually be in the composition, which is the
    # stronger claim — a resolution that composed the wrong things reports a
    # perfectly good number.
    Scenario Outline: Showing one binding reports what was declared and what it resolves to
      Given an initialized ctxloom project
      And a profile "dev" exists
      And I run "ctxloom agent create developer --llm claude-code --profiles dev --runtime container-rootless"
      When Alice inspects one binding:
        """
        ctxloom agent show developer <flags>
        """
      Then the command succeeds
      And the output reports "definition.name" as "<the binding named>"
      And the output reports "definition.llm" as "<the engine declared>"
      And the output reports "definition.runtime" as "<the runtime declared>"
      And the output reports "definition.profiles" containing "<the profile declared>"
      And the output reports "resolved.label" as "<what the engine resolves to>"
      And the output reports "resolved.fragments" containing "<what the profiles compose>"

      Examples: no --format at all takes the derived default off a terminal; an explicit one wins in both directions
        | flags         | the binding named | the engine declared         | the runtime declared        | the profile declared | what the engine resolves to | what the profiles compose               |
        |               | developer         | claude-code                 | container-rootless          | dev                  | claude-code                 | ctxloom+local:dev-base#fragments/example |
        | --format json | developer         | claude-code                 | container-rootless          | dev                  | claude-code                 | ctxloom+local:dev-base#fragments/example |
        | --format text | Agent: developer  | Engine (declared): claude-code | Runtime: container-rootless | dev                  | Resolved llm:               | Composed fragments:                     |

    # Reporting nothing for a name nobody defined would be a listing of one,
    # not an answer: "this agent has no axes set" and "this agent does not
    # exist" are different facts about the project.
    Scenario: Showing a binding nobody defined fails, naming what was asked for
      Given an initialized ctxloom project
      When I run "ctxloom agent show nosuchagent"
      Then the command fails
      And the output contains "nosuchagent"

  Rule: create and edit are NOT an upsert — each refuses the case the other owns

    `create` refuses a name that already names an agent; `edit` refuses a name
    no agent has. The upsert `agent set` they replaced silently minted a new
    agent on a typo'd name, and silently overwrote a live one on a reused name.

    A request to make these an upsert again was REJECTED on 2026-08-08
    (taskloom vivacious-overlook). Recorded here because the request will
    recur — two verbs is real friction, and the reason to keep them is not
    obvious from the surface:

    create and edit already share ONE body (operations.SetAgent, which
    j002000_engine_switch.feature relies on for its --llm validation), so the
    refusals are the only thing that distinguishes the verbs. Making them upsert
    is DELETING TWO GUARDS from a shared function, not merging two
    implementations. And a binding carries engine, profiles, runtime and
    permission mode, so a silent overwrite loses whatever the invocation did not
    name — the exact loss the merge scenario below pins.

    If the friction needs answering, an explicit `--force` on create (or a
    separate upsert verb) gives idempotence without removing the guards.

    # The refusal is asserted on the SURVIVING BINDING, not just the exit code:
    # a `create` that refused loudly and overwrote anyway would pass an
    # exit-code-and-message scenario, and that is precisely the defect the
    # guard exists to stop.
    # Only the SURVIVING BINDING is tabled. A command that fails writes its
    # error to stderr and leaves stdout empty in every encoding, so the two
    # refusal-message assertions are prose whichever format is resolved — they
    # are not a rendering the table could vary.
    Scenario Outline: Create refuses an existing name, and leaves the live binding untouched
      Given an initialized ctxloom project
      And a profile "dev" exists
      And a profile "ops" exists
      And I run "ctxloom agent create developer --llm claude-code --profiles dev"
      When Alice re-uses a name that is already bound:
        """
        ctxloom agent create developer --profiles ops
        """
      Then the command fails
      And the output contains "already exists"
      And the output contains "ctxloom agent edit developer"
      When I run "ctxloom agent show developer <flags>"
      Then the command succeeds
      And the output reports "definition.profiles" containing "<the profile that survived>"
      And the output reports "resolved.profiles" not containing "<the profile that did not survive>"

      Examples: no --format at all takes the derived default off a terminal; an explicit one wins in both directions
        | flags         | the profile that survived | the profile that did not survive |
        |               | dev                        | ops                               |
        | --format json | dev                        | ops                               |
        | --format text | dev                        | ops                               |

    # The other half: a typo'd `edit` must not quietly mint a brand-new agent.
    # The listing afterward is what proves nothing was created — the refusal
    # message alone is true of a command that refused AND wrote.
    Scenario Outline: Edit refuses a name no agent has, and creates nothing
      Given an initialized ctxloom project
      And a profile "dev" exists
      When Alice edits a name she has not defined:
        """
        ctxloom agent edit nosuchagent --profiles dev
        """
      Then the command fails
      And the output contains "no agent named"
      And the output contains "ctxloom agent create nosuchagent"
      When I run "ctxloom agent list <flags>"
      Then the command succeeds
      And the output reports "$" as empty, saying "No agents defined"

      Examples: no --format at all takes the derived default off a terminal; an explicit one wins in both directions
        | flags         |
        |               |
        | --format json |
        | --format text |

    # THE LOSS THIS SPLIT EXISTS TO PREVENT. Every field the invocation did not
    # name survives — asserted one axis at a time, because a merge that kept
    # the engine and wiped the profiles would satisfy any single one of them.
    # The text arm reads the surviving profiles off one comma-joined line; the
    # payload carries them as an array, so BOTH are named individually. A
    # single membership check would pass against a merge that kept one profile
    # and dropped the other, which is exactly the loss under test.
    Scenario Outline: An edit naming only --runtime leaves every other axis intact
      Given an initialized ctxloom project
      And a profile "dev" exists
      And a profile "ops" exists
      And I run "ctxloom agent create developer --llm claude-code --profiles dev,ops --runtime host --permissions plan"
      When Alice moves one binding into a container and changes nothing else:
        """
        ctxloom agent edit developer --runtime container-rootless <flags>
        """
      Then the command succeeds
      And the output reports "name" as "<the binding written>"
      When I run "ctxloom agent list <flags>"
      Then the command succeeds
      And the output reports "[name=developer].runtime" as "<the axis that changed>"
      And the output reports "[name=developer].llm" as "<the engine that survived>"
      And the output reports "[name=developer].profiles" containing "<the first profile>"
      And the output reports "[name=developer].profiles" containing "<the second profile>"
      And the output reports "[name=developer].permissions" as "<the posture that survived>"

      Examples: no --format at all takes the derived default off a terminal; an explicit one wins in both directions
        | flags         | the binding written | the axis that changed       | the engine that survived | the first profile | the second profile | the posture that survived |
        |               | developer           | container-rootless          | claude-code              | dev               | ops                | plan                      |
        | --format json | developer           | container-rootless          | claude-code              | dev               | ops                | plan                      |
        | --format text | Updated agent       | runtime: container-rootless | llm: claude-code         | profiles: dev, ops | ops               | permissions: plan         |

    # --surface is the delivery-preference axis (kind=approach, repeatable):
    # which mechanism a surface reaches the engine through, distinct from
    # llm/profiles/runtime/permissions above. It carries no listing render
    # (agent list never prints a "surfaces:" line), so the only honest read
    # of the effect is the binding it was written to.
    Scenario Outline: Setting a surface delivery preference records it in the binding
      Given an initialized ctxloom project
      And a profile "dev" exists
      And I run "ctxloom agent create developer --llm claude-code --profiles dev"
      When Alice sets how this binding's context should be delivered:
        """
        ctxloom agent edit developer --surface context=system-prompt <flags>
        """
      Then the command succeeds
      And the output reports "name" as "<the binding written>"
      And the file ".ctxloom/config.yaml" contains "context: system-prompt"

      Examples: no --format at all takes the derived default off a terminal; an explicit one wins in both directions
        | flags         | the binding written |
        |               | developer           |
        | --format json | developer           |
        | --format text | Updated agent       |

  Rule: --config-home decides WHOSE engine config home this binding's runs get

    The other axes above pick what runs. `--config-home` picks whose ~/.codex,
    ~/.claude or ~/.kiro it runs against — the directory the engine reads its
    hooks, MCP registrations, prompts and skills from, and writes its session
    state back into. `host` (and leaving it unsaid) keeps the human's own;
    `project` gives this binding's runs a disposable PER-SESSION home under
    `.ctxloom/state/<harp>/home` instead, so a ctxloom agent is not handed the
    human's memory, plugins and personal registrations by default.

    # THE EFFECT, MEASURED WHERE IT LANDS. Every other scenario in this file
    # can stop at the binding, because the binding is all their axis is: no
    # engine is launched, so there is nothing further to look at. This one
    # cannot, because a `--config-home` that persisted perfectly and changed no
    # engine's environment is precisely the silent no-op the flag exists to
    # prevent — and `agent list`/`agent show` would report it as a success.
    #
    # So this borrows J002200's PATH-sandboxed recording spy (see
    # j002200_isolation.feature) rather than a real engine binary, and reads
    # CODEX_HOME back out of the spawned process's OWN environment. The
    # difference from the sibling scenario there is the only thing under test
    # here: that fixture writes `config_home: project` into config.yaml itself,
    # while this one renders NO config_home at all and makes `ctxloom agent
    # edit --config-home` the sole writer. Both halves are asserted, because
    # they fail apart — a flag dropped before the binding leaves the file
    # without the key, and a consumer that ignores the key leaves the engine on
    # Alice's own home.
    Scenario: --config-home project moves the engine off Alice's own home onto a per-session one
      Given Alice has a git-backed project
      And Alice has whatever host credentials "codex" needs to authenticate
      And Alice declares config_home "project" on her agent with the ctxloom CLI
      When Alice runs the isolated "codex" agent under workspace "none"
      Then the file ".ctxloom/config.yaml" contains "config_home: project"
      And the spy "codex" process's "CODEX_HOME" env var points at this session's config-home instance
      And Alice's own "codex" home directory was never created by the run

  Rule: The default agent is what a bare run binds

    `ctxloom run` with no --agent and no -p/-f/-t binds the DEFAULT AGENT: its
    profiles become the context and its engine, runtime and posture the
    transport. This replaced the retired `profile default` — the default
    context is now whatever the default agent composes, so there is no "unset",
    only a different name.

    # NOT TABLED BY FORMAT, and the reason is a tracked debt rather than a
    # choice: `agent default` never routes its result through emit(), so it is
    # carried in internal/cli's formatDebtAllowlist. It renders prose whatever
    # format resolves, and asking it for a structured one FAILS the command
    # outright — so the derived default off a terminal cannot be used here
    # either. `--format text` is stated explicitly to name the only encoding
    # this command can currently answer in. When the allowlist entry is paid
    # down, this becomes an Outline like its neighbours.
    Scenario: With nothing bound, the report says so; naming an agent binds it
      Given an initialized ctxloom project
      When I run "ctxloom agent default --format text"
      Then the command succeeds
      And the output contains "No default agent set."
      Given a profile "dev" exists
      And I run "ctxloom agent create developer --profiles dev"
      When Alice makes one binding the one a bare run picks up:
        """
        ctxloom agent default developer --format text
        """
      Then the command succeeds
      And the file ".ctxloom/config.yaml" contains "default_agent: developer"
      When I run "ctxloom agent default --format text"
      Then the command succeeds
      And the output contains "Default agent: developer"

    # Fault tolerance, deliberately: a name that is not defined YET is bound
    # with a warning rather than refused, because the ordinary order of work is
    # to name the default and then define it. The warning is what stops that
    # from being a silent misconfiguration.
    # Same format debt as the scenario above: `agent default` is on
    # formatDebtAllowlist, so `--format text` is named rather than derived.
    Scenario: Naming an agent that does not exist yet warns, and still binds it
      Given an initialized ctxloom project
      When I run "ctxloom agent default ghost --format text"
      Then the command succeeds
      And the output contains "not defined yet"
      And the file ".ctxloom/config.yaml" contains "default_agent: ghost"

  Rule: Removing reports before it destroys

    A bare `remove` is a PREVIEW: it prints what would go and removes nothing,
    naming the exact invocation that would. A plan and an outcome otherwise
    render identically, and the difference is the whole point.

    # `applied` is the payload's own answer to "did anything happen", not a
    # word the renderer chose — so a preview that quietly destroyed would have
    # to lie in a field rather than merely print the wrong sentence.
    Scenario Outline: Bare remove reports and destroys nothing
      Given an initialized ctxloom project
      And a profile "dev" exists
      And I run "ctxloom agent create developer --profiles dev"
      When I run "ctxloom agent remove developer <flags>"
      Then the command succeeds
      And the output reports "applied" as "<nothing was applied>"
      And the output reports "apply" as "<the invocation that would>"
      # The follow-up listing is what actually catches a guard that reported
      # and destroyed anyway; the exit code cannot see it.
      When I run "ctxloom agent list <flags>"
      Then the output reports "[name=developer].name" as "<the binding that survived>"

      Examples: no --format at all takes the derived default off a terminal; an explicit one wins in both directions
        | flags         | nothing was applied | the invocation that would          | the binding that survived |
        |               | false               | ctxloom agent remove developer --yes | developer               |
        | --format json | false               | ctxloom agent remove developer --yes | developer               |
        | --format text | Nothing was removed | ctxloom agent remove developer --yes | developer               |

    # `status` is the one verb in this noun that IS a payload field rather than
    # a rendered word, so the removal can be asserted structurally where the
    # create/edit confirmations could not be.
    Scenario Outline: --yes takes the binding out of the config it was written to
      Given an initialized ctxloom project
      And a profile "dev" exists
      And I run "ctxloom agent create developer --profiles dev"
      And the file ".ctxloom/config.yaml" contains "developer"
      When Alice retires a binding for good:
        """
        ctxloom agent remove developer --yes <flags>
        """
      Then the command succeeds
      And the output reports "status" as "<the binding is gone>"
      And the file ".ctxloom/config.yaml" does not contain "developer"
      When I run "ctxloom agent list <flags>"
      Then the output reports "$" as empty, saying "No agents defined"

      Examples: no --format at all takes the derived default off a terminal; an explicit one wins in both directions
        | flags         | the binding is gone |
        |               | removed             |
        | --format json | removed             |
        | --format text | Removed agent       |

    Scenario: Removing a binding nobody defined fails rather than reporting success
      Given an initialized ctxloom project
      When I run "ctxloom agent remove nosuchagent"
      Then the command fails
      And the output contains "nosuchagent"
