@doc
Feature: The trust surface — what "review" actually controls

  This is a REFERENCE, not a journey: there is no persona and no story arc.
  It answers one question exhaustively, in a form a security reviewer can
  check in a single table: for every kind of thing a bundle can ship, can it
  be approved, can it be denied, and does that denial actually hold — in the
  payload the assistant receives?

  A bundle ships five kinds of thing (internal/bundles/bundles.go:38-59):
  fragments, skills, mcp servers, hooks, and profiles. They are not equally
  dangerous. A hook is a shell command the harness runs on a matching tool
  call, with no model in the loop — straight RCE. An MCP server is a binary
  launched and handed a tool surface the agent can invoke. A fragment or
  skill is prose injected into an agent that already holds the shell, the
  filesystem, the network, and the credentials — "just text" is the
  industry's mistake, not ours.

  Before this table existed, reject-coverage was scattered and lopsided in
  exactly the highest-stakes places: the hook had been rejected in one test
  (claude only), the fragment had been rejected in one test, and the MCP
  server, the skill, and the profile had NEVER been denied in any test, on
  any engine. Every existing scenario that mentions an MCP server asserts
  it APPEARS.

  ENGINE SCOPE: the executable trust gate is applied UPSTREAM of every engine
  writer (internal/config/config_bundles.go's ResolveBundleMCPServers /
  ResolveBundleHooks route through one shared c.execGate before any backend
  ever sees the result), so a per-engine bypass is not structurally possible.
  This feature proves the gate on ONE engine (claude-code) and makes no claim
  about any other — it would re-prove the identical mechanism four times.

  TWO OUTLINES, NOT ONE, and deliberately so: a denial test that starts from a
  state the item would be withheld from ANYWAY (e.g. unsigned, never
  reviewed) can never fail for the reason it claims to prove — rejecting it
  changes nothing observable. So denial is proven the way J3 already proved
  it for hooks and MCP servers: start from a bundle a TRUSTED publisher
  signed (which the content/executable gate allows by default, the same as
  J3's Background), then reject one item and watch it — and ONLY it — go
  dark, even though the signature is still good. Approval is proven the
  opposite way: start from an UNSIGNED, never-reviewed bundle (denied by
  default), then approve one item and watch it — and ONLY it — start
  reaching the assistant.

  PROFILES ARE A DIFFERENT CASE, not a fifth row of the same table: a bundle
  profile is never trust-gated at all (no trust.ItemKind for it — see
  internal/trust/trust.go's ItemKind: fragment | prompt | mcp | hook, and
  internal/bundles/bundles.go:46-51's comment). "ctxloom trust"/"ctxloom
  blacklist" cannot even parse a "#profiles/<name>" selector. The final
  scenario below proves that refusal directly, rather than asserting a
  decision that does not exist.

  # Ordered by execution tier — hooks first, because Tier 1 (RCE, no model in
  # the loop) is the loudest claim this table can make or break.
  Scenario Outline: Approving the shipped item is what makes it reach the assistant
    Given a bundle from an unsigned, never-reviewed publisher ships one of each: a fragment, a skill, an MCP server, and a hook
    When Alice approves the <element>
    And Alice starts a session
    Then the <element> is present in her assistant's delivered surface

    Examples:
      | element    |
      | hook       |
      | MCP server |
      | skill      |
      | fragment   |

  Scenario Outline: Rejecting the shipped item withholds it, even though a trusted publisher signed it
    Given a trusted publisher's signed bundle ships one of each: a fragment, a skill, an MCP server, and a hook
    When Alice rejects the <element>
    And Alice starts a session
    Then the <element> is absent from her assistant's delivered surface

    Examples:
      | element    |
      | hook       |
      | MCP server |
      | skill      |
      | fragment   |

  # PROFILES: not a fifth row on either table above — there is no decision to
  # make one, on either side.
  Scenario: A profile cannot be approved or denied — there is no gate to run it through
    Given a bundle from an unsigned, never-reviewed publisher ships one of each: a fragment, a skill, an MCP server, and a hook
    When Alice tries to approve the bundle's profile
    Then ctxloom refuses, because profiles are not a trust-addressable kind
