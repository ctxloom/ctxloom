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

  When this journey was written it was not one act, and for one of those kinds
  it was not possible at all. It stated the whole claim anyway, because the
  claim is what the bundle-as-tree restructure is FOR, and a journey that only
  asserted the parts that already worked would have gone green while the
  capability stayed missing. It is all green now; the block below records what
  each blocker was and how it closed, because a journey that goes green
  silently is as easy to misread as one that never does.

  # ============================================================================
  # WHY THIS IS A NEW JOURNEY (J001400) AND NOT AN EXTENSION OF J000700/J001500/J001600
  #
  # Three existing journeys touch neighbouring ground and none of them is the
  # right home:
  #
  #   J000700 (team authoring) is FIRST-PARTY and in-project — Carol commits to the
  #   team's own repo and Bob pulls the project. There is no publisher, no
  #   remote bundle reference and no signature anywhere in it; its whole point
  #   is that none of that is needed when the team owns the code. Publication
  #   is the thing this journey is about, so it cannot live there.
  #
  #   J001500 (corporate signed) is about PROVENANCE AND INTEGRITY of content that
  #   already flows: its Background fixes a "secure-coding" bundle and every
  #   scenario perturbs the TRUST inputs — tamper, reject, retract, revoke.
  #   Adding "and can this kind of content be published at all" there would
  #   conflate a trust question with a capability question, and its scenarios
  #   are LOCKED and green. A red capability row inside a locked trust journey
  #   makes both harder to read.
  #
  #   J001600 (signing) is the AUTHOR-side key journey — ssh-agent, allowed_signers,
  #   the project/user store split, sign/verify/move. It ends at "Alice pulls
  #   the newly published version" because delivery is not what it is proving.
  #
  # This journey's subject is the one none of them own: does the PAYLOAD of
  # every surface kind survive the trip from an author's tree to a consumer's
  # agent, across the isolation configurations that agent might be running in.
  # It was also, when written, entirely red — and it needed to be independently
  # untaggable as a unit, which it could not be folded into a green journey.
  #
  # The existing suite is NOT renumbered. This is additive.
  # ============================================================================

  # ============================================================================
  # WHAT WAS @wip HERE, AND WHAT UNTAGGED EACH PART
  #
  # THE PRIMARY BLOCKER IS FIXED. It was: a directory-form bundle could not be
  # fetched from a remote at all — fetchAtLockedSHA resolved a ref to ONE file
  # path and called FetchFile on it, there was no tree fetch anywhere in
  # internal/remote, and a remote bundle WAS "<name>.yaml" by construction —
  # while internal/bundles/loader.go:389 refuses skills in a single-file
  # bundle. Jointly unsatisfiable, which is what taskloom task
  # `engaged-chivalry` recorded as impossible.
  #
  # `deps pull` now probes the directory form when the single file is absent,
  # walks the tree at the pinned SHA through internal/content/remotetree, and
  # installs it under the consumer's cache with the publisher's exec bit
  # intact. Every scenario asserting the PUBLICATION SIDE — the payload landing
  # in the consumer's tree, per-kind metadata placement, MCP structure, and the
  # whole skill package with its executable script — is untagged and green.
  #
  # THE TREE READ PATH IS ALSO FIXED. It was the second blocker: the bytes
  # arrived but nothing read a bundle's ITEMS back out of a tree.
  # internal/content/convert now goes both ways (convert.Read), and
  # config.loadRemoteBundleSeed reads a tree-shaped lockfile entry from its
  # installed tree, verifying it through internal/content/attest — the manifest
  # signature plus a two-directional contents check — instead of dead-ending at
  # remote.ErrTreeBundleUnreadable.
  #
  # The open question that stopped the reader being written is CLOSED, and the
  # answer was already in docs/design/bundle-as-tree.design.md: a tree
  # signature covers the SHA256SUMS manifest over file bytes, checked at the
  # content gate, most-specific-wins. Trent's fixture now signs the tree the way
  # the Given always claimed it did, so five of the six kinds — fragment,
  # command, mcp, hook and the whole skill package — reach Alice's assistant.
  #
  # Two more things were red, and both are green now:
  #
  # (a) HOOK ORDER (one scenario) was red for a fixture reason, never the fetch
  #     gap — see the block above that scenario for what was actually wrong
  #     and how the fixture and its assertions were corrected.
  #
  # (b) was THE CONTAINER HALF OF THE DELIVERY MATRIX (six rows), and is now
  #     GREEN: a hermetic container cell exists
  #     (internal/testsupport/containercell) and those rows run a real
  #     `profile materialize` INSIDE a container against a bind-mounted target,
  #     asserting bytes, mode and OWNERSHIP from the host side. What they do not
  #     reach — ctxloom's OWN container launch and its containerConfigOverlay —
  #     is covered by internal/lm/isolation's TestContainerRun_* pair instead,
  #     for the reason stated at that scenario (the delivery is only observable
  #     mid-turn, which a godog step over a shelled-out `run` cannot hold open).
  #
  # PROFILES and THE HOST HALF OF THE DELIVERY MATRIX were red here and are now
  # green. The profile row was red because "reaches the assistant" is not a
  # claim a profile can satisfy; it now asserts the profile's EFFECT instead.
  # The host matrix rows were red because no hermetic backend materialized
  # anything; mock now delivers a context file and a skills tree through the
  # shared seam. Both blocks say so at the scenario.
  # ============================================================================

  # NOTE ON STEP WORDING: J001500 already owns `Alice trusts the company key`, and
  # that step reads J001500's OWN fixture state (j001500.signer, set by J001500's publish
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

  # --------------------------------------------------------------------------
  # PROFILES — the sixth kind, split out because "reaches the assistant" does
  # not mean the same thing for it, and the difference is not a gap in delivery.
  #
  # A profile is not content an assistant READS. It is the thing that SELECTS
  # what an assistant reads. So it is delivered when its EFFECT is observable,
  # and the assertion below states exactly that: materialize the published
  # profile BY ITS OWN bundle-qualified ref, and require the content only IT
  # selects to arrive.
  #
  # The marker therefore lives in a FRAGMENT that studio selects, not in
  # studio's own description — a description is metadata about a selector, and
  # no engine surface renders it, so asserting on it would be asserting that
  # ctxloom leaks profile metadata into an assistant's context, which it must
  # not do.
  #
  # Three distinct failure modes ride on this one line, which is why it is this
  # line and not a weaker one:
  #
  #   - the profile file arrives but is never seeded into the shared profile
  #     loader (config.loadBundleProfileSeed)  -> the ref does not resolve
  #   - it is seeded, but its short same-repo selector does not resolve against
  #     the PULLED tree                        -> assembly finds nothing
  #   - it resolves, but the selected fragment is never delivered
  #                                            -> no file under the target
  #     carries the marker
  #
  # The candidates that were rejected: "studio is listable and runnable" catches
  # only the first, and dropping the second assertion entirely catches none and
  # narrows the journey's claim to "arrives on disk".
  #
  # The vehicle is `profile materialize`, not `ctxloom run`, for the reason
  # stated at the delivery matrix below: a run's Cleanup strips what it
  # delivered before any step could stat it.
  # --------------------------------------------------------------------------
  Scenario: A published profile arrives and becomes one of Alice's profiles
    Given Trent publishes the "atelier" tree to his company repo, signed with the company key
    When Alice references the company's "atelier" bundle and pulls it
    Then Alice's pulled "atelier" tree carries "profiles/studio.yaml" byte for byte as published
    And materializing Alice's "company/atelier#profiles/studio" delivers the marker "ATELIER-PROFILE-6b41fc"

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
  # The tree format stores one file per hook at hooks/<event>/<name>.yaml, and
  # a directory has no list — so its ONLY order carrier is content.Hook.Order,
  # a sparse integer in the hook's own ".<name>.meta.yaml" sidecar, resolved by
  # content.SortHooks (an earlier "NN-" filename-prefix scheme was retracted in
  # favour of it — see internal/content/convert's package doc). Without that
  # field a reader has nothing to sort by except the filename, and nothing
  # about the BYTES would show the disagreement: a tree that round-trips four
  # hooks under one event with their sequence permuted is byte-identical, file
  # for file, to one that did not.
  #
  # This scenario asserts the sequence explicitly, per event, and it is the
  # one scenario here that would still be needed if the fetch gap were fixed
  # tomorrow. The fixture declares each post_file_edit hook's order via its
  # sidecar (stamp=100, audit=200) so that ALPHABETICAL ORDER AND DECLARED
  # ORDER DISAGREE — a fixture whose names happened to sort correctly would
  # pass while proving nothing.
  #
  # WAS RED AFTER THE FETCH GAP CLOSED, for a fixture reason, not a product
  # one: content.Hook.Order and content.SortHooks already existed and needed
  # no change. The fixture authored bare hook files with no sidecar and no
  # order field, and the assertion read raw directory order (sorted by
  # filename) rather than resolving through SortHooks — a fixture declaring
  # order by LIST POSITION in a format that has no list, checked against an
  # enumeration that never consulted the field the format provides. Fixed by
  # authoring the sidecar and reading the pulled tree through the same
  # content.NewTreeStore -> Bundle.Refs/Item/Surface path the product uses,
  # then resolving with content.SortHooks — the "declared order survives the
  # trip" claim, for a directory-form bundle, IS "the Order sidecar field
  # survives the trip", because that field is the only way a tree-form bundle
  # can declare order at all.
  #
  # The bucketing half reads each hook's DECODED content.Hook.Event (via
  # Item.Surface, i.e. hookType.Decode), not the ref's path string — the ref
  # path only proves a hook's file sits in the right directory, where the
  # decoded Event field is what internal/bundles.ReadTree's reader.add
  # actually keys its per-event grouping on once a real pulled tree becomes
  # the bundle the product merges.
  # --------------------------------------------------------------------------
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
  #   host + none       — the baseline: deliver into the live project dir
  #   host + worktree   — the workspace is a detached checkout OUTSIDE the
  #                       project tree, the shape isolation.Worktree resolves
  #                       (worktree.go's worktreeScratchPath puts it under the
  #                       session's ephemeral/ dir); surfaces must follow it
  #                       there, and a writer that resolved paths against the
  #                       PROJECT instead of the target lands nothing here
  #   container + none  — see the hazard below; this is the cell that fails
  #   container + wt    — the worktree checkout is bind-mounted identical-path
  #
  # THE HERMETIC VEHICLE, and what it is NOT.
  #
  # The mock backend is now the vehicle for the host rows, because it now
  # genuinely delivers. Mock embeds agent.LaunchBackend, so its Setup routes
  # through the SAME surfaces × typed-cells seam every real launch backend uses
  # (internal/lm/backends/mock.go), and it declares two surfaces of its own
  # (internal/lm/backends/mock_surfaces.go): a CONTEXT surface writing the
  # ctxloom-managed section of MOCK_CONTEXT.md through the shared
  # agent.WriteManagedContext, and a SKILLS surface — the shared
  # agent.ManagedSkillPackagesDelivery bound to the shared
  # agent.WriteManagedSkillPackages — producing a .mock/skills/ tree. Both are
  # the same writers claude's .claude/skills/, kiro's .kiro/, opencode's
  # .opencode/skill/ and antigravity's .agents/ go through, differing only in
  # the directory they target, so a row that passes here is exercising the
  # shared seam rather than a mock-only path.
  #
  # (This paragraph previously said the opposite twice and was wrong on both
  # counts: it said Mock.Setup "only records its payload" and that mock's
  # registry descriptor "has no surfaces at all" — untrue since 4f1ca395 — and
  # after mock gained a context surface it still implied no skills tree was
  # possible — untrue since 3ec1643e.)
  #
  # THE VEHICLE IS `profile materialize`, NOT `ctxloom run`. That is a
  # lifecycle fact about every backend, not a mock limitation: grpc.RunTurn
  # calls Cleanup immediately after Execute, and the shared LIFO reversal
  # strips the delivered managed section — removing the file outright when
  # nothing user-authored remains. A step that shells out to `run` and then
  # stats the directory observes nothing, exactly as it would for antigravity's
  # AGENTS.md. The LIVE-RUN half of the claim is covered where it can be
  # observed mid-turn: tests/integration/delivery_approach_matrix_test.go (mock
  # is in all three matrix tests) and grpc_test's
  # TestRunTurn_MockDeliversContextSurfaceDuringTheTurn.
  #
  # WHAT THE HOST ROWS THEREFORE DO AND DO NOT PROVE. They prove that the
  # published bytes and the published MODES reach the engine-native path an
  # agent reads, in the project dir and in a detached checkout outside it —
  # including that the skill script's exec bit travels, which is a claim about
  # the sidecar DECLARATION rather than the fixture's filesystem bit (the
  # manifest attests content.ComponentMode and agent.WriteManagedSkillPackages
  # re-asserts it with Chmod, so an existing file's mode cannot win). They do
  # NOT prove the RESOLUTION step — that a real run points delivery at its own
  # worktree — which is J002200's subject (its mock req.WorkDir record) and the
  # integration matrix's.
  #
  # A fragment is asserted differently from a skill on purpose. A skill IS a
  # file and is compared whole, byte for byte. A fragment is never delivered as
  # a file — it is merged into the engine's single context file alongside every
  # other fragment the profile selects — so the only honest form of "byte for
  # byte" for it is "its published body, verbatim, inside that file". The
  # fixture's fragment body is several lines long so that check cannot decay
  # into a marker search.
  #
  # THE MODE COLUMN MEANS TWO DIFFERENT THINGS, for the same reason. On a skill
  # file it is the PUBLISHER'S declaration travelling: the sidecar declares
  # scripts/run.sh executable, the manifest attests content.ComponentMode, and
  # agent.WriteManagedSkillPackages re-asserts it with Chmod — so 0755 here is a
  # claim about the publication round trip, and 0644 on SKILL.md is the claim
  # that a blanket chmod did not smear the exec bit across the package. On the
  # fragment row it is the ENGINE'S CONTEXT FILE's mode, because a merged
  # fragment has no mode of its own: 0600, owner-only, which is what
  # agent.AtomicWriteFile deliberately defaults a new managed file to (and it
  # reuses an existing file's mode rather than widening it). That row is pinned
  # here so a context file that started arriving world-readable would be caught,
  # not because 0600 is the publisher's bit.
  # --------------------------------------------------------------------------
  Scenario Outline: The published artifacts reach a host agent in every workspace
    Given Trent publishes the "atelier" tree to his company repo, signed with the company key
    And Alice references the company's "atelier" bundle and pulls it
    When the pulled surfaces are delivered to a "<runtime>" agent in its "<workspace>" workspace
    Then the "<artifact>" reaches that agent's workspace carrying its published bytes
    And the mode of "<artifact>" is "<mode>" where that agent can read it

    Examples: host runtime — the workspace axis alone
      | runtime | workspace | artifact                        | mode |
      | host    | none      | fragments/house-style.md        | 0600 |
      | host    | none      | skills/reviewer/SKILL.md        | 0644 |
      | host    | none      | skills/reviewer/scripts/run.sh  | 0755 |
      | host    | worktree  | fragments/house-style.md        | 0600 |
      | host    | worktree  | skills/reviewer/SKILL.md        | 0644 |
      | host    | worktree  | skills/reviewer/scripts/run.sh  | 0755 |

  # --------------------------------------------------------------------------
  # THE CONTAINER HALF OF THE MATRIX — now green, on whichever container
  # runtime the runner has.
  #
  # WHAT UNBLOCKED IT. A hermetic container cell now exists
  # (internal/testsupport/containercell): a `FROM scratch` image carrying one
  # statically linked ctxloom, the environment root bind-mounted at its own
  # absolute path, `--network=none`, and `profile materialize --backend mock`
  # executed INSIDE the container. The rows below assert on the HOST side of
  # that mount. Nothing about that route existed when this block said it was
  # impossible: the suite launched no container anywhere (j002200 proves the
  # fail-loud DEGRADE contract, which is a different claim from "a container
  # run delivered these bytes"), the fixture image had no engine, and — the
  # part that was actually load-bearing — the mock backend materialised NOTHING
  # until it started delivering through the shared cells seam, so a cell would
  # have had nothing to observe even with a daemon, an image and a mount.
  #
  # WHY THIS IS NOT THE VACUOUS VERSION. Delivering on the host and asserting a
  # host path would pass every row below without crossing the process boundary
  # they name. Three things stop that: the delivering process is inside the
  # container and its ONLY route to the target is the bind mount, so a delivery
  # that resolved to a container-private path lands in the ephemeral layer and
  # the row goes red on an absent file; the target is checked EMPTY before the
  # container starts, so no host-side leftover can stand in; and each row
  # asserts bytes, POSIX mode AND OWNERSHIP.
  #
  # OWNERSHIP IS THE ROW THE HOST OUTLINE DOES NOT HAVE, and it is the only
  # property a process boundary breaks while bytes and modes come through
  # untouched. A ROOTFUL daemon writing through a bind mount produces
  # byte-identical, mode-identical, ROOT-OWNED files in the invoking user's
  # tree. That is the bug class the PUID/PGID entrypoint remap exists for, and
  # before this line nothing in the suite would have noticed it. The cell runs
  # as container-root under a rootless runtime (where root IS the invoker on
  # the host filesystem) and as --user <hostuid>:<hostgid> under a rootful one;
  # this assertion is what checks that rule rather than trusting it.
  #
  # WHICH RUNTIME RUNS THESE ROWS depends on the runner, and the matrix is
  # asymmetric on purpose: no host is both rootful and rootless, and most have
  # no podman, so each runner covers what it has. A missing runtime SKIPS the
  # scenario naming the runtime and what did not run; CTXLOOM_REQUIRE_DOCKER=1
  # turns "no runtime at all" into a failure; and CTXLOOM_REQUIRE_RUNTIMES
  # (dockergate) lets a lane declare "I cover podman" so that claim is
  # enforceable rather than aspirational. The three-runtime matrix over the
  # cell itself lives in internal/testsupport/containercell's
  # docker_integration test, which is where a per-runtime cell can be a
  # subtest rather than a whole journey re-run.
  #
  # WHAT THESE ROWS DO NOT PROVE, AND WHERE THAT IS NOW PROVEN INSTEAD. They
  # exercise a container that ctxloom's own isolation machinery did not build:
  # the cell mounts and launches directly, so these rows say nothing about
  # containerConfigOverlay either way. That half is closed in
  # internal/lm/isolation's docker_integration lane, by
  # TestContainerRun_DeliversIntoTheMountedWorkspace and
  # TestContainerRun_OverlaidConfigDirIsSwallowedByTheScratchOverlay: the same
  # cell, wired to a container the PRODUCT builds (Container.PrepareWorkspace →
  # SpawnClient → a real turn whose in-container Setup delivers), observed
  # MID-TURN because grpc.RunTurn's Cleanup strips the delivery the instant
  # Execute returns.
  #
  # WHAT THOSE TESTS FOUND. containerConfigOverlay is SOUND, not a hole. A
  # surface whose target is NOT an overlaid directory lands in the bind-mounted
  # host project with its bytes, POSIX mode and host ownership intact — that is
  # the "points delivery at the mounted workspace" claim, live. A surface whose
  # target IS one of profile.overlayDirs (claude's ".claude", where claude's own
  # skills/settings writers aim) lands in the per-run scratch overlay and never
  # in the host project — but the overlay is bind-mounted AT THAT SAME PATH, so
  # the in-container engine reads exactly what was delivered while the run is
  # live. It is discarded at teardown, which is what the overlay is FOR (the
  # host project stays clean) and matches the host axis anyway, where the shared
  # LIFO Cleanup reverses the same delivery. The container's session-state
  # mounts are not a workaround for this and were never meant to be —
  # sessionStateMounts binds exactly <harp>/persist and
  # <harp>/persist/transcripts (statemounts.go, pinned by statemounts_test.go's
  # "transcript store + persist only"), deliberately NOT <harp>/ephemeral/ and
  # NOTHING at the harp-dir top level.
  # --------------------------------------------------------------------------
  Scenario Outline: The published artifacts reach a containerized agent across the process boundary
    Given Trent publishes the "atelier" tree to his company repo, signed with the company key
    And Alice references the company's "atelier" bundle and pulls it
    When the pulled surfaces are delivered to a "<runtime>" agent in its "<workspace>" workspace
    Then the "<artifact>" reaches that agent's workspace carrying its published bytes
    And the mode of "<artifact>" is "<mode>" where that agent can read it
    And "<artifact>" is owned on the host by the user that ran the delivery

    Examples: container runtime — the process boundary crossed
      | runtime   | workspace | artifact                        | mode |
      | container | none      | fragments/house-style.md        | 0600 |
      | container | none      | skills/reviewer/SKILL.md        | 0644 |
      | container | none      | skills/reviewer/scripts/run.sh  | 0755 |
      | container | worktree  | fragments/house-style.md        | 0600 |
      | container | worktree  | skills/reviewer/SKILL.md        | 0644 |
      | container | worktree  | skills/reviewer/scripts/run.sh  | 0755 |
