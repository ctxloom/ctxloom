Feature: Agent bindings
  Agents are LOCAL engine↔profile bindings (config `agents:`) with an optional
  runtime axis; `acp list` advertises one ACP server entry per binding.

  Two surfaces this file used to carry now have per-noun specs of their own.
  The `container tooling` / `container scaffold` / `container check` scenarios
  moved to cli/container.feature. The `init prompt` scenario moved to
  cli/init.feature, where it asserts the interview's phase headings rather than
  a single token.

  Scenario: Agent create, list, show, edit, and remove lifecycle
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
    # Bare `remove` is a preview: it must leave the agent bound. A guard that
    # quietly destroyed anyway would still pass a scenario that only checked
    # exit code — the follow-up `agent list` is what actually catches that.
    When I run "ctxloom agent remove developer"
    Then the command succeeds
    And the output contains "Nothing was removed"
    And the output contains "--yes"
    When I run "ctxloom agent list"
    Then the output contains "developer"
    When I run "ctxloom agent remove developer --yes"
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
  # j002000_engine_switch.feature relies on for its --engine validation), so the
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

  # The `container tooling`, `container scaffold` and `container check`
  # scenarios that used to live here MOVED to cli/container.feature, the
  # per-noun spec for that surface. Nothing was dropped: the withheld/trusted
  # tooling pair, the scaffold, and the diagnostic-only project-tree assertion
  # are all there, alongside the adoption/--force pair and the path-escape
  # refusal this file never covered.

  # RESTORED 2026-08-08 after being deleted in an uncommitted working-tree
  # change. Worth stating why, because the deletion was invisible to every
  # gate: `ctxloom acp list` stays credited as a covered leaf by
  # j000500_editor.feature, so completeness_test.go went right on passing — it
  # compares LEAVES, and cannot see that a leaf's only assertion about its
  # PAYLOAD has gone.
  #
  # What lives only here: the PER-BINDING entry. j000500 proves the block is
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

