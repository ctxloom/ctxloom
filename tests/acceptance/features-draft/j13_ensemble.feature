@doc
Feature: The ensemble surface — fan work across many members, weave the results back

  J6 narrates ONE delegated child. Real coordination is plural: Alice fans a
  single shared task out to SEVERAL members at once (`ctxloom map`), then folds
  their outputs back into one synthesized result (`ctxloom weave`). This is the
  map→reduce primitive — the "map" half broadcasts the task to every member and
  emits one labeled block per member (internal/cli/map.go:103,
  internal/operations/ensemble.go:295 FormatParts); the "weave" composite runs
  that fan and then reduces it through a high-power synthesis profile in-process
  (internal/cli/weave.go:96, internal/operations/ensemble.go:224).

  The claim this journey exists to pin down is that an ensemble is not a shared
  blast radius wearing a plural name: each member is dispatched on its OWN
  resolved binding and gets its OWN isolation workspace axis — a distinct
  worktree keyed by the member's identifier (ensemble.go:148-151 AgentID scopes
  the per-member workspace; ensemble.go:266 memberAxes), even though the fan's
  workspace VALUE is one setting for the whole run. What breaks without this
  journey: today nothing narrates the multi-member surface at all, so a
  regression that collapsed every member into the same checkout, dropped a
  member from the fan, or let a synthesizer reduce only the last member's output
  would pass every existing acceptance test. The fan is also fault-tolerant by
  contract (partial success is success — ensemble.go:81,159 record a failed
  member as an error Part instead of aborting the set); nothing proves that
  either.

  # WHAT THIS JOURNEY CAN AND CANNOT SEE (honesty, mirroring j6_delegation's and
  # j9_isolation's own notes). tests/acceptance drives a `ctxloom` SUBPROCESS and
  # reads only what lands on stdout or on disk. The built-in mock backend
  # (internal/lm/backends/mock.go) is the ONLY hermetic engine, and it is a bare
  # echo: it never spawns a real engine grandchild and never reasons. Several
  # structural facts follow, confirmed by reading source, not assumed:
  #
  #   1. THE SINGLE RECORD FILE. The mock records each request to ONE fixed path,
  #      CTXLOOM_MOCK_RECORD_FILE, set once at the engine-config level
  #      (mock.go:69/121; tests/integration/testenv/mock_lm.go:89 writes exactly
  #      one path). Members run in PARALLEL (ensemble.go:111-170), so if every
  #      member shared one mock label they would clobber that file
  #      last-writer-wins — the fan's ordering is not a reliable way to read two
  #      members apart. This journey therefore does NOT try to distinguish
  #      concurrent members through one shared record file. Instead each member
  #      agent is bound to its OWN mock engine label with its OWN record file (a
  #      per-member record token); after a fan, each member's file is read
  #      independently, so ordering never matters. This is the same discipline
  #      j9 used for the workspace axis, extended to plural members.
  #
  #   2. WORKDIR, NOT CWD, IS THE WORKSPACE SIGNAL. The mock never os.Chdir's and
  #      never spawns a grandchild, so os.Getwd() is the plugin's inherited cwd —
  #      identical across every workspace axis (mock.go:99-107, quoting j9). The
  #      honest per-member signal is req.WorkDir, recorded as `workdir=`
  #      (mock.go:121) — the value isolation actually resolved for THAT member.
  #      Distinct per-member worktrees are read from the per-member record files'
  #      workdir= lines, never from cwd.
  #
  #   3. AGGREGATION REACHING THE REDUCER IS HERMETICALLY VISIBLE. weave runs the
  #      members, then runs the synthesizer as a separate oneshot whose prompt is
  #      buildSynthesisTask — which embeds FormatParts of EVERY member's part
  #      (ensemble.go:248,277-287). Because synthesis runs AFTER the member fan
  #      completes (ensemble.go:170 wg.Wait, then 246 RunOneshot), the shared
  #      record file's LAST writer is the synthesizer, and its `=== Prompt ===`
  #      section carries every member's labeled block. That is a genuine,
  #      disk-durable proof that the reduce saw all of the map — not a struct
  #      capture, not faked.
  #
  # What this journey CANNOT prove, and does not claim: that a REAL engine did
  # real reasoning or that the synthesis is semantically good. The mock's member
  # outputs are fixed echoes (mock.go:71/155 CTXLOOM_MOCK_RESPONSE), so we prove
  # DISPATCH (every member ran), DISTINCT WORKSPACES (per-member isolation axis),
  # and STRUCTURAL AGGREGATION (the reduce received every part), never the
  # quality of what a live model would produce. The RUNTIME axis's per-member
  # container boundary needs a live daemon plus a built image and is deferred
  # exactly as in j9 (no @container scenario here). Binding each member to its
  # own DISTINCT real engine — the production multi-engine fan — is likewise out
  # of hermetic scope; the per-member mock labels stand in for it structurally.

  Background:
    Given Alice has a git-backed project with two member agents "reviewer-sec" and "reviewer-perf", each bound to its own mock engine with its own record file
    And Carol authored a synthesis profile "synthesis" that reduces member outputs

  # LOCKED — the base fan-out claim: `map` broadcasts one task to EVERY member
  # and emits one labeled block per member, in member order (map.go:103 prints
  # FormatParts; ensemble.go:59 parts are indexed in input order, 302 headers
  # each part by its own Profile). Break-point: a fan that dropped a member, or
  # reused one member's label for another, fails the per-member-header assertion.
  Scenario: map fans one shared task across every member and labels each output
    When Alice maps the task "review this change" across members "reviewer-sec" and "reviewer-perf"
    Then the map output carries one labeled part per member, in member order
    And each part is headed by its own member's name

  # LOCKED — the sharpest claim, and the reason this journey exists: each member
  # gets its OWN isolation workspace axis. Under "none" every member shares the
  # live project dir; under "worktree" each member lands in a DISTINCT checkout
  # keyed by its identifier (ensemble.go:148-151 AgentID; 266 memberAxes), none
  # under the project tree. Read per member from each member's own record file's
  # workdir= line (honesty note 1 & 2), so the parallel fan's ordering never
  # matters. Genuinely two members precisely so a one-value read cannot pass by
  # accident. Mirrors j9's locked "two isolated runs share no workspace".
  Scenario Outline: Each mapped member lands in the workspace its axis dictates
    When Alice maps a task across "reviewer-sec" and "reviewer-perf" under workspace "<workspace>"
    Then the members' recorded workdirs <relationship>

    Examples:
      | workspace | relationship                                                |
      | none      | are the same and both are the project directory             |
      | worktree  | are two distinct worktrees, and neither is under the project directory |

  # LOCKED — the reduce half: weave folds every member's output into one
  # synthesized result, and the reduce provably SAW every member. The woven
  # result carries a Report plus one Part per member (ensemble.go:187,241,257),
  # and the synthesizer's recorded prompt embeds every member's labeled block
  # (ensemble.go:248,277-287; observable because the synth is the record file's
  # last writer — honesty note 3). Break-point: a weave that synthesized only
  # the last member fails the "embeds every member" assertion.
  Scenario: weave reduces every member's output into one synthesized result
    When Alice weaves "reviewer-sec" and "reviewer-perf" through synthesis profile "synthesis" over the task "review this change"
    Then the woven result carries a synthesized report and both members' parts
    And the synthesizer's recorded prompt embeds every member's labeled part

  # LOCKED — map and reduce are SEPARABLE halves of the one surface: weave
  # --no-synthesize (weave.go:197; ensemble.go:242 returns before RunOneshot)
  # emits the fanned parts with the reduce skipped entirely. The result carries
  # both members' parts but no report and no synthesizer record — proving the
  # fan does not depend on a synthesis pass to run.
  Scenario: weave --no-synthesize emits the members' parts with no reduce
    When Alice weaves "reviewer-sec" and "reviewer-perf" with synthesis skipped over the task "review this change"
    Then the woven result carries both members' parts
    But it carries no synthesized report

  # LOCKED — the ensemble is fault-tolerant: a member that FAILS is reported
  # inline as an error part and never aborts the others (partial success is
  # success — ensemble.go:81/159 record a failed member as an error Part; map.go:96
  # only warns). The failure here is surfaced as a payload error BLOCK in the
  # aggregated output, not read from the run's process exit code. The surviving
  # member's own output is present and untouched — genuinely two members so the
  # survivor's block cannot be the failure's.
  Scenario: One failing member is reported inline and never aborts the ensemble
    Given member "reviewer-perf" is bound to an engine that exits nonzero
    When Alice maps the task "review this change" across "reviewer-sec" and "reviewer-perf"
    Then the map output carries an error part for "reviewer-perf"
    And it still carries "reviewer-sec"'s real output, unaffected by the failure
