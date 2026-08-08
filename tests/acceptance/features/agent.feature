Feature: Agent bindings and container tooling
  Agents are LOCAL engine↔profile bindings (config `agents:`) with an optional
  runtime axis; `init prompt` emits the interview prompt; `container tooling`
  collects trusted bundles' agent-image tool declarations; `container scaffold`
  makes the base Containerfile editable; `container check` is a read-only
  capability diagnosis; `acp list` advertises one ACP server entry per binding.

  Scenario: Agent create, list, show, edit, and delete lifecycle
    Given an initialized ctxloom project
    And a profile "dev" exists
    When I run "ctxloom agent create developer --profiles dev --runtime container"
    Then the command succeeds
    When I run "ctxloom agent list"
    Then the output contains "developer"
    And the output contains "runtime: container"
    When I run "ctxloom agent show developer"
    Then the output contains "Runtime: container"
    When I run "ctxloom agent edit developer --runtime host"
    Then the command succeeds
    When I run "ctxloom agent list"
    Then the output contains "runtime: host"
    # The profiles survived an edit that named only --runtime: `agent edit`
    # merges per field. The upsert it replaced used to wipe every unnamed one.
    And the output contains "profiles: dev"
    When I run "ctxloom agent delete developer"
    Then the command succeeds
    When I run "ctxloom agent list"
    Then the output contains "No agents defined"

  # create and edit are NOT an upsert: each refuses the case the other owns.
  # The upsert `agent set` they replaced silently minted a new agent on a
  # typo'd name and silently overwrote a live one on a reused name.
  #
  # A request to make these an upsert again was REJECTED on 2026-08-08
  # (taskloom vivacious-overlook). Recorded here because the request will
  # recur — two verbs is real friction, and the reason to keep them is not
  # obvious from the surface:
  #
  # create and edit already share ONE body (operations.SetAgent, which
  # j20_engine_switch.feature relies on for its --engine validation), so the
  # refusals are the only thing that distinguishes the verbs. Making them
  # upsert is DELETING TWO GUARDS from a shared function, not merging two
  # implementations. And a binding carries engine, profiles, runtime and
  # permission mode, so a silent overwrite loses whatever the invocation did
  # not name — the exact loss the lifecycle scenario above pins with its
  # "profiles survived an edit that named only --runtime" assertion.
  #
  # If the friction needs answering, an explicit `--force` on create (or a
  # separate upsert verb) gives idempotence without removing the guards.
  Scenario: Create refuses an existing name and edit refuses an absent one
    Given an initialized ctxloom project
    And a profile "dev" exists
    When I run "ctxloom agent create developer --profiles dev"
    Then the command succeeds
    When I run "ctxloom agent create developer --profiles dev"
    Then the command fails
    And the output contains "already exists"
    When I run "ctxloom agent edit nosuchagent --profiles dev"
    Then the command fails
    And the output contains "no agent named"

  Scenario: Init prompt emits the interview prompt
    Given an initialized ctxloom project
    When I run "ctxloom init prompt"
    Then the command succeeds
    And the output contains "SCAN"

  # Absence satisfied absence: with nothing declared anywhere, "none reported"
  # was equally consistent with the trust gate working and with collection
  # being broken outright — a render that dropped EVERY declaration, trusted or
  # not, left this green. So the fixture declares tooling twice: once from a
  # bundle whose declaration has been rejected (must be withheld, and the
  # summary line is then a fact about the GATE), and once from a bundle that is
  # trusted (must come through — the positive control that makes "none
  # reported" mean something).
  #
  # DECIDED 2026-08-08 (taskloom vivacious-overlook), NOT YET IMPLEMENTED:
  # `container tooling` will gain a section naming what was withheld BY BUNDLE
  # AND ITEM REF ("shady#commands/tooling") — never the publisher-authored
  # declaration body. A ref is a ctxloom-controlled identifier; the body is
  # attacker-controlled text, and rendering it to an operator's terminal is a
  # confirmed hazard (taskloom delicious-goatskin: publisher content reaches
  # the terminal with no sanitiser, measured).
  #
  # So the "does not contain TOOLING-DECL-SHADY" assertion below becomes MORE
  # load-bearing when that lands, not less: it is what pins that adding the ref
  # section did not start leaking the body.
  Scenario: Container tooling withholds an untrusted declaration and reports none
    Given an initialized ctxloom project
    And a bundle "shady" declaring container tooling "TOOLING-DECL-SHADY"
    And I run "ctxloom trust reject shady#commands/tooling"
    When I run "ctxloom container tooling"
    Then the command succeeds
    And the output contains "No trusted bundles declare container tooling"
    And the output contains "Untrusted declarations are withheld"
    And the output does not contain "TOOLING-DECL-SHADY"
    Given a bundle "tooled" declaring container tooling "TOOLING-DECL-TOOLED"
    When I run "ctxloom container tooling"
    Then the command succeeds
    And the output contains "TOOLING-DECL-TOOLED"
    And the output does not contain "TOOLING-DECL-SHADY"
    And the output does not contain "No trusted bundles declare container tooling"

  Scenario: Scaffold materializes the editable base Containerfile
    Given an initialized ctxloom project
    When I run "ctxloom container scaffold"
    Then the command succeeds
    And the file ".ctxloom/base.Containerfile" contains "FROM"
    And the file ".ctxloom/config.yaml" contains "isolation_base_containerfile"

  # DIAGNOSTIC-ONLY is a claim about what the command does NOT do, and no
  # assertion on its own stdout can see it writing a file somewhere else: the
  # static "Container capability" header was printed either way. The read-only
  # half is asserted where it actually lives — the project tree, byte for byte.
  Scenario: Container capability check is diagnostic-only
    Given an initialized ctxloom project
    And I record the project tree
    When I run "ctxloom container check claude-code"
    Then the command succeeds
    And the output contains "Container capability (backend: claude-code)"
    And the output contains "in a container:"
    And the output contains "shared fs:"
    And the project tree is unchanged

  # RESTORED 2026-08-08 after being deleted in an uncommitted working-tree
  # change. Worth stating why, because the deletion was invisible to every
  # gate: `ctxloom acp list` stays credited as a covered leaf by
  # j5_editor.feature, so completeness_test.go went right on passing — it
  # compares LEAVES, and cannot see that a leaf's only assertion about its
  # PAYLOAD has gone.
  #
  # What lives only here: the PER-BINDING entry. j5 proves the block is
  # pasteable JSON and names the agent; nothing anywhere else asserts that a
  # configured agent gets its own server line carrying `--agent <name>`, which
  # is the whole point of advertising one entry per binding rather than one
  # entry. Verified: zero matches for "acp serve --agent" or "agent_servers
  # paste block" in any other feature file.
  Scenario: ACP agent entries advertise per-binding servers
    Given an initialized ctxloom project
    And a profile "dev" exists
    And I run "ctxloom agent create developer --profiles dev"
    When I run "ctxloom acp list"
    Then the command succeeds
    And the output contains "developer"
    And the agent_servers paste block declares a server "ctxloom" running "acp serve"
    And the agent_servers paste block declares a server "ctxloom: developer" running "acp serve --agent developer"

