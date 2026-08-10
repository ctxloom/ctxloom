Feature: Per-binding ACP entries
  What is left of the old catch-all `agent` feature once its subjects moved to
  per-noun specs of their own. The agent binding lifecycle — list, show,
  create, edit, default, remove — is cli/agent.feature. `container tooling`,
  `container scaffold` and `container check` are cli/container.feature.
  `init prompt` is cli/init.feature.

  What lives only here is the PER-BINDING entry: that a configured agent gets
  its own ACP server line carrying `--agent <name>`, which nothing else
  asserts.

  The agent binding lifecycle itself — list/show/create/edit/default/remove,
  the non-upsert guards and the per-field merge — lives in cli/agent.feature
  and is deliberately NOT restated here.

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


  # MOVED here from features/profile.feature, which was retired when
  # cli/profile.feature took over that noun. The scenario was never about
  # profiles: `profile default` was RETIRED, and the default context is now
  # whatever the always-bound default AGENT composes. It is the only place
  # `ctxloom agent default` is driven at all, in either its reading or its
  # setting form, which is why it moved rather than being deleted with the
  # file it was living in.
  #
  # There is no "unset": the replacement binds a NAME, not a settable list.
  Scenario: The default agent is read before it is set, and reads back after
    Given an initialized ctxloom project
    When I run "ctxloom agent default"
    Then the command succeeds
    And the output contains "No default agent set."
    Given a bundle "demo" exists
    And a profile "dev" with bundle "demo"
    And I run "ctxloom agent create developer --profiles dev"
    When I run "ctxloom agent default developer"
    Then the command succeeds
    When I run "ctxloom agent default"
    Then the command succeeds
    And the output contains "developer"
