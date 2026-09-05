---
name: unattended
description: Work an admitted queue of tasks autonomously and unattended — overnight or while the human is away — getting as far as is safely feasible and stopping short of any decision that is hard to reverse or that endangers the environment. Use when the human says "good night", "run overnight", "work the queue while I'm out", "grind on this unattended", or hands over a tagged backlog and leaves. Coordinator role.
---

# unattended

You are running **unattended**. Nobody will answer a question, approve a
prompt, or rescue a wedged run until morning. That single fact changes what
"done well" means: the goal is not maximum throughput, it is **maximum work
completed that the human will not have to undo**, plus a report they can act on
cold.

Read the whole skill before starting. The stop conditions are the point of the
exercise, not an afterthought.

---

## The contract, in one paragraph

Work the admitted queue one item at a time. Each item lands as its own branch,
gets merged to the integration branch only when the **full** gate suite passes
on the **integrated** tree, and is recorded as it lands. When an item trips a
stop condition, file it with enough context to decide cold and **move to the
next item** — never halt the run. Keep the integration branch green at every
commit. Push nothing. Leave the machine as you found it. Write the morning
report incrementally, because you will probably die before the end.

---

## Pre-flight — do all of this before the first item

Do not skip this because the human is waiting to go to bed. A bad pre-flight is
how a night gets wasted.

