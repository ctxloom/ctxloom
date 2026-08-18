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
  .ctxloom/config.yaml (or as .ctxloom/agents/<name>.yaml files) and are never
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

    Scenario: A project with no bindings says so, and says where to define one
      Given an initialized ctxloom project
      When Alice asks what agents this project defines:
        """
        ctxloom agent list
        """
      Then the command succeeds
      And the output contains "No agents defined"
      And the output contains ".ctxloom/agents/"

    Scenario: Creating a binding records every axis, and the listing reads each one back
      Given an initialized ctxloom project
      And a profile "dev" exists
      When Alice binds an engine and a runtime to a profile:
        """
        ctxloom agent create developer --llm claude-code --profiles dev --runtime container-rootless
        """
      Then the command succeeds
      And the output contains "Created agent"
      # The EFFECT, not the echo: the confirmation above is printed from the
      # request, so it is true of a writer that saved nothing.
      And the file ".ctxloom/config.yaml" contains "developer"
      When I run "ctxloom agent list"
      Then the command succeeds
      And the output contains "developer"
      And the output contains "llm: claude-code"
      And the output contains "profiles: dev"
      And the output contains "runtime: container-rootless"

    # The bare noun answers the question somebody typing it has, rather than
    # teaching them what they could have typed instead.
    Scenario: Bare agent lists the bindings
      Given an initialized ctxloom project
      And a profile "dev" exists
      And I run "ctxloom agent create developer --profiles dev"
      When I run "ctxloom agent"
      Then the command succeeds
      And the output contains "developer"
      And the output does not contain "Available Commands:"

    # `show` answers two questions a listing cannot: what the definition
    # DECLARES, and what that declaration actually RESOLVES to. Both halves are
    # asserted, because a binding whose engine resolves to nothing still lists
    # perfectly.
    Scenario: Showing one binding reports what was declared and what it resolves to
      Given an initialized ctxloom project
      And a profile "dev" exists
      And I run "ctxloom agent create developer --llm claude-code --profiles dev --runtime container-rootless"
      When Alice inspects one binding:
        """
        ctxloom agent show developer
        """
      Then the command succeeds
      And the output contains "Agent: developer"
      And the output contains "Engine (declared): claude-code"
      And the output contains "Runtime: container-rootless"
      And the output contains "Profiles"
      And the output contains "dev"
      And the output contains "Resolved llm:"
      And the output contains "Composed fragments:"

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
    Scenario: Create refuses an existing name, and leaves the live binding untouched
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
      When I run "ctxloom agent show developer"
      Then the command succeeds
      And the output contains "dev"
      And the output does not contain "ops"

    # The other half: a typo'd `edit` must not quietly mint a brand-new agent.
    # The listing afterward is what proves nothing was created — the refusal
    # message alone is true of a command that refused AND wrote.
    Scenario: Edit refuses a name no agent has, and creates nothing
      Given an initialized ctxloom project
      And a profile "dev" exists
      When Alice edits a name she has not defined:
        """
        ctxloom agent edit nosuchagent --profiles dev
        """
      Then the command fails
      And the output contains "no agent named"
      And the output contains "ctxloom agent create nosuchagent"
      When I run "ctxloom agent list"
      Then the command succeeds
      And the output contains "No agents defined"

    # THE LOSS THIS SPLIT EXISTS TO PREVENT. Every field the invocation did not
    # name survives — asserted one axis at a time, because a merge that kept
    # the engine and wiped the profiles would satisfy any single one of them.
    Scenario: An edit naming only --runtime leaves every other axis intact
      Given an initialized ctxloom project
      And a profile "dev" exists
      And a profile "ops" exists
      And I run "ctxloom agent create developer --llm claude-code --profiles dev,ops --runtime host --permissions plan"
      When Alice moves one binding into a container and changes nothing else:
        """
        ctxloom agent edit developer --runtime container-rootless
        """
      Then the command succeeds
      And the output contains "Updated agent"
      When I run "ctxloom agent list"
      Then the command succeeds
      And the output contains "runtime: container-rootless"
      And the output contains "llm: claude-code"
      And the output contains "profiles: dev, ops"
      And the output contains "permissions: plan"

    # --surface is the delivery-preference axis (kind=approach, repeatable):
    # which mechanism a surface reaches the engine through, distinct from
    # llm/profiles/runtime/permissions above. It carries no listing render
    # (agent list never prints a "surfaces:" line), so the only honest read
    # of the effect is the binding it was written to.
    Scenario: Setting a surface delivery preference records it in the binding
      Given an initialized ctxloom project
      And a profile "dev" exists
      And I run "ctxloom agent create developer --llm claude-code --profiles dev"
      When Alice sets how this binding's context should be delivered:
        """
        ctxloom agent edit developer --surface context=system-prompt
        """
      Then the command succeeds
      And the output contains "Updated agent"
      And the file ".ctxloom/config.yaml" contains "context: system-prompt"

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

    Scenario: With nothing bound, the report says so; naming an agent binds it
      Given an initialized ctxloom project
      When I run "ctxloom agent default"
      Then the command succeeds
      And the output contains "No default agent set."
      Given a profile "dev" exists
      And I run "ctxloom agent create developer --profiles dev"
      When Alice makes one binding the one a bare run picks up:
        """
        ctxloom agent default developer
        """
      Then the command succeeds
      And the file ".ctxloom/config.yaml" contains "default_agent: developer"
      When I run "ctxloom agent default"
      Then the command succeeds
      And the output contains "Default agent: developer"

    # Fault tolerance, deliberately: a name that is not defined YET is bound
    # with a warning rather than refused, because the ordinary order of work is
    # to name the default and then define it. The warning is what stops that
    # from being a silent misconfiguration.
    Scenario: Naming an agent that does not exist yet warns, and still binds it
      Given an initialized ctxloom project
      When I run "ctxloom agent default ghost"
      Then the command succeeds
      And the output contains "not defined yet"
      And the file ".ctxloom/config.yaml" contains "default_agent: ghost"

  Rule: Removing reports before it destroys

    A bare `remove` is a PREVIEW: it prints what would go and removes nothing,
    naming the exact invocation that would. A plan and an outcome otherwise
    render identically, and the difference is the whole point.

    Scenario: Bare remove reports and destroys nothing
      Given an initialized ctxloom project
      And a profile "dev" exists
      And I run "ctxloom agent create developer --profiles dev"
      When I run "ctxloom agent remove developer"
      Then the command succeeds
      And the output contains "Nothing was removed"
      And the output contains "ctxloom agent remove developer --yes"
      # The follow-up listing is what actually catches a guard that reported
      # and destroyed anyway; the exit code cannot see it.
      When I run "ctxloom agent list"
      Then the output contains "developer"

    Scenario: --yes takes the binding out of the config it was written to
      Given an initialized ctxloom project
      And a profile "dev" exists
      And I run "ctxloom agent create developer --profiles dev"
      And the file ".ctxloom/config.yaml" contains "developer"
      When Alice retires a binding for good:
        """
        ctxloom agent remove developer --yes
        """
      Then the command succeeds
      And the output contains "Removed agent"
      And the file ".ctxloom/config.yaml" does not contain "developer"
      When I run "ctxloom agent list"
      Then the output contains "No agents defined"

    Scenario: Removing a binding nobody defined fails rather than reporting success
      Given an initialized ctxloom project
      When I run "ctxloom agent remove nosuchagent"
      Then the command fails
      And the output contains "nosuchagent"
