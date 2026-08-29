---
name: closeout
description: Close out a turn that changed something — verify against the real gate, kill a mutation for every test written or changed, fix falsified comments, leave the task log true, and fix or file what was found. Use when finishing a turn that touched code, tests, docs or the task log; when asked to "close out", "wrap up", "finish this properly"; and at the end of a unit of work. Reports honestly, including "not done" where that is the truth.
---

# closeout

The close-out contract for a turn that CHANGED something.

**INVOKED DELIBERATELY — by a human, or by you when a unit of work ends. NOTHING
FIRES IT AUTOMATICALLY, and that is a decision rather than an omission:** it was
once wired to a per-turn hook and the checklist MASSIVELY POLLUTED CONTEXT,
arriving on every turn whether or not it was wanted. Firing it costs a chunk of
the window, so it is spent when the work is done, not on a timer.

Do not re-wire it to a hook. A later reader finding `ctxloom hook turn-changed`
and concluding the automation is "missing" is re-deriving exactly the thing that
was removed on purpose — that has already happened once.

**This file is the single source of the checklist.** Anything that points at
close-out points HERE rather than restating it, because a rule hand-copied into
two places is the expensive kind of drift: one copy gets retired and the other
keeps asserting it, with nothing to say which is current.

## The checklist

1. **VERIFY, against the real gate.** → `turn-gates`
   Build from THIS tree and drive that binary. Read exit codes, and know the
   three that lie: `gofmt -l` lists files and exits 0, `go test -run` matching
   nothing exits 0 having run nothing, and a pipeline reports the LAST
   command's status.
2. **KILL A MUTATION for every test you wrote or changed, and REPORT every one,
   including survivors.** → `green-is-not-passing`, `turn-gates`
   A survivor is the most valuable result there is: it names an untested claim
   you just shipped. Scope the mutation to the production code your new or
   changed tests NAME. Changed nothing under test? Say so; do not invent a run.
3. **FIX THE DOCS AND COMMENTS YOU FALSIFIED.** → `unchecked-bindings`
   Nothing tests a comment, which is why this is on a checklist and not in CI.
   Cite by symbol; state the invariant, not the history.
4. **LEAVE THE TASK LOG TRUE.** → `close-the-turn`
   Close what landed, quoting ask against outcome. Cut a partly-satisfied task
   down to what REMAINS. Re-tag what you moved. BEFORE CLOSING, re-read it for
   anything still open — closing is destructive to queryability, so a live
   decision or deferral inside it must exist as its OWN row first.
5. **FIX WHAT YOU FOUND.** → `close-the-turn`
   Filing is the exception. Defer only for a human decision (tag it `human`,
   state the question) or a genuinely large load — and say which.

## How to report

Report what is now TRUE, not what you noticed. Lead with what landed, not with
what you observed. **Do not call the work done while a found item is still open
and unnamed** — name it, or it does not exist.

Say "not done" where that is the truth. A confident wrong claim costs the reader
their next hour; an honest gap costs a sentence.