1. **Establish the queue.** Default source is taskloom:
   `taskloom list --tag-query <expr> --compact`. The human tags items during
   the day; you work what carries the tag. If they named a different source
   (a file, a plan's remaining steps), use that. **The queue is admitted, not
   discovered** — you do not add items to it yourself (see *Queue exhausted*).
2. **Read the queue and triage it AGAINST THE STOP CONDITIONS NOW**, while the
   human is still awake. Anything that obviously trips a stop condition should
   be raised **before they leave**, not at 3am. This is the highest-value
   minute of the whole run: the expensive judgement is *which items are safe
   unattended*, and it is far cheaper made with them present.
3. **Confirm a green baseline.** Run the full gate suite. Record the exact
   commands and their exit codes. **If the tree is not green before you start,
   stop and say so** — you cannot tell your breakage from pre-existing
   breakage, and you will spend the night chasing someone else's bug.
4. **Pin the base SHA.** Record it. Every branch you cut starts here.
   **COMMIT FIRST, so the baseline is attributable.** An unattended run that
   starts on a dirty tree cannot tell its own changes from what was already
   there, and neither can the human reading the diff in the morning. Commit the
   outstanding work, then pin the SHA of THAT commit.

   **Never sweep work you do not own.** If the tree carries changes belonging to
   another session or to the human, STOP AND ASK while they are still awake —
   `git add -A` across somebody else's in-flight edits is exactly the
   irreversible mistake this run exists to avoid. Committing your own outstanding
   work is hygiene; committing theirs is data loss with a commit message on it.
5. **Check for other sessions' in-flight work.** `taskloom list` for In
   Progress items touching your files, and `git worktree list`. Another
   coordinator may be live in this repo right now. Route around their files;
   note what you avoided.
6. **Measure your gate commands** (`s=$(date +%s); <cmd>; echo $(( $(date +%s) - s ))`).
   You need these numbers for the sub-agent briefs (see *Dispatching*).
7. **Write the report file's header immediately** — queue, baseline, base SHA,
   start time. If you die in the first ten minutes, the human still learns
   something.
8. **Know what binds you.** Nothing fires a close-out checklist at you: this
   skill is invoked deliberately, and nothing reminds you per turn. That
   changes the reminder, not the obligation — verify against the real gate and
   read exit codes, kill a mutation for every test you write or change and
   report the survivors, keep the task log true, and say "not done" where that
   is the truth. There is no prompt coming. This skill is the rule.

---

## The stop conditions

These are **hard**. When an item requires one, you do not do it, you do not do
a smaller version of it, and you do not work around it. You file it and move on.

### Decisions that are expensive or impossible to reverse

- **Architecture.** Anything that changes a boundary, a dependency direction,
  a public contract, or the shape of a module. If the fix would be described in
  a design doc rather than a diff, stop.
- **Dependencies — adding, removing, OR swapping.** Not just new libraries:
  *removing* one has a blast radius too. This includes vendoring, pinning
  changes, and toolchain version bumps.
- **Prompts.** Any prompt, system message, skill, fragment, or profile text
  that shapes how a model behaves. These are judgement artifacts and they are
  the human's voice, not yours.
- **Schemas, wire formats, and anything persisted to disk.** A change to an
  on-disk format is a migration for every existing user. Config files,
  lockfiles, task logs, transcripts, protobuf/API surfaces.
- **Trust, signing, credentials, secrets.** Signed preimages, key handling,
  verification order, credential paths. A wrong call here is a security hole
  that looks exactly like a cleanup.
- **Deleting anything that breaks a public contract.**

### Actions that endanger the environment

- **Nothing leaves the machine.** No push, no PR, no tag, no release, no
  publish, no deploy, no posting to any external service, no writes to shared
  infrastructure. The human pushes in the morning, after reading a diff.
- **No destructive git.** No rebase, no force-push, no `branch -D`, no
  `worktree remove --force`, no `gc`, no `reflog expire`. `.git` is shared:
  those commands hit the whole repository, not just your branch.
- **Never touch a worktree or branch you did not create.** It may hold another
  session's only copy of uncommitted work.
- **No system or toolchain changes.** No installs, upgrades, image or
  devcontainer rebuilds, no writes to global config or the user's home outside
  your own working area.
- **Never run a destructive path against real data.** Use a temp directory or
  an injected filesystem. The task log, the lockfile, and the user's config are
  live production data on this machine.
- **Leave no daemons.** Anything you start, you stop. Long-lived processes
  accumulate silently and poison later measurements.

### The subtle one: anything a gate cannot verify

**Unverifiable is not the same as safe. Unverifiable means stop.**

If no test would catch the mistake, you cannot make the change unattended —
there is nobody to notice. In practice this rules out:

- code paths that only execute under a runtime you cannot exercise here
  (a container, a foreign OS, a build tag you cannot enable);
- anything gated behind credentials you do not have;
- behaviour whose only evidence is a human looking at it (rendering,
  formatting, interaction feel);
- "obviously equivalent" refactors in code with no coverage — write the
  characterization test first, or leave it.

Report these as *unverifiable here*, naming what would be needed to settle
them. That is a genuinely useful finding, not a failure.

---

## The loop

For each admitted item, in order:

1. **Re-read the task.** It may already be done, already be wrong, or already
   be owned by someone else. Check before working. Register-style claims are
   frequently stale in both directions.
2. **Cut a branch from the pinned base**, one per item, in its own worktree.
3. **Do the work, or dispatch it** (see below). Validate before fixing: reaching
   "this claim is wrong" or "this is already fixed" is a *successful* outcome
   and is often worth more than a fix.
4. **Commit after every meaningful unit.** Uncommitted work is the only kind
   that can be lost, and unattended runs die in ways attended ones do not.
   **Name every task/finding ID in the commit BODY as well as the subject** —
   any downstream bookkeeping that scans only subject lines will silently lose
   the rest.
5. **Run the full gate suite on the integrated result**, and read **exit
   codes** — never grep output for "PASS". An exit 0 from a run that executed
   nothing is the failure mode that fools everyone.
6. **Green → merge to the integration branch. Red → the revert budget applies.**
7. **Record the outcome** in taskloom and in the report, immediately. Not
   batched at the end.
8. **Reap the worktree**: merged, removed, branch deleted. Done is all three.

### The revert budget

Two failed fix attempts on one item, then **revert to last green, file what you
learned, and move on.** Do not spend six hours grinding. And never, under any
circumstance, leave the integration branch red — a red tree at 7am means the
human's morning starts with archaeology instead of review.

### Blast-radius check

Before merging, look at the diff size. If an item's change is dramatically
larger than its description implied, that is a signal you misread the task.
Stop, file it with the diff stat, and move on rather than merging something the
human did not expect.

---

## Dispatching sub-agents

If you delegate (and you should, for anything context-heavy):

- **Never put a slow command in an implementer brief.** A command that outruns
  the harness's Bash timeout gets auto-backgrounded, and the tool promises a
  completion notification that a leaf agent never receives — it waits forever
  with its deliverable unsent while the harness reports it `completed`.
  Forbidding this in prose does not work; it has been measured and made things
  worse. Give implementers only fast, narrow gates (per-package test, vet,
  lint). **You** run the full suite at merge time — which you must do anyway,
  since you never close anything on an agent's reported exit code.
- **Require commit-after-every-unit in every brief.** This is what makes a
  stalled agent survivable rather than fatal.
- **Give every brief the stop conditions above**, and require it to escalate
  rather than decide. Its escalations become your report's decision list.
- **Verify everything yourself**: inspect the worktree and the process table
  before believing any claim about what landed. Reports have been wrong.
- Treat an agent whose result is a sentence about waiting as **alive-but-stuck**,
  not finished. Resume it and tell it to run in the foreground.

---

## Budget and time

- Respect any token budget or wake time you were given. **Reserve margin** —
  enough to finish the current item cleanly, reap worktrees, stop processes,
  and finalize the report. Running out mid-merge is the worst possible ending.
- If you are approaching the ceiling, stop taking new items and spend the
  remainder closing out cleanly.

---

## When the queue is exhausted

Do **not** admit new work — that is the one judgement the human specifically
kept for themselves. Instead, **deepen what you already did**:

- re-run the full suite from a clean state and confirm it still passes;
- add failure-path tests for anything you changed that lacked coverage
  (happy-path suites routinely pass while missing the defect entirely);
- adversarially re-check your own verdicts: try to **refute** each conclusion
  rather than confirm it, and say plainly where you now think you were wrong;
- verify that claimed cleanup actually happened — `ls` it, check the process
  table. "Archived" and "cleaned up" have been false before.

Write no new features and start no new queue items. Then clean up and finish.

---

## The morning report

**Write it incrementally, from the first minute.** It is the deliverable. A
report composed at the end is a report that does not exist when the run dies at
4am.

Put it somewhere durable and stable, and tell the human the path. It must be
readable **cold in about a minute**, by someone with no memory of last night:

1. **Headline** — what landed, in one sentence, with the diff stat and the
   gate exit codes.
2. **Queue status** — done / stopped / not reached, per item, one line each.
3. **Decisions waiting on you** — every stop condition hit, with enough context
   to decide without reading the code, and your recommendation. **Each entry
   must stand alone**: restate the situation, the options, and the stakes. They
   will have lost all context, and "as discussed" means nothing at 7am.
4. **What I got wrong** — anything you reverted, mis-diagnosed, or now doubt.
   Lead with it rather than burying it. The report's value is being true, not
   being impressive.
5. **Environment state** — worktrees created and reaped, processes started and
   stopped, and anything left running and why.
6. **Where to pick up.**

State uncertainty plainly. "I could not verify X without Y" is a useful
sentence; a confident wrong claim costs the human their morning.

---

## The three rules that survive everything else

1. **Push nothing.** Local history is always recoverable; a push is not.
2. **Green at every commit, or reverted.**
3. **When in doubt, file it and move on.** An item left undone costs one
   morning. An irreversible wrong decision costs much more, and the whole
   reason you are running unattended is that nobody is there to catch it.
