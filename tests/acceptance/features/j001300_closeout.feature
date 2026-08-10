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

  # NOTE ON SCOPE. `session worktrees`, `session purge` and doctor's checks
  # ship. The `cleanup` routine does not, and its scenario is its acceptance
  # definition. Ten of the eleven scenarios pass; the one tagged @wip is red,
  # and that is the deliverable rather than a defect in the file.
  #
  # COMMAND SURFACES THIS FILE COVERS: `ctxloom doctor`, `session worktrees`
  # and its `purge` leaf, `session purge` and `session transcript purge`, and
  # `ctxloom run -r`.
  #
  # The lessons leg — four scenarios specifying
  # `session distill --skill --to-bundle` — is gone; that surface is ruled out.
  # See the retired-section note where those scenarios stood.
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
  # exit-nonzero, never success); destruction confirms against evidence shown
  # in the same invocation; and the refusal list is hard-coded — no
  # force-remove, no dirty or unmerged or unowned trees, no live session, no
  # sweeping an undistilled session, no touching vendor stores.
  #
  # NOTE ON TAGS. One of the eleven scenarios is @wip, with its own untag
  # condition. The other ten pass today; each says what closed it rather than
  # being left silent. Keep this count honest — it is the first thing a reader
  # uses to decide how much of this file is still a wish.

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
  # Closed: DOCTOR-CHECK-GITIGNORE-f6 reports a superseded blanket `.ctxloom`
  # ignore rule and names `ctxloom manage gitignore install` as the fix.
  # gitignore.Ensure already performed the retirement when it ran; the gap was
  # that no doctor check REPORTED the posture beforehand — read-only, via the
  # new gitignore.SupersededBlanketLines export (RetireSupersededFile now
  # routes through it too, so there is one detector, not two that could
  # disagree).
  Scenario: The routine will not start against a repo that cannot commit its own content
    Given the project still carries the superseded blanket ctxloom ignore rule
    When I run "ctxloom doctor"
    Then the checks name the ignore rule and the command that retires it

  # doctor check 5 (the foreign-worktree report). The population ctxloom did
  # NOT create: report-only, always, by design. The report has to carry enough
  # for a human to act — merged-ness, dirty state, and the exact commands —
  # because ctxloom deliberately will not run them.
  #
  # Closed: DOCTOR-CHECK-FOREIGN-WORKTREES-r8 reports long-lived worktrees
  # outside the sessions root with their merge state (git.Git gained
  # MergedBranches — no merged-ness primitive existed anywhere before this),
  # their dirty state as measured AT CHECK TIME (never assumed — the fixture's
  # own tree is clean by the time doctor runs, having committed over its
  # planted WIP; a wrong implementation that echoed the fixture's setup step
  # back would print "dirty" here and be lying), and the manual commands —
  # `git worktree remove` then `git branch -d`, never `-D`.
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
  # Closed: DOCTOR-CHECK-HARP-DURABILITY-s9 warns about authored files in the
  # unclassified top level and names the persistent home they belong in. The
  # walk is two-level and guards IsDir() on the OUTER iteration: HomeSessionsDir
  # itself holds index.yaml beside the harp directories, and an exclusion list
  # aimed at the harp level alone (as first proposed) would never see that
  # file, since it is never inside any one harp dir at all.
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
  # `session worktrees purge <harp> --yes` drives isolation.ReapWorktrees over
  # exactly what isolation.ClassifyOrphanedWorktrees just classified, and
  # reports its outcome taxonomy (reaped/spared/skipped).
  Scenario: Purging removes only what it can prove is safe, and says why it left the rest
    Given a finished session "amber-quiet-heron" whose work is already distilled
    And session "amber-quiet-heron" left a clean scratch worktree whose owning process is dead
    And session "amber-quiet-heron" left a scratch worktree holding uncommitted work
    And session "amber-quiet-heron" left a scratch worktree nothing can prove the owner of
    And session "amber-quiet-heron" left a scratch worktree whose owning process is still alive
    When I run "ctxloom session worktrees purge amber-quiet-heron --yes"
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
    When I run "ctxloom session worktrees purge amber-quiet-heron --yes"
    Then her own long-lived worktree is untouched and was never listed

  # ---- Step 3: lessons — REMOVED 2026-08-08 ------------------------------
  # Four scenarios lived here specifying
  #   `ctxloom session distill <harp> --skill <ref> --to-bundle <name>`
  # as the lessons-extraction surface. That REQUIREMENT IS WITHDRAWN by human
  # ruling (2026-08-08): `--skill` and `--to-bundle` must not exist on
  # `session distill`.
  #
  # The reason is a scope boundary, not a spelling preference. `session distill`
  # exists to RECOVER LOCAL STATE — it compresses a session into an essence so
  # that session can be resumed. Its output is local and per-session. Extracting
  # lessons and publishing them into a shared, signed, team-distributed bundle
  # is a different concern with a different lifetime and a different audience,
  # and hanging it off this verb's flags would overload the one command whose
  # job has to stay small.
  #
  # Nothing is asserted here now, deliberately. Whether ctxloom should grow a
  # lessons-extraction capability AT ALL, and what surface would carry it, is an
  # open design question — see taskloom pretended-referee. If it is built, it
  # gets a fresh design and fresh scenarios rather than these, which were
  # written against a shape that will not exist.
  #
  # The behaviours those rows described are still worth keeping in mind if that
  # design happens: extraction must complete BEFORE any write (so "nothing was
  # written" is true by construction, not by luck); the output must be signed in
  # the same apply, because an edited-unsigned bundle is silently withheld once
  # a pin advances; a run extracting zero fragments must fail loudly rather than
  # report success over an empty bundle; and a withheld extraction skill must
  # refuse rather than silently fall back to a built-in prompt —
  # operations.GetSkill already returns errs.ErrSkillWithheld for that.

  # ---- Step 4: retention -------------------------------------------------
  # Read-only default, the confirmation line's first rule. Inspect always. The
  # payload assertion is that the report DESTROYED NOTHING — a "report" that
  # deletes as a side effect is the worst possible version of this leaf.
  #
  # `ctxloom session purge <harp>` reports without destroying: with no `--yes`,
  # nothing on disk or in the session index changes, on a TTY or not.
  Scenario: Purge shows its work before it destroys anything
    Given a finished session "amber-quiet-heron" whose work is already distilled
    And a finished session "brisk-copper-moth" that was never distilled
    When I run "ctxloom session purge amber-quiet-heron"
    Then the report lists what would be destroyed and what would be kept
    And every byte of every session is still on disk

  # WHAT "EMPTY" MEANS, and the one thing it may never take. Emptying a session
  # sweeps every population ctxloom wrote into it — the recorded conversation
  # AND the essence distilled from it. That is the whole point of the verb, and
  # it is exactly why this journey's lessons leg matters: a lesson that is still
  # sitting in a session directory on Monday is a lesson with a deadline.
  #
  # Two things survive regardless. The index entry stays, marked purged, because
  # a session that vanishes from the index is indistinguishable from one that
  # never existed. And human-authored artifacts are never negotiable — they are
  # NAMED in the report rather than silently skipped, because a file kept but
  # never mentioned is a file nobody will ever file.
  #
  # Alice's other option is on the leaf that understands one population:
  # `session transcript purge` drops the bulk and leaves the essence standing.
  # That leaf has its own coverage in the session spec; what this row owns is
  # the authored file planted in a real harp directory, which a wrong
  # implementation would really have destroyed.
  Scenario: Emptying a session takes everything ctxloom wrote, and nothing Alice did
    Given a finished session "amber-quiet-heron" carrying design notes nobody filed
    When I run "ctxloom session purge amber-quiet-heron --yes"
    Then the machine-written bulk of "amber-quiet-heron" is gone
    And its distilled essence goes with the bulk, and its index entry survives
    And her unfiled design notes are still there, and were named in the report

  # Maximal friction on the irreversible case. Emptying a session nobody ever
  # summarised destroys the only record of what happened — for a structured run
  # the canonical transcript is the ONLY copy, and nothing can re-derive it once
  # the engine's own store has rotated. So the sweep refuses and names the one
  # leaf that can do it deliberately.
  #
  # The refusal has to carry that leaf, not merely say no. Selection flags live
  # on the population that understands them: `--undistilled` means something
  # precise about a transcript and nothing at all about an essence or a
  # worktree, so the sweep has none to offer and sends her to the verb that
  # does.
  Scenario: Emptying a session nobody ever summarised refuses, and names the deliberate route
    Given a finished session "brisk-copper-moth" that was never distilled
    When I run "ctxloom session purge brisk-copper-moth --yes"
    Then ctxloom refuses, naming the session that was never distilled and the leaf that can destroy it
    And every byte of every session is still on disk

  # THE SWEEP INHERITS THE SAFETY RULES, it does not outrank them. Emptying a
  # session covers the scratch worktrees living inside it, and covering them
  # means reaching exactly the verdicts the worktree leaf reaches on its own: a
  # tree holding uncommitted work is spared IN PLACE — not stashed, not
  # branched, not "recoverable from reflog" — still sitting where its author
  # left it and still registered with git. A sweep that recursively removed a
  # harp directory instead would take that work with it without ever consulting
  # an owner marker.
  #
  # This project has lost work to a force-removed worktree before. That is why
  # the assertion reads the WIP file's own bytes off disk rather than checking
  # that a directory still exists.
  Scenario: Emptying a session spares the uncommitted work living inside it
    Given a finished session "amber-quiet-heron" whose work is already distilled
    And session "amber-quiet-heron" left a scratch worktree holding uncommitted work
    When I run "ctxloom session purge amber-quiet-heron --yes"
    Then the uncommitted work is still there, spared in place
    And the scratch worktree is still registered with git

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
