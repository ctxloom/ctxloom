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

  The one thing she runs at the end is `ctxloom session sweep`, and it is
  deterministic: a fixed table decides every ended session of the project, and
  each action goes through the same leaf, with the same refusals, that this
  file specifies one at a time — worktrees, then reclaim, then retention. It
  removes what it can prove is safe and reports the rest; the same sessions
  give the same report. There is deliberately no top-level `cleanup` verb; the
  sweep lives under the noun it tidies.

  # COMMAND SURFACES THIS FILE COVERS: `ctxloom doctor`, `session worktrees`
  # and its `purge` leaf, `session purge` and `session transcript purge`, and
  # `session sweep`.
  #
  # NOTE ON WHAT THE FIXTURES BUILD. A close-out flow is defined almost
  # entirely by what it REFUSES to do, and a refusal cannot be tested against a
  # fixture with nothing to refuse. So these scenarios build the real debris:
  # genuine `git worktree add` checkouts inside a harp's own work/
  # directory beside real session liveness locks, foreign long-lived worktrees
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
  # destroying a transcript its retention keeps, no touching vendor stores.
  #
  # NOTE ON TAGS. Every scenario passes and says what closed it rather than
  # being left silent. A scenario added here as a wish carries @wip and states
  # its own untag condition — whether anything here is still a wish is the
  # first thing a reader needs, and a @wip tag is the only honest way to say
  # so.

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

  # doctor check 4 (B13, the session-dir durability contract). The session dir
  # holds machine state only, classified row by row; readable outputs belong in
  # the session's output dir. An authored file at the session dir's TOP LEVEL
  # is classified by no row: a containerized agent writing a design note there
  # writes into container-ephemeral space and loses it on exit.
  #
  # Closed: DOCTOR-CHECK-HARP-DURABILITY-s9 warns about authored files in the
  # unclassified top level and names the output dir they belong in. The walk is
  # two-level and guards IsDir() on the OUTER iteration: the sessions root holds
  # lock files beside the harp directories, which are never inside any one
  # harp dir at all.
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
    Given a finished session "amber-quiet-heron" whose transcript retention has passed
    And session "amber-quiet-heron" left a clean scratch worktree
    And session "amber-quiet-heron" left a scratch worktree holding uncommitted work
    When I run "ctxloom session worktrees"
    Then the report names each scratch worktree with its harp, its owner and its verdict

  # THE SAFETY SCENARIO, and the one that must never be weakened. Only the
  # clean tree of a session that can be PROVEN ended may go. Uncommitted work
  # is spared IN PLACE — not stashed, not branched, not "recoverable from
  # reflog": still sitting where its author left it.
  #
  # This project has lost work to a force-removed worktree before. That is why
  # the assertion reads the WIP file's own bytes off disk rather than checking
  # that a directory still exists.
  #
  # ONE SESSION PER OWNER-STATE, and the split is forced by the product rather
  # than chosen: liveness is the SESSION's (an exclusive lock held for its
  # lifetime — internal/shared/sessionlock), so one harp can no longer hold a
  # dead-owned and a live-owned worktree at once. Clean-vs-dirty still varies
  # WITHIN the ended session, because that is a property of the checkout. The
  # running and unprovable populations keep their own sessions, and this
  # scenario proves a harp-scoped purge does not stray outside its harp; the
  # two scenarios below pin what happens when they are the TARGET.
  #
  # `session worktrees purge <harp> --yes` drives isolation.ReapWorktrees over
  # exactly what isolation.ClassifyOrphanedWorktrees just classified, and
  # reports its outcome taxonomy (reaped/spared/skipped).
  Scenario: Purging removes only what it can prove is safe, and says why it left the rest
    Given a finished session "amber-quiet-heron" whose transcript retention has passed
    And session "amber-quiet-heron" left a clean scratch worktree
    And session "amber-quiet-heron" left a scratch worktree holding uncommitted work
    And a session "teal-running-wren" that is still running
    And session "teal-running-wren" left a scratch worktree of its own
    And a session "slate-unproven-vole" nothing can prove the liveness of
    And session "slate-unproven-vole" left a scratch worktree of its own
    When I run "ctxloom session worktrees purge amber-quiet-heron --yes"
    Then only the clean, provably-orphaned worktree is gone from disk
    And the uncommitted work is still there, spared in place
    And the report says why each spared worktree was left alone

  # THE LIVE-SESSION ARM, and the incident this whole design guards against:
  # reaping out from under a session that is still running. The tree here is
  # genuinely CLEAN — every other signal would call it reapable — and it
  # survives purely because its owning session still holds its lock.
  Scenario: Purging a session that is still running removes nothing
    Given a session "teal-running-wren" that is still running
    And session "teal-running-wren" left a clean scratch worktree
    When I run "ctxloom session worktrees purge teal-running-wren --yes"
    Then no worktree of "teal-running-wren" is removed, and ctxloom says it could prove nothing safe

  # THE UNPROVABLE ARM. A session with no lock file at all cannot be proven
  # ended, and "I cannot tell" and "yes, someone is using this" have the same
  # correct answer. A MISSING lock must never read as a free one — that is the
  # single inversion that would turn this sweep into data loss.
  Scenario: Purging a session nothing can prove the liveness of removes nothing
    Given a session "slate-unproven-vole" nothing can prove the liveness of
    And session "slate-unproven-vole" left a clean scratch worktree
    When I run "ctxloom session worktrees purge slate-unproven-vole --yes"
    Then no worktree of "slate-unproven-vole" is removed, and ctxloom says it could prove nothing safe

  # The population split, pinned so it cannot erode. Foreign worktrees are
  # invisible to the candidate finder by construction — they live outside the
  # sessions root — and the absence of a reaping verb for them is DELIBERATE.
  # This scenario exists so that a later "helpful" change that starts listing
  # them under a verb which also reaps has to fail a test first.
  #
  # `session worktrees` excludes foreign trees structurally:
  # isolation.findWorkWorktrees only ever scans under
  # ~/.ctxloom/sessions/, so this population is never even candidate-listed.
  Scenario: Her own long-lived worktrees are not this verb's business
    Given a finished session "amber-quiet-heron" whose transcript retention has passed
    And session "amber-quiet-heron" left a clean scratch worktree
    And a long-lived worktree "stale-feature" of her own, outside the sessions root, with unmerged work
    When I run "ctxloom session worktrees purge amber-quiet-heron --yes"
    Then her own long-lived worktree is untouched and was never listed

  # ---- Step 4: retention -------------------------------------------------
  # Read-only default, the confirmation line's first rule. Inspect always. The
  # payload assertion is that the report DESTROYED NOTHING — a "report" that
  # deletes as a side effect is the worst possible version of this leaf.
  #
  # `ctxloom session purge <harp>` reports without destroying: with no `--yes`,
  # nothing on disk or in the session index changes, on a TTY or not.
  Scenario: Purge shows its work before it destroys anything
    Given a finished session "amber-quiet-heron" whose transcript retention has passed
    And a session "brisk-copper-moth" that finished an hour ago
    When I run "ctxloom session purge amber-quiet-heron"
    Then the report lists what would be destroyed and what would be kept
    And every byte of every session is still on disk

  # WHAT "EMPTY" MEANS, and the one thing it may never take. Emptying a session
  # sweeps the machine-written bulk ctxloom wrote into it — the recorded
  # conversation. That is the whole point of the verb, and it sets the
  # deadline this whole flywheel runs against: a lesson still sitting in a
  # session directory when retention reaches it is a lesson lost.
  #
  # Two things survive regardless. The index entry stays, marked purged, because
  # a session that vanishes from the index is indistinguishable from one that
  # never existed. And human-authored artifacts are never negotiable — they are
  # NAMED in the report rather than silently skipped, because a file kept but
  # never mentioned is a file nobody will ever file.
  #
  # The session's output dir — its plans, and the summary an older ctxloom
  # left there — is the human's, and no destroyer reaches it. What this row
  # owns is the authored file planted in a real harp directory, which a wrong
  # implementation would really have destroyed.
  Scenario: Emptying a session takes everything ctxloom wrote, and nothing Alice did
    Given a finished session "amber-quiet-heron" carrying design notes nobody filed
    When I run "ctxloom session purge amber-quiet-heron --yes"
    Then the machine-written bulk of "amber-quiet-heron" is gone
    And its output dir is untouched, and its index entry survives
    And her unfiled design notes are still there, and were named in the report

  # Maximal friction on the irreversible case. Engines compact their own
  # sessions, so a session's transcript is its only record — for a structured
  # run the canonical transcript is the ONLY copy, and nothing can re-derive
  # it once the engine's own store has rotated. So a transcript is kept until
  # the session ended more than session_transcript_retention ago (30 days by
  # default), and the refusal says that is why. No flag overrides it.
  Scenario: Emptying a session that ended within its retention refuses, and says why
    Given a session "brisk-copper-moth" that finished an hour ago
    When I run "ctxloom session purge brisk-copper-moth --yes"
    Then ctxloom refuses, naming the session and that its transcript is within its retention
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
    Given a finished session "amber-quiet-heron" whose transcript retention has passed
    And session "amber-quiet-heron" left a scratch worktree holding uncommitted work
    When I run "ctxloom session purge amber-quiet-heron --yes"
    Then the uncommitted work is still there, spared in place
    And the scratch worktree is still registered with git

  # ---- The sweep ----------------------------------------------------------
  # One command ties the legs together, and nothing in it is decided by a
  # model: `session sweep` turns each session's facts into rows by a fixed
  # table (operations.DecideSweep) and acts only through the leaves above,
  # each of which re-checks the session's lock under its own hold.
  #
  # Read-only default, the confirmation line's first rule: the report names
  # what it would do to each session, and destroys nothing.
  #
  # Closed: `ctxloom session sweep`, reporting without --yes.
  Scenario: The sweep reports every session's fate before it changes anything
    Given a finished session "amber-quiet-heron" whose transcript retention has passed
    And a finished session "brisk-copper-moth" whose transcript retention has passed
    And every session has been idle for 120 days
    When I run "ctxloom session sweep --purge-older-than 90d"
    Then the sweep report names "amber-quiet-heron" and "brisk-copper-moth"
    And every byte of every session is still on disk

  # THE SWEEP INHERITS EVERY REFUSAL ABOVE, over a mixed fixture where each
  # refusal has something real to refuse. A running session and one whose
  # liveness nothing can prove are skipped whole. A scratch worktree holding
  # uncommitted work is spared IN PLACE — read back by its own bytes — while
  # its clean sibling goes, and its session is spared from purge. A session
  # past the purge age whose transcript retention has passed has its
  # transcripts emptied — its output dir (plans, any summary an older ctxloom
  # left) is the human's and a sweep never takes it; a session marked keep is
  # not, and the report names the command that releases it. An internal
  # one-shot is nobody's only record of anything, so it is emptied at any
  # transcript age.
  #
  # Closed: `ctxloom session sweep --yes`.
  Scenario: The sweep removes only what it can prove is safe, and says why it left the rest
    Given a finished session "amber-quiet-heron" whose transcript retention has passed
    And a finished session "brisk-copper-moth" marked keep
    And an internal one-shot session "gray-oneshot-wisp" that finished an hour ago
    And a finished session "olive-dirty-finch" whose transcript retention has passed
    And session "olive-dirty-finch" left a clean scratch worktree
    And session "olive-dirty-finch" left a scratch worktree holding uncommitted work
    And a session "teal-running-wren" that is still running
    And session "teal-running-wren" left a scratch worktree of its own
    And a session "slate-unproven-vole" nothing can prove the liveness of
    And session "slate-unproven-vole" left a scratch worktree of its own
    And every session has been idle for 120 days
    When I run "ctxloom session sweep --purge-older-than 90d --yes"
    Then the sweep skipped "teal-running-wren" and "slate-unproven-vole" without touching either
    And only the clean, provably-orphaned worktree is gone from disk
    And the uncommitted work is still there, spared in place
    And the report says why each spared worktree was left alone
    And the machine-written bulk of "amber-quiet-heron" is gone
    And the machine-written bulk of "gray-oneshot-wisp" is gone
    And the sweep kept the transcript of "brisk-copper-moth" and named "ctxloom session keep brisk-copper-moth --undo"
    And the sweep kept the transcript of "olive-dirty-finch" and named "ctxloom-wt-wip"
