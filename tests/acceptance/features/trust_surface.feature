@doc
Feature: The trust surface — what "review" actually controls

  This is a REFERENCE, not a journey: there is no persona and no story arc.
  It answers one question exhaustively, in a form a security reviewer can
  check in a single table: for every kind of thing a bundle can ship, can it
  be approved, can it be denied, and does that denial actually hold — in the
  payload the assistant receives?

  A bundle ships five kinds of thing (internal/bundles/bundles.go:38-59):
  fragments, commands, mcp servers, hooks, and profiles. They are not equally
  dangerous. A hook is a shell command the harness runs on a matching tool
  call, with no model in the loop — straight RCE. An MCP server is a binary
  launched and handed a tool surface the agent can invoke. A fragment or
  command is prose injected into an agent that already holds the shell, the
  filesystem, the network, and the credentials — "just text" is the
  industry's mistake, not ours.

  Before this table existed, reject-coverage was scattered and lopsided in
  exactly the highest-stakes places: the hook had been rejected in one test
  (claude only), the fragment had been rejected in one test, and the MCP
  server, the command, and the profile had NEVER been denied in any test, on
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
    Given a bundle from an unsigned, never-reviewed publisher ships one of each: a fragment, a command, an MCP server, and a hook
    When Alice approves the <element>
    And Alice starts a session
    Then the <element> is present in her assistant's delivered surface

    Examples:
      | element    |
      | hook       |
      | MCP server |
      | command    |
      | fragment   |

  Scenario Outline: Rejecting the shipped item withholds it, even though a trusted publisher signed it
    Given a trusted publisher's signed bundle ships one of each: a fragment, a command, an MCP server, and a hook
    When Alice rejects the <element>
    And Alice starts a session
    Then the <element> is absent from her assistant's delivered surface

    Examples:
      | element    |
      | hook       |
      | MCP server |
      | command    |
      | fragment   |

  # PROFILES: not a fifth row on either table above — there is no decision to
  # make one, on either side.
  Scenario: A profile cannot be approved or denied — there is no gate to run it through
    Given a bundle from an unsigned, never-reviewed publisher ships one of each: a fragment, a command, an MCP server, and a hook
    When Alice tries to approve the bundle's profile
    Then ctxloom refuses, because profiles are not a trust-addressable kind

  # GAP A — the sharpest untested claim on this whole page: "a rejection is of
  # BYTES, not provenance" (internal/operations/trust.go's ReviewRecords.Rejected
  # doc). Every scenario above rejects at the REF (this exact bundle, this exact
  # name); none of them prove the CONTENT-level block a renamed or moved copy
  # still has to clear. Proven the same way the REJECT outline above proves
  # anything: start from a trusted-signer bundle (allowed by default), reject
  # the fragment, then have the publisher legitimately re-publish it under a
  # new name (re-signed, so a broken content check would let it right back in
  # via step 5) — the marker must still never reach the assistant.
  Scenario: A rejection binds bytes, not identity — it survives a rename or move
    Given a trusted publisher's signed bundle ships one of each: a fragment, a command, an MCP server, and a hook
    When Alice rejects the fragment
    And the publisher renames the fragment to a new name, keeping its bytes identical, and re-signs it
    And Alice starts a session
    Then the fragment is absent from her assistant's delivered surface

  # GAP B — "an approval only allows when it covers THIS FORM" (EffectiveTrust
  # step 6's own comment) was never checked: nothing in this suite ships a
  # distilled form at all, so SetItemTrust's second (distilled) countersignature
  # write, and computeItemPayload's form selection, ran on every approve call
  # but nothing ever depended on the RESULT differing by form. Two scenarios:
  # first, that approving a fragment shipped with both forms covers BOTH — not
  # just whichever one got checked first — by flipping which form materialize
  # prefers and watching the SERVED bytes flip with it. Second, and sharper: an
  # approval must never silently expand to cover bytes it never saw. A fragment
  # approved while it had only a raw form, and then given a distilled form
  # afterward, must NOT start serving that new, unapproved form just because
  # config now prefers distilled by default — it must re-gate to pending.
  Scenario Outline: Approving a fragment shipped with both a raw and a distilled form covers both, not just the one form checked first
    Given a bundle from an unsigned, never-reviewed publisher ships a fragment with both a raw and a distilled form
    When Alice approves the fragment
    And Alice starts a session preferring <form> content
    Then the fragment's <form> marker is present in her assistant's delivered surface

    Examples:
      | form      |
      | raw       |
      | distilled |

  Scenario: Approving a fragment while it has only a raw form does not silently cover a distilled form added later
    Given a bundle from an unsigned, never-reviewed publisher ships one of each: a fragment, a command, an MCP server, and a hook
    When Alice approves the fragment
    And Alice starts a session
    Then the fragment is present in her assistant's delivered surface
    When the publisher adds a distilled form to the fragment, keeping its raw bytes unchanged
    And Alice starts a session
    Then the fragment is withheld entirely, in neither its raw nor its new distilled form
    And the fragment's review state is "pending"

  # GAP E — the review STATE LABEL a human actually sees (`ctxloom review`,
  # `fragment list --format json`'s "state" field) is a SEPARATE claim from "the
  # payload is withheld", and nothing above checks it: EffectiveTrustResult.
  # State() renders BOTH SourceRejected and SourceRetracted as "rejected" through
  # one shared case arm, and either half of that arm could be deleted with every
  # payload assertion above still green — a rejected or retracted item could
  # render as "pending" (awaiting review) and mislead a reviewer into thinking
  # nothing has been decided about it yet. Withholding still happens either
  # way — this is a display gap, not a gate bypass — but a wrong label is its
  # own kind of failure for a tool whose whole job is telling a human what was
  # decided and why.
  Scenario: A rejected item's review state is labeled "rejected," not silently "pending"
    Given a trusted publisher's signed bundle ships one of each: a fragment, a command, an MCP server, and a hook
    When Alice rejects the fragment
    Then the fragment's review state is "rejected"

  Scenario: A retracted bundle's items are labeled "rejected," not silently "pending"
    Given a trusted publisher's signed bundle ships one of each: a fragment, a command, an MCP server, and a hook
    When the publisher retracts the bundle
    Then the fragment's review state is "rejected"

  Scenario: An approved item's review state is labeled "accepted," not left at "pending"
    Given a bundle from an unsigned, never-reviewed publisher ships one of each: a fragment, a command, an MCP server, and a hook
    When Alice approves the fragment
    Then the fragment's review state is "accepted"

  # GAP C — the DECISION THAT WAS RECORDED, not the payload that was served.
  # Every assertion above reads the downstream materialized surface. None of them
  # ever look at what `ctxloom trust`/`ctxloom blacklist` actually wrote, so the
  # write path could record a block for a form the item does not even have — or
  # silently fail to record one it does — and the whole table would stay green.
  # A rejection's content component is written PER FORM the item currently has
  # (spec §5.3), so these two read that decision back out of the tool's own
  # report of it: a raw-only item must be blocked in exactly its raw form, and an
  # item that ships both forms must be blocked in both. A phantom block for a
  # form that does not exist is not harmless bookkeeping — it is the tool
  # claiming to have protected something it never looked at.
  Scenario: Rejecting a raw-only item records a content block for exactly that form
    Given a bundle from an unsigned, never-reviewed publisher ships one of each: a fragment, a command, an MCP server, and a hook
    When Alice rejects the fragment, and ctxloom reports what it recorded
    Then the recorded rejection covers exactly the raw form

  Scenario: Rejecting an item that ships both forms records a content block for both
    Given a bundle from an unsigned, never-reviewed publisher ships a fragment with both a raw and a distilled form
    When Alice rejects the fragment, and ctxloom reports what it recorded
    Then the recorded rejection covers exactly the raw and distilled forms

  # GAP F — FAIL CLOSED ON AN UNRECOGNIZED SOURCE. A ref that carries a scheme
  # marker (so it was plainly INTENDED as a canonical reference) but does not
  # parse as one must be REFUSED — never quietly downgraded to "a local bundle
  # name." Locality is not a label: local content is auto-allowed at step 3 of
  # the cascade, ahead of any review, so treating an unparseable remote ref as
  # local is a gate bypass, not a cosmetic mislabel. Nothing on this page
  # exercised that guard.
  Scenario: A source reference that cannot be parsed is refused, never treated as local
    Given a bundle from an unsigned, never-reviewed publisher ships one of each: a fragment, a command, an MCP server, and a hook
    When Alice tries to review an item whose source reference is malformed
    Then ctxloom refuses, rather than treating an unrecognized source as local

  # GAP D — WAS a confirmed vulnerability (taskloom rocky-motto), NOW FIXED.
  # internal/operations/trust.go's "approvals store unreadable -> deny
  # EVERYTHING" guard used to only run when EffectiveTrustRequest.Records was
  # nil — and NO real caller ever left it nil. TrustStamper, the
  # content/executable gates (trust_gate.go), and PendingReview (`ctxloom
  # review`) each build their OWN non-nil ReviewRecords before calling
  # EffectiveTrust, so the guard was unreachable dead code from every real CLI
  # path: a rejected local fragment REAPPEARED in materialized output, exit 0,
  # no warning, the moment its user approvals store was replaced with a plain
  # file. Fixed by checking readability via an optional capability
  # (readableRecords) unconditionally — whichever way records was obtained,
  # not only the records-built-fresh branch — so the fail-closed gate now
  # covers every production call site (internal/operations/trust_test.go and
  # trust_approvals_readable_test.go carry the unit-level proof, including
  # the boundary that a fresh/never-created store must NOT trip it).
  # This scenario proves the fix end to end: the previously-rejected fragment
  # stays withheld, and everything else goes dark too (deny-all, not merely
  # "this one item"), with Alice told plainly that her approvals store is the
  # problem.
  Scenario: A corrupted approvals store denies everything, rather than silently un-rejecting previously withheld content
    Given a trusted publisher's signed bundle ships one of each: a fragment, a command, an MCP server, and a hook
    When Alice rejects the fragment
    And Alice starts a session
    Then the fragment is absent from her assistant's delivered surface
    When her approvals store is corrupted, a file where a directory should be
    And Alice starts a session
    Then her session refuses to start, telling her the approvals store is corrupted
    And the previously-rejected fragment has still not reappeared in her assistant's context
