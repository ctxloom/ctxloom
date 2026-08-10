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
  changes nothing observable. So denial is proven the way J001500 already proved
  it for hooks and MCP servers: start from a bundle a TRUSTED publisher
  signed (which the content/executable gate allows by default, the same as
  J001500's Background), then reject one item and watch it — and ONLY it — go
  dark, even though the signature is still good. Approval is proven the
  opposite way: start from an UNSIGNED, never-reviewed bundle (denied by
  default), then approve one item and watch it — and ONLY it — start
  reaching the assistant.

  PROFILES ARE A DIFFERENT CASE, not a fifth row of the same table: a bundle
  profile is never trust-gated at all (no trust.ItemKind for it — see
  internal/trust/trust.go's ItemKind: fragment | prompt | mcp | hook, and
  internal/bundles/bundles.go:46-51's comment). "ctxloom bundle trust"/"ctxloom
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

  # THE TEXT→EXEC ESCALATION. An approval attests bytes IN A ROLE, and the role
  # is not recoverable from the bytes: a fragment's payload is its bare body,
  # while an MCP server's is deterministic JSON, so a publisher can ship a
  # FRAGMENT whose body IS the MCP server's executable preimage — byte equality,
  # no collision search needed. The reviewer is shown that fragment as TEXT
  # (fragments render as content; executables render as "what they run"), and if
  # the two shared one approval key the executable would reach the assistant
  # having NEVER been displayed as an executable, because the dangerous rendering
  # is exactly the step skipped for an already-approved item. What the countersign
  # payload binds is therefore a COMPOSITE form naming the role
  # ("fragment/raw" vs "exec/mcp"), so the two can never share a key.
  #
  # The last four lines are what make that claim testable at all (audit
  # irate-catfish, F4). The delivered-surface half alone could not fail:
  # an approval's key is ref PLUS form, the two items are #fragments/context
  # and #mcp/toolserver, and Ref.Key bakes the KIND into the ref — so the ref
  # component already separates them and the composite form never did any
  # work. Replacing exec/mcp with fragment/raw outright left 30 of 30
  # scenarios green. Worse, the MCP was withheld by default anyway (unsigned,
  # never reviewed), so its absence proved an absence.
  #
  # So the roles are read out of the countersignature store itself, and the
  # executable is then approved IN ITS OWN ROLE and shown to start flowing —
  # which is what establishes that nothing but the missing exec/mcp approval
  # was keeping it out of the session above.
  Scenario: Approving a text fragment never approves the executable whose bytes it copies
    Given a bundle from an unsigned, never-reviewed publisher ships a fragment whose body is byte-identical to its MCP server's executable preimage
    When Alice approves the fragment
    And Alice starts a session
    Then the copied preimage is present in her assistant's delivered surface as text
    And the MCP server is absent from her assistant's delivered surface
    And what Alice approved is on record as the text's role only, never as the executable's
    When Alice approves the MCP server
    And Alice starts a session
    Then the MCP server is present in her assistant's delivered surface
    And the two decisions are on record as separate attestations over the same bytes, one per role

  # STALING. The composite form is a change to what gets signed, so it bumps the
  # countersign contract and every approval recorded before it stops verifying.
  # That is accepted and announced — but it must land as STALE, not ABSENT: an
  # item whose earlier approval no longer covers it has to come back labelled an
  # UPDATE, because "new" would tell the reviewer nobody ever looked at this,
  # hiding that these bytes might be a substitution for something they approved.
  Scenario: An approval recorded under a superseded contract reads as an update, not as a new item
    Given a bundle from an unsigned, never-reviewed publisher ships one of each: a fragment, a command, an MCP server, and a hook
    When Alice approves the fragment
    And her approval was recorded under a superseded countersign contract
    And Alice starts a session
    Then the fragment is absent from her assistant's delivered surface
    And review lists the fragment as an update awaiting re-review, not as a new item

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
  # ever look at what `ctxloom bundle trust`/`bundle reject` actually wrote, so the
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

  # GAP D2 — the RETRACTION half of the same posture (U085-F02). The write
  # side of a corrupt lock.yaml was already refused (it is the only on-disk
  # record of publisher retractions, so overwriting it un-retracts silently).
  # The READ side still failed OPEN: EffectiveTrust's retraction lookup
  # degraded an unparseable lock.yaml to "nothing is retracted", so a bundle
  # the publisher deliberately WITHDREW was served to the assistant again,
  # exit 0. "I cannot read the retraction record" is not "nothing is
  # retracted"; collapsing the two inverts the one control that exists for
  # "this content turned out to be harmful". Now it withholds, records a
  # fatal trust finding, and names the recovery. The BOUNDARY that must not
  # trip — a project with no lock.yaml at all has nothing retracted and keeps
  # working — is pinned in internal/operations/trust_retraction_readable_test.go.
  # ⚠ THIS SCENARIO IS GREEN FOR THE WRONG REASON — taskloom alive-rover.
  # RE-MEASURED 2026-08-07 by instrumenting operations.EffectiveTrust's
  # retraction-readable arm and running this scenario alone: with the lockfile
  # corrupted, that arm is reached by SEVEN refs, all of them companion items
  # (ltk fragment/prompt/hook, taskloom fragment/mcp/hooks), and by the remote
  # `trustdemo` bundle this scenario is named after ZERO times. Mechanism:
  # remote.BundleReader serves a remote bundle only at the SHA its lockfile
  # entry records, so an unparseable lock.yaml yields ErrBundleNotInLockfile
  # and the bundle is never loaded, never trust-evaluated, and produces no
  # finding. The abort asserted below is raised entirely by companion content.
  # The outcome therefore depends on whether the machine has ltk/taskloom on
  # PATH, not on the behaviour under test: delete the retraction-readability
  # check for remote content and this row stays green wherever companions are
  # installed. No fixture can fix that — there is no way to keep a remote
  # bundle loadable through a lockfile that does not parse — so
  # tsRefuseIfOnlyCompanionsCanTrip now fails LOUDLY, naming this gap, on any
  # machine where the accident that makes it pass is absent. Do not read a
  # pass here as coverage of the sentence below until the fixture makes the
  # REMOTE bundle produce the finding.
  Scenario: An unreadable lockfile withholds remote content, rather than silently un-retracting it
    Given a trusted publisher's signed bundle ships one of each: a fragment, a command, an MCP server, and a hook
    When Alice starts a session
    Then the fragment is present in her assistant's delivered surface
    When her lockfile is corrupted, an unparseable lock.yaml
    And Alice starts a session
    Then her session refuses to start, telling her the retraction state cannot be established, and naming how to recover
