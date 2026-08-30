Feature: A linked worktree's tasks land in the primary checkout's store

  "Tasks aren't context": an agent working in a throwaway linked git
  worktree that finds something outside its own remit needs to file it
  somewhere the coordinator — running from the primary checkout — will
  actually see, or the finding dies with the worktree. internal/projectroot's
  TaskStoreRoot redirects a linked worktree's task-store identity to its
  primary checkout for exactly this reason, unless that worktree carries its
  own .ctxloom (an explicit `ctxloom init` there opts it out as a
  deliberately separate project). The resolution logic itself is already
  unit-tested; this journey drives the real `taskloom` binary as a separate
  OS process from inside a REAL `git worktree add` fixture to prove the
  redirect end to end — not just that the right path gets computed, but that
  a task filed from the worktree is actually WRITABLE and READABLE through
  the primary checkout, that no second store gets silently minted alongside
  the first, and that the opt-out is honored when a worktree deliberately
  wants a project of its own.

  Background:
    Given Alice has a git-backed taskloom project

  # LOCKED — the redirect itself: a task filed from a linked worktree must be
  # readable from the primary checkout, and — the critical payload assertion
  # — exactly one on-disk store file may exist afterward. A redirect that
  # silently did nothing would still pass "the task exists somewhere"; only
  # the exact-one-file count catches a second store being minted alongside
  # the first.
  Scenario: A task filed from a linked worktree lands in the primary checkout's store
    Given Alice adds a linked git worktree named "bob-wt"
    When Bob files a task "Investigate the flaky retry" from worktree "bob-wt"
    Then the primary checkout's taskloom lists a task "Investigate the flaky retry"
    And exactly 1 home file matches ".ctxloom/tasks/*.jsonl"

  # The explicit opt-out: a worktree that carries its own project IDENTITY —
  # a project-id marker, which only an explicit `ctxloom init` there leaves
  # behind — is a deliberately separate project, and the task-store redirect
  # must respect it rather than silently overriding it. A task filed there
  # stays invisible from the primary checkout, lands in a genuinely SECOND
  # store file alongside the primary's own, and the primary's own task stays
  # exactly where it was filed.
  Scenario: A worktree with its own project identity keeps a separate task store
    Given Alice files a task "Wire up the release checklist" from the primary checkout
    And Alice adds a linked git worktree named "carol-wt"
    And Carol's worktree "carol-wt" has its own project identity
    When Carol files a task "Refactor the widget cache" from worktree "carol-wt"
    Then the primary checkout's taskloom lists a task "Wire up the release checklist"
    And the primary checkout's taskloom does not list a task "Refactor the widget cache"
    And exactly 2 home files match ".ctxloom/tasks/*.jsonl"

  # The inverse, and the regression this journey exists to catch. A project's
  # .ctxloom is COMMITTED, so `git worktree add` alone materializes a full one
  # in every linked worktree — config and all, but never a project-id, which is
  # gitignored. Reading that checked-out directory as an opt-out made every
  # worktree of every config-committing project mint a brand-new empty project
  # in silence, and every task the primary checkout held simply vanished.
  Scenario: A worktree whose .ctxloom merely came from the checkout still shares the store
    Given Alice files a task "Wire up the release checklist" from the primary checkout
    And Alice adds a linked git worktree named "dave-wt"
    And Dave's worktree "dave-wt" has a checked-out .ctxloom with no project identity
    When Dave files a task "Refactor the widget cache" from worktree "dave-wt"
    Then the primary checkout's taskloom lists a task "Wire up the release checklist"
    And the primary checkout's taskloom lists a task "Refactor the widget cache"
    And exactly 1 home file matches ".ctxloom/tasks/*.jsonl"
