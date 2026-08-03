@doc
Feature: Publishing a bundle's whole surface, and a consumer receiving it intact

  Trent has written down how his company works, and it is not one kind of
  thing. It is guidance his engineers' assistants read (fragments), slash
  commands they invoke by hand, an MCP server that reaches the company
  ledger, a hook that stamps every file edit, a skill package with a script
  the model runs on its own, and the profile that composes all of it. That is
  one coherent unit of work. Publishing it should be one act, and receiving it
  should deliver every part of it, unchanged, to every engineer — whichever
  way their agent happens to be isolated.

  Today it is not one act, and for one of those kinds it is not possible at
  all. This journey states the whole claim anyway, because the claim is what
  the bundle-as-tree restructure is FOR, and a journey that only asserted the
  parts that already work would go green while the capability stayed missing.

  # ============================================================================
  # WHY THIS IS A NEW JOURNEY (J20) AND NOT AN EXTENSION OF J2/J3/J18
  #
  # Three existing journeys touch neighbouring ground and none of them is the
  # right home:
  #
  #   J2 (team authoring) is FIRST-PARTY and in-project — Carol commits to the
  #   team's own repo and Bob pulls the project. There is no publisher, no
  #   remote bundle reference and no signature anywhere in it; its whole point
  #   is that none of that is needed when the team owns the code. Publication
  #   is the thing this journey is about, so it cannot live there.
  #
  #   J3 (corporate signed) is about PROVENANCE AND INTEGRITY of content that
  #   already flows: its Background fixes a "secure-coding" bundle and every
  #   scenario perturbs the TRUST inputs — tamper, reject, retract, revoke.
  #   Adding "and can this kind of content be published at all" there would
  #   conflate a trust question with a capability question, and its scenarios
  #   are LOCKED and green. A red capability row inside a locked trust journey
  #   makes both harder to read.
  #
  #   J18 (signing) is the AUTHOR-side key journey — ssh-agent, allowed_signers,
  #   the project/user store split, sign/verify/move. It ends at "Alice pulls
  #   the newly published version" because delivery is not what it is proving.
  #
  # This journey's subject is the one none of them own: does the PAYLOAD of
  # every surface kind survive the trip from an author's tree to a consumer's
  # agent, across the isolation configurations that agent might be running in.
  # It is also, today, entirely red — and it needs to be independently
  # untaggable as a unit, which it could not be folded into a green journey.
  #
  # The existing suite is NOT renumbered. This is additive.
  # ============================================================================

  # ============================================================================
  # WHAT IS STILL @wip HERE, AND WHAT UNTAGS IT
  #
  # THE PRIMARY BLOCKER IS FIXED. It was: a directory-form bundle could not be
  # fetched from a remote at all — fetchAtLockedSHA resolved a ref to ONE file
  # path and called FetchFile on it, there was no tree fetch anywhere in
  # internal/remote, and a remote bundle WAS "<name>.yaml" by construction —
  # while internal/bundles/loader.go:389 refuses skills in a single-file
  # bundle. Jointly unsatisfiable, which is what taskloom task
  # `engaged-chivalry` recorded as impossible.
  #
  # `remote pull` now probes the directory form when the single file is absent,
  # walks the tree at the pinned SHA through internal/content/remotetree, and
  # installs it under the consumer's cache with the publisher's exec bit
  # intact. Every scenario asserting the PUBLICATION SIDE — the payload landing
  # in the consumer's tree, per-kind metadata placement, MCP structure, and the
  # whole skill package with its executable script — is untagged and green.
  #
  # THREE THINGS REMAIN RED, for three different reasons.
  #
  # (a) THE TREE READ PATH (the six "reaches Alice's assistant" rows). The
  #     bytes now arrive; nothing yet reads a bundle's ITEMS back out of a tree
  #     and hands them to assembly. remote.BundleReader serves single-file
  #     bundles only and now fails with remote.ErrTreeBundleUnreadable rather
  #     than inventing a missing file. internal/content/convert goes one way
  #     (document -> tree) and has no inverse.
  #     UNTAG CONDITION: a tree -> bundles.Bundle read path feeding
  #     config.loadRemoteBundleSeed. NOTE that this is not purely mechanical:
  #     that seed site verifies a publisher signature over the exact file bytes
  #     of "<name>.yaml" BEFORE parse, and a tree has no single file to be the
  #     preimage. What a tree signature attests, and when it is checked, is an
  #     open decision — not something to settle by writing the reader.
  #
  # (b) HOOK ORDER (one scenario). See the block above that scenario; the
  #     reason it is red is NOT the fetch gap and never was.
  #
  # (c) THE DELIVERY MATRIX (twelve rows), on the second, independent blocker
  #     stated at that scenario. Fixing the fetch gap did not untag it, exactly
  #     as predicted there.
  # ============================================================================

  # NOTE ON STEP WORDING: J3 already owns `Alice trusts the company key`, and
  # that step reads J3's OWN fixture state (j3.signer, set by J3's publish
  # step) — reusing it here would dereference a signer this journey never
  # created. godog matches on text alone and would have bound it silently, so
  # the wording below is deliberately distinct rather than accidentally
  # near-identical. Same reason for "Alice's pulled ..." on every assertion:
  # skill.feature's `"<path>" is executable` and `"<path>" carries the marker`
  # resolve relative to the PROJECT dir, which is the wrong root for a
  # consumer-side or worktree-side assertion.
  Background:
    Given Trent authors a directory-form bundle "atelier" carrying every surface kind
    And Alice trusts Trent's publishing key

  # --------------------------------------------------------------------------
  # PUBLICATION: one artifact per surface kind, each a genuinely different case.
  #
  # The kinds are read from internal/bundles/bundles.go's Bundle struct, and
  # the tree paths from internal/content/testdata/tree — the canonical layout
  # the shipped content package already reads. NOTE two corrections to the
  # obvious guesses, both verified in code rather than assumed:
  #
  #   - a command's selector directory is "prompts", not "commands"
  #     (trust.ItemKind.Dir(), trust.KindPrompt) — the skill/command rename
  #     freed "skills" for real Agent Skills and left the slash-command item
  #     addressed as "#prompts/<name>".
  #   - a bundle can also hold PROFILES (Bundle.Profiles), a sixth kind. It is
  #     included below. Unlike the other five it is never trust-gated as an
  #     item (there is no trust.ItemKind for a profile), but it is still a file
  #     in the tree that must arrive intact, so it is still a delivery case.
  #
  # The kind->extension split the design settles is visible in the table: the
  # two CONTENT kinds are ".md" (and carry their metadata as front-matter),
  # the executable and structural kinds are ".yaml" (metadata in a
  # ".<name>.meta.yaml" sidecar). Asserting both spellings is the point of
  # enumerating rather than sampling.
  # --------------------------------------------------------------------------
  @wip
  Scenario Outline: Every surface kind survives publication and reaches the consumer's assistant
    Given Trent publishes the "atelier" tree to his company repo, signed with the company key
    When Alice references the company's "atelier" bundle and pulls it
    Then Alice's pulled "atelier" tree carries "<artifact>" byte for byte as published
    And the "<kind>" reaches Alice's assistant carrying the marker "<marker>"

    Examples:
      | kind     | artifact                                | marker                    |
      | fragment | fragments/house-style.md                | ATELIER-FRAGMENT-4a91c2   |
      | command  | prompts/ship-it.md                      | ATELIER-COMMAND-7d33e1    |
      | mcp      | mcp/ledger.yaml                         | ATELIER-MCP-1f88b0        |
      | hook     | hooks/post_file_edit/stamp.yaml         | ATELIER-HOOK-9c02af       |
      | skill    | skills/reviewer/SKILL.md                | ATELIER-SKILL-3e77da      |
      | profile  | profiles/studio.yaml                    | ATELIER-PROFILE-6b41fc    |

  # Metadata placement is a per-kind fact, not a uniform one, and it is the
  # half most likely to be silently dropped by a converter: front-matter lives
  # INSIDE the .md bytes (so it travels for free), a sidecar is a SEPARATE
  # FILE that a tree walk must remember to carry. A publication path that
  # copied content files and forgot sidecars would pass every byte-for-byte
  # assertion above and still lose every mcp and skill description.
  Scenario Outline: Each kind's metadata survives publication in the place that kind stores it
    Given Trent publishes the "atelier" tree to his company repo, signed with the company key
    When Alice references the company's "atelier" bundle and pulls it
    Then Alice's pulled "atelier" tree stores the "<kind>" metadata "<probe>" as <placement>

    Examples:
      | kind     | probe                    | placement                                  |
      | fragment | ATELIER-FRAGMENT-DESC    | front-matter in "fragments/house-style.md" |
      | command  | ATELIER-COMMAND-DESC     | front-matter in "prompts/ship-it.md"       |
      | mcp      | ATELIER-MCP-DESC         | the sidecar "mcp/.ledger.meta.yaml"        |
      | skill    | ATELIER-SKILL-DESC       | the sidecar "skills/.reviewer.meta.yaml"   |

  # An MCP server is a STRUCTURE, not a blob of prose. A substring match on
  # the file would pass even if the command/args/env had been flattened,
  # reordered into a shape the resolver cannot read, or had a field dropped —
  # which is exactly the failure a YAML round-trip through an intermediate
  # representation produces. Assert the parsed structure.
  Scenario: A published MCP server's configuration structure survives, not merely its text
    Given Trent publishes the "atelier" tree to his company repo, signed with the company key
    When Alice references the company's "atelier" bundle and pulls it
    Then Alice's pulled "atelier" MCP server "ledger" parses with its command, args and env intact

  # A skill is the only MULTI-FILE item, and the only one where a POSIX mode
  # is load-bearing. Both are assertable only on the whole package: a scenario
  # that checked SKILL.md alone would miss a dropped scripts/ directory
  # entirely, and one that checked bytes alone would ship a script the model
  # cannot execute. internal/shared/agent/packagefiles.go goes out of its way
  # to re-Chmod on every materialize precisely because this bit drifts.
  Scenario: A published skill package arrives whole, with its script still executable
    Given Trent publishes the "atelier" tree to his company repo, signed with the company key
    When Alice references the company's "atelier" bundle and pulls it
    Then Alice's pulled skill "reviewer" contains exactly the files Trent published, byte for byte
    And Alice's pulled skill "reviewer" file "scripts/run.sh" is executable

  # --------------------------------------------------------------------------
  # HOOKS — the kind where ORDER IS PART OF THE MEANING, and the only kind a
  # tree format can break silently.
  #
  # In bundle.yaml a hook event is an ORDERED LIST (BundleHooks.PostFileEdit
  # and friends are []BundleHook), and within an event hooks merge by PURE
  # APPEND across every source — nothing sorts, nothing consults a per-hook
  # field (wire.UnifiedHooks.Append, agent.MergeHooksConfig). Sequence is
  # therefore a property of the list's ORDER.
  #
  # The tree format stores one file per hook at hooks/<event>/<name>.yaml and
  # enumerates a directory, which yields SORTED-BY-NAME order. Those two facts
  # do not agree, and nothing about the BYTES would show the disagreement: a
  # tree that round-trips four hooks under one event with their sequence
  # permuted is byte-identical, file for file, to one that did not.
  #
  # This scenario asserts the sequence explicitly, per event, and it is the
  # one scenario here that would still be needed if the fetch gap were fixed
  # tomorrow. The fixture deliberately names its post_file_edit hooks so that
  # ALPHABETICAL ORDER AND DECLARED ORDER DISAGREE ("stamp" is declared first,
  # "audit" second) — a fixture whose names happened to sort correctly would
  # pass while proving nothing.
  #
  # STILL RED AFTER THE FETCH GAP CLOSED, and the reason is not the one above.
  # The delivery is byte-perfect: both hooks arrive, under the right event, and
  # the observed failure is exactly "got [audit stamp], want [stamp audit]".
  # But the tree format DOES have an order carrier — content.Hook.Order, a
  # sparse integer in the hook's metadata sidecar, resolved by
  # content.SortHooks (see internal/content/convert's package doc, which
  # records that an earlier "NN-" filename-prefix scheme was retracted in
  # favour of it). This fixture uses neither: it authors bare hook files with
  # no sidecar and no order field, and the assertion reads raw directory order
  # rather than resolving through SortHooks. So what is red here is a fixture
  # that declares order by LIST POSITION in a format that has no list, checked
  # against an enumeration that never consults the field the format provides.
  #
  # That is left as-is rather than quietly repaired, because repairing it
  # changes what the scenario proves — from "declared order survives the trip"
  # to "the Order sidecar field survives the trip", which is a different and
  # narrower claim. Someone who owns the spec should choose.
  # --------------------------------------------------------------------------
  @wip
  Scenario: Hooks arrive in the right event buckets AND in their declared order within each
    Given Trent publishes the "atelier" tree to his company repo, signed with the company key
    When Alice references the company's "atelier" bundle and pulls it
    Then Alice's pulled "atelier" hooks under "post_file_edit" are exactly "stamp, audit" in that order
    And Alice's pulled "atelier" hooks under "session_start" are exactly "greet" in that order
    And no pulled "atelier" hook appears under an event it was not declared in

  # --------------------------------------------------------------------------
  # DELIVERY MATRIX — the two isolation axes, crossed.
  #
  # Publication is only half the claim. The other half is that the bytes and
  # the modes reach a path THE AGENT IN THAT CONFIGURATION CAN ACTUALLY READ.
  # The axes are independent and compose (docs: workspace isolates the FILES,
  # runtime isolates the PROCESS), so all four cells are distinct claims:
  #
  #   host + none       — the baseline: materialize into the live project dir
  #   host + worktree   — WorkDir is a detached checkout under the session's
  #                       ephemeral/ dir, NOT under the project (worktree.go's
  #                       worktreeScratchPath); surfaces must follow it there
  #   container + none  — see the hazard below; this is the cell that fails
  #   container + wt    — the worktree checkout is bind-mounted identical-path
  #
  # THE SECOND BLOCKER (this scenario only, and NOT untagged by fixing the
  # fetch gap): there is no hermetic vehicle in this suite that both runs an
  # agent under a chosen isolation configuration AND materializes surfaces.
  #   - the built-in mock backend materializes NOTHING: Mock.Setup only records
  #     its payload (internal/lm/backends/mock.go) and its registry descriptor
  #     has no surfaces at all (internal/lm/backends/registry.go — "the mock
  #     backend registers only backend+config"), so the j9 workspace steps
  #     cannot produce a .claude/skills/ tree to assert on.
  #   - the RUNTIME axis has no hermetic container cell anywhere: j9_isolation
  #     .feature says so in its own prose ("no @container scenario exists in
  #     this file") and only proves the fail-loud/degrade contract. The live
  #     container proof is @live, in isolation_probe.feature.
  # UNTAG CONDITION (secondary, this scenario): a hermetic fixture backend that
  # materializes real surfaces under a chosen runtime/workspace pair — i.e. the
  # j9 spy vehicle extended to assert delivered FILES, plus a hermetic
  # container cell. Fixing the fetch gap alone leaves this red.
  #
  # A THIRD, SUBSTANTIVE HAZARD this scenario exists to surface rather than
  # dodge — a real product gap, not a harness one. On the {container, none}
  # cell, containerConfigOverlay (internal/lm/isolation/container.go) bind-
  # mounts a per-run SCRATCH directory over ".claude" (profile.overlayDirs).
  # Surfaces materialized in-container therefore land in that scratch overlay,
  # which is removed at teardown and is never copied back out. The agent can
  # read them; the host cannot; nothing survives the run. The container's
  # session-state mounts do not help — sessionStateMounts binds exactly
  # <harp>/persist and <harp>/persist/transcripts (statemounts.go, pinned by
  # statemounts_test.go's "transcript store + persist only"), deliberately NOT
  # <harp>/ephemeral/, and NOTHING at the harp-dir top level (task
  # `operable-account`'s unclassified-middle zone). If a delivered artifact
  # lands anywhere but the mounted project/worktree dir, a containerized agent
  # cannot see it, and this row is how that is found out rather than assumed.
  # --------------------------------------------------------------------------
  @wip
  Scenario Outline: The published artifacts reach the agent in every isolation configuration
    Given Trent publishes the "atelier" tree to his company repo, signed with the company key
    And Alice references the company's "atelier" bundle and pulls it
    When Alice runs an agent with runtime "<runtime>" and workspace "<workspace>"
    Then the "<artifact>" is readable by that agent, byte for byte as published
    And the mode of "<artifact>" is "<mode>" where that agent can read it

    Examples: host runtime — the workspace axis alone
      | runtime | workspace | artifact                        | mode |
      | host    | none      | fragments/house-style.md        | 0644 |
      | host    | none      | skills/reviewer/SKILL.md        | 0644 |
      | host    | none      | skills/reviewer/scripts/run.sh  | 0755 |
      | host    | worktree  | fragments/house-style.md        | 0644 |
      | host    | worktree  | skills/reviewer/SKILL.md        | 0644 |
      | host    | worktree  | skills/reviewer/scripts/run.sh  | 0755 |

    Examples: container runtime — the process boundary crossed
      | runtime   | workspace | artifact                        | mode |
      | container | none      | fragments/house-style.md        | 0644 |
      | container | none      | skills/reviewer/SKILL.md        | 0644 |
      | container | none      | skills/reviewer/scripts/run.sh  | 0755 |
      | container | worktree  | fragments/house-style.md        | 0644 |
      | container | worktree  | skills/reviewer/SKILL.md        | 0644 |
      | container | worktree  | skills/reviewer/scripts/run.sh  | 0755 |
