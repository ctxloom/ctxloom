@doc
Feature: The close-out — the end of a workstream

  The feature shipped on Friday. Monday the debris is real, and none of it is
  interesting enough that anyone will deal with it voluntarily: stale worktrees,
  several holding commits that were never merged; thousands of session
  directories; and the only durable record of what the team actually learned
  sitting in one person's head, where it will stay.

  This is the leg of the flywheel that closes it. A session produces a lesson,
  the lesson becomes authored content, the content shapes the next session.
  Skip it and ctxloom is a delivery pipeline with an open loop: it can hand an
  assistant everything the team has ever written down, and nothing that
  Friday's work discovered.

  The close-out routine is itself shipped content — a signed bundle command run
  as `ctxloom run -r cleanup` — walking noun-homed leaves in order:
  preconditions, then worktrees, then lessons, then retention. There is
  deliberately no top-level `cleanup` verb; that question was settled by the
  noun-verb convention and is not re-litigated here.

  # NOTE ON SCOPE — THIS FEATURE IS A SPECIFICATION. Almost nothing it drives
  # exists yet. `session worktrees`, `session purge`, `session distill --skill
  # --to-bundle` and doctor's five new checks are proposed surfaces, and these
  # scenarios are their acceptance definition. Nearly every scenario here is
  # RED, and that is the deliverable rather than a defect in the file.
  #
  # NOTE ON WHAT THE FIXTURES BUILD. A close-out flow is defined almost
  # entirely by what it REFUSES to do, and a refusal cannot be tested against a
  # fixture with nothing to refuse. So these scenarios build the real debris:
  # genuine `git worktree add` checkouts inside a harp's own ephemeral
  # directory with real sibling owner-pid markers, foreign long-lived worktrees
  # outside the sessions root, uncommitted work, and harp directories carrying
  # machine-written bulk beside human-authored plan files. Every "spared",
  # "skipped" and "preserved" assertion reads a real file that a wrong
  # implementation would really have destroyed.
  #
  # NOTE ON THE CONFIRMATION LINE, which several scenarios enforce directly:
  # inspect always (read-only default on every leaf); additive writes proceed
  # on plain apply WITH payload assertions ("wrote 0 fragments" is
  # exit-nonzero, never success); destruction confirms per item against
  # evidence shown in the same invocation; and the refusal list is hard-coded —
  # no force-remove, no dirty or unmerged or unowned trees, no live session, no
  # undistilled `--everything` without its own flag, no touching vendor stores.
  #
  # NOTE ON TAGS. Every scenario is @wip with its own untag condition. The
  # handful believed to pass today say so plainly rather than being left silent.

  Background:
    Given the feature shipped on Friday and Alice is closing the workstream out

  # ---- Step 1: preconditions --------------------------------------------
  # doctor check 1 (gitignore posture). Red stops the routine with the fix
  # NAMED — a precondition that fails without saying what to run is a
  # precondition nobody will satisfy. This is not hypothetical: ctxloom's own
  # repository is in exactly this state, `git ls-files .ctxloom` returns zero
  # tracked files, and the retirement machinery to fix it is built and merely
  # un-run.
  #
  # UNTAG WHEN: doctor reports a superseded blanket `.ctxloom` ignore rule and
  # names `ctxloom manage gitignore install` as the fix. Expected RED today:
  # gitignore.Ensure performs the retirement when it runs, but no doctor check
  # reports the posture.
  @wip
  Scenario: The routine will not start against a repo that cannot commit its own content
    Given the project still carries the superseded blanket ctxloom ignore rule
    When I run "ctxloom doctor"
    Then the checks name the ignore rule and the command that retires it

  # doctor check 5 (the foreign-worktree report). The population ctxloom did
  # NOT create: report-only, always, by design. The report has to carry enough
  # for a human to act — merged-ness, dirty state, and the exact commands —
  # because ctxloom deliberately will not run them.
  #
  # UNTAG WHEN: doctor reports long-lived worktrees outside the sessions root
  # with their merge and dirty state and the manual commands. Expected RED:
  # no such check exists.
  @wip
  Scenario: Worktrees ctxloom did not create are reported, never touched
    Given a long-lived worktree "stale-feature" of her own, outside the sessions root, with unmerged work
    When I run "ctxloom doctor"
    Then the checks name the foreign worktree, that it is unmerged and dirty, and the exact commands to remove it

  # doctor check 4 (B13, the harp-directory durability contract). Agent-authored
  # artifacts land at the harp directory's TOP LEVEL — neither `persist/`
  # (mounted into containers) nor `ephemeral/` (rightly excluded; it holds the
  # scratch worktrees). An unclassified middle with no declared durability, and
  # the plan-stamping convention writes directly into it. A containerized agent
  # writing a design note into its own session directory writes into
  # container-ephemeral space and loses it on exit.
  #
  # UNTAG WHEN: doctor warns about authored files in the unclassified top level
  # and names the persistent home they belong in. Expected RED: verified defect,
  # no check exists.
  @wip
  Scenario: Authored artifacts in the unclassified middle of a harp directory are flagged
    Given a finished session "amber-quiet-heron" carrying design notes nobody filed
    When I run "ctxloom doctor"
    Then the checks warn that the design notes sit in the harp directory's unclassified top level

  # ---- Step 2: worktrees, two populations, two verbs ---------------------
  # The inspect half. Read-only by default on every leaf, so the first thing
  # this leaf ever does is show its work.
  #
  # `ctxloom session worktrees` lists ctxloom-owned scratch trees with harp,
  # owner pid and verdict — isolation.ClassifyOrphanedWorktrees, split out of
  # isolation.ReapOrphanedWorktrees so a caller can show its work before
  # anything is removed.
  Scenario: The scratch worktrees are listed before anything is removed
    Given a finished session "amber-quiet-heron" whose work is already distilled
    And session "amber-quiet-heron" left a clean scratch worktree whose owning process is dead
    And session "amber-quiet-heron" left a scratch worktree holding uncommitted work
    When I run "ctxloom session worktrees"
    Then the report names each scratch worktree with its harp, its owner and its verdict

  # THE SAFETY SCENARIO, and the one that must never be weakened. Four trees,
  # four different verdicts, one invocation. Only the clean tree with a
  # provably-dead owner may go. Uncommitted work is spared IN PLACE — not
  # stashed, not branched, not "recoverable from reflog": still sitting where
  # its author left it. A tree whose owner cannot be proven dead is treated
  # exactly like one whose owner is alive, because "I cannot tell" and "yes,
  # someone is using this" have the same correct answer.
  #
  # This project has lost work to a force-removed worktree before. That is why
  # the assertion reads the WIP file's own bytes off disk rather than checking
  # that a directory still exists.
  #
  # `session worktrees --reap --yes` drives isolation.ReapWorktrees over
  # exactly what isolation.ClassifyOrphanedWorktrees just classified, and
  # reports its outcome taxonomy (reaped/spared/skipped).
  Scenario: Reaping removes only what it can prove is safe, and says why it left the rest
    Given a finished session "amber-quiet-heron" whose work is already distilled
    And session "amber-quiet-heron" left a clean scratch worktree whose owning process is dead
    And session "amber-quiet-heron" left a scratch worktree holding uncommitted work
    And session "amber-quiet-heron" left a scratch worktree nothing can prove the owner of
    And session "amber-quiet-heron" left a scratch worktree whose owning process is still alive
    When I run "ctxloom session worktrees --reap --yes"
    Then only the clean, provably-orphaned worktree is gone from disk
    And the uncommitted work is still there, spared in place
    And the report says why each spared worktree was left alone

  # The population split, pinned so it cannot erode. Foreign worktrees are
  # invisible to the candidate finder by construction — they live outside the
  # sessions root — and the absence of a reaping verb for them is DELIBERATE.
  # This scenario exists so that a later "helpful" change that starts listing
  # them under a verb which also reaps has to fail a test first.
  #
  # `session worktrees` excludes foreign trees structurally:
  # isolation.findEphemeralWorktrees only ever scans under
  # ~/.ctxloom/sessions/, so this population is never even candidate-listed.
  Scenario: Her own long-lived worktrees are not this verb's business
    Given a finished session "amber-quiet-heron" whose work is already distilled
    And session "amber-quiet-heron" left a clean scratch worktree whose owning process is dead
    And a long-lived worktree "stale-feature" of her own, outside the sessions root, with unmerged work
    When I run "ctxloom session worktrees --reap --yes"
    Then her own long-lived worktree is untouched and was never listed

  # ---- Step 3: lessons ---------------------------------------------------
  # The flywheel's missing leg (boundary B10: lessons die in essence.md). The
  # SAME verb as ordinary session distillation, a DIFFERENT skill: extraction
  # rather than compression. Because the lessons skill is bundle content, a
  # team ships its house extraction style the way it ships any other context —
  # ctxloom's own behaviour flowing through ctxloom's own supply chain.
  #
  # UNTAG WHEN: `session distill <harp> --skill <ref> --to-bundle <name>`
  # emits fragments into the named bundle. Expected RED: `session distill` is
  # ExactArgs(1) with its output hard-wired to essence.md; neither flag exists.
  @wip
  Scenario: A finished session's lessons become fragments a team can read
    Given a finished session "amber-quiet-heron" whose work is already distilled
    And Alice's house lessons-extraction skill is trusted content she can select
    When I run "ctxloom session distill amber-quiet-heron --skill house-style#skills/lessons --to-bundle team-lessons"
    Then the lessons land as fragments in the "team-lessons" bundle
    And that bundle is signed in the same breath

  # The signing half is not decoration. An edited-unsigned bundle is silently
  # withheld once a pin advances — J21's own red scenario is about nothing
  # else — so a lessons flow that ends unsigned would manufacture the exact
  # defect it exists to close, and would do it quietly, in a flow whose whole
  # purpose is preserving knowledge. Asserted separately from the write so a
  # signature step that silently no-ops cannot hide behind a successful write.
  #
  # UNTAG WHEN: the lessons write signs its output in the same apply.
  @wip
  Scenario: Lessons that ship unsigned would be withheld, so they are signed on the way out
    Given a finished session "amber-quiet-heron" whose work is already distilled
    And Alice's house lessons-extraction skill is trusted content she can select
    When I run "ctxloom session distill amber-quiet-heron --skill house-style#skills/lessons --to-bundle team-lessons"
    Then that bundle is signed in the same breath

  # THE SILENT-NO-OP GUARD, which this codebase needs more than most. A
  # distillation that extracts nothing and exits 0 with a success line is
  # indistinguishable from one that worked, and the bundle it "wrote" is empty.
  # An unremarkable session with no lessons in it is a perfectly ordinary
  # outcome — reporting it as a successful extraction is not.
  #
  # UNTAG WHEN: a lessons run producing zero fragments exits non-zero and says
  # so. Expected RED: the surface does not exist.
  @wip
  Scenario: A session with no lessons in it fails loudly rather than writing an empty bundle
    Given a finished session "amber-quiet-heron" that was never distilled
    And Alice's house lessons-extraction skill is trusted content she can select
    When I run "ctxloom session distill amber-quiet-heron --skill house-style#skills/lessons --to-bundle team-lessons"
    Then ctxloom reports that it extracted nothing, and does not call that success
    And nothing was written into the "team-lessons" bundle

  # A VERIFIED SILENT DEGRADE, specified out of existence. A distill skill is a
  # context-exfiltration and context-poisoning primitive of the first order: it
  # reads whole session transcripts — the most sensitive artifact ctxloom
  # holds — and decides what gets written back into checked-in, signed,
  # team-distributed fragments. It is a strictly higher-value target than an
  # ordinary fragment, which is exactly why it rides the same per-item,
  # hash-bound trust choke as any other skill.
  #
  # The consumer half is what is broken today: cli.loadDistillPrompt swallows
  # EVERY GetCommand error including ErrCommandWithheld and silently falls back
  # to the embedded prompt. So a trust decision silently changes which prompt
  # runs, and the user is never told the skill they chose was not the one used.
  # Withheld must FAIL LOUD, with `--degraded` as the only way past it.
  #
  # UNTAG WHEN: selecting a withheld distill skill refuses and names the review
  # command. Expected RED.
  @wip
  Scenario: A withheld distill skill stops the run instead of quietly swapping itself out
    Given a finished session "amber-quiet-heron" whose work is already distilled
    When I run "ctxloom session distill amber-quiet-heron --skill untrusted-remote#skills/lessons --to-bundle team-lessons"
    Then ctxloom refuses rather than falling back to its built-in prompt
    And nothing was written into the "team-lessons" bundle

  # ---- Step 4: retention -------------------------------------------------
  # Read-only default, the confirmation line's first rule. Inspect always. The
  # payload assertion is that the report DESTROYED NOTHING — a "report" that
  # deletes as a side effect is the worst possible version of this leaf.
  #
  # UNTAG WHEN: `ctxloom session purge <harp>` reports without destroying.
  # Expected RED: no purge, gc, prune, retention or forget verb exists anywhere
  # in the CLI (boundary B11).
  @wip
  Scenario: Purge shows its work before it destroys anything
    Given a finished session "amber-quiet-heron" whose work is already distilled
    And a finished session "brisk-copper-moth" that was never distilled
    When I run "ctxloom session purge amber-quiet-heron"
    Then the report lists what would be destroyed and what would be kept
    And every byte of every session is still on disk

  # THE THREE CONTENT CLASSES, each treated differently, asserted separately.
  # Machine-written bulk goes. Derived essence and the index entry stay, marked
  # purged — a session that vanishes from the index is indistinguishable from
  # one that never existed. Human-authored artifacts are never negotiable, and
  # are NAMED in the report rather than silently skipped, because a file kept
  # but never mentioned is a file nobody will ever file.
  #
  # UNTAG WHEN: purge implements the three-class split. Expected RED.
  @wip
  Scenario: Purge destroys the bulk, keeps the meaning, and never eats authored work
    Given a finished session "amber-quiet-heron" carrying design notes nobody filed
    When I run "ctxloom session purge amber-quiet-heron --yes"
    Then the machine-written bulk of "amber-quiet-heron" is gone
    And its distilled essence and its index entry survive
    And her unfiled design notes are still there, and were named in the report

  # Maximal friction on the irreversible case. `--everything` is the
  # offboarding, subpoena, leaked-secret day — and destroying the only record
  # of a never-summarized session earns its own flag on top of the typed
  # confirmation. For a structured run the canonical transcript is the ONLY
  # copy; nothing promises `session backfill` can re-derive it.
  #
  # UNTAG WHEN: `--everything` refuses undistilled sessions without
  # `--undistilled`. Expected RED.
  @wip
  Scenario: Forgetting a session nobody ever summarised takes an extra deliberate flag
    Given a finished session "brisk-copper-moth" that was never distilled
    When I run "ctxloom session purge brisk-copper-moth --everything --yes"
    Then ctxloom refuses, naming the session that was never distilled
    And every byte of every session is still on disk

  # The hard boundary between the two destroyers. A purge never runs git
  # operations — reaping is `session worktrees --reap`'s job, under safety
  # semantics a purge does not implement. A purge that recursively removed a
  # harp directory would take the scratch worktrees with it, including any
  # holding work, without ever consulting an owner marker.
  #
  # UNTAG WHEN: purge leaves scratch worktrees registered and on disk.
  # Expected RED: the leaf does not exist.
  @wip
  Scenario: Purging a session does not reap the worktrees living inside it
    Given a finished session "amber-quiet-heron" whose work is already distilled
    And session "amber-quiet-heron" left a scratch worktree holding uncommitted work
    When I run "ctxloom session purge amber-quiet-heron --yes"
    Then the scratch worktrees are still registered with git, untouched by the purge

  # ---- The routine itself ------------------------------------------------
  # The one-thing-you-run affordance, recovered through ctxloom's own
  # mechanism rather than a new verb: a first-party signed bundle command
  # reached by `run -r`, with the shipped `check-triggers` command as the
  # existing precedent for a command orchestrating CLI and MCP calls in order.
  # Honest cost, stated where it belongs: `run -r` is LLM-driven, so the
  # routine is agentic rather than deterministic.
  #
  # UNTAG WHEN: a first-party `cleanup` command ships and resolves. Expected
  # RED. Note this asserts RESOLUTION, not a full agentic run — the four leaves
  # it drives have their own scenarios above, and re-driving them through an
  # LLM here would test the model rather than the product.
  @wip
  Scenario: The close-out is itself a piece of signed content she can read and edit
    When I run "ctxloom run -r cleanup -n"
    Then ctxloom resolves a shipped, signed cleanup routine
