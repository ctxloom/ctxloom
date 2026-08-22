# Task Tagging Standard — the ctxloom task log

**Status:** proposed standard. **Scope:** every task in the taskloom log.

---

## TL;DR — five levels, and security is one of the outcomes, not a second scale

**Severity names the CONSEQUENCE, not the category.** A trust-gate escape and an
unrecoverable data loss are the same severity, because they cost the same. There
is no separate security scale to cross-reference.

| # | `triage:severity=` | Means | Blocks release |
|---|---|---|---|
| 1 | `critical` | Data loss or corruption, **or** a trust/isolation boundary breached | yes |
| 2 | `serious` | Major functionality broken with no workaround — **including succeeding without doing the thing** | yes |
| 3 | `normal` | Wrong, but a workaround exists; or documentation asserting behaviour the code lacks | no |
| 4 | `minor` | Low or no user impact — cosmetic, inconsistency, tidy-up | no |
| 5 | `wishlist` | Does not exist yet — new capability, or a refactor with no live defect | no |

One severity per task. It is a property of the CONSEQUENCE if the task is not
done, never of how interesting or how large the work is.

---

## Severity is not priority, and not kind

Three independent axes, and conflating them is what makes a log unqueryable:

- **`triage:severity=`** — how bad is it if we ship without this? Derived from
  consequence.
- **`triage:priority=`** — what order do we work in? A `minor` bug can outrank a
  `serious` one when it blocks someone today.
- **`triage:kind=`** — `defect` / `chore` / `capability`. What SHAPE of work it
  is. A `wishlist` severity is usually `kind=capability`, but a large chore with
  no live defect is `wishlist` too.

A task carrying only a severity is still findable. A task carrying only a
priority is not, because priority decays and severity does not.

---

## The levels, in detail

### 1 — `critical`

The consequence is unrecoverable or crosses a boundary that is supposed to hold.

- Data destroyed or corrupted with no recovery path.
- A trust gate admits content it should withhold, or a rejection is dropped so
  the item is re-admitted on a publisher signature.
- A credential becomes readable where it was not, or a workspace confinement is
  escaped.

Security lands here **by consequence**. Ask what an attacker or an accident
GETS, not whether the word "security" appears.

### 2 — `serious`

The thing is unusable, or — the shape this project actually produces — it
reports success and did not do the work.

- Exit 0, a success message, and zero bytes written.
- A gate that is green while measuring nothing: a test that passes with the
  subject neutered, coverage credited to code that never ran.
- Major functionality broken with no satisfactory workaround.

**The silent no-op is `serious`, not `normal`**, and this is the one place this
standard departs from a plain reading of its sources. A loud failure is a
smaller problem than a quiet one: the loud one is discovered by whoever hit it,
while the quiet one is discovered by nobody and is indistinguishable from
working. A green gate that measures nothing is the same defect wearing a
lab coat.

### 3 — `normal`

Wrong, and a user could act on the wrongness, but there is a way around it.

- Incorrect output, a crash, or a race, where a workaround exists.
- **Documentation, help text or a comment asserting behaviour the code does not
  have.** This sits here rather than lower because a wrong doc is what the next
  reader trusts INSTEAD of re-deriving the thing — silence would at least have
  made them look.

### 4 — `minor`

Real, but nobody is materially worse off: cosmetic issues, naming
inconsistencies, tidy-ups, dead code with no live consequence.

### 5 — `wishlist`

The capability does not exist yet. New commands and surfaces, extensions,
speculative work, and refactors undertaken to enable future work rather than to
fix a present defect.

A refactor whose absence causes no wrong behaviour today is `wishlist`, however
much it is wanted.

---

## Where this comes from, and where it deliberately diverges

Levels 1–4 are [Mozilla's defect severity
ladder](https://firefox-source-docs.mozilla.org/bug-mgmt/guides/severity.html)
(S1 catastrophic / S2 serious / S3 normal / S4 trivial), which already puts data
loss at the top. Level 5 is Debian's `wishlist`, the one tier that survives in
almost every tracker.

**The divergence is folding security in.** Mozilla, Chromium and Debian all keep
a separate security scale — Chromium's `S4` even means "lack of a security
severity". They separate it because security triage has a different assessor, a
different SLA, and an embargo and disclosure workflow.

ctxloom has none of those: no external reporters, no embargo, one person
triaging. A second scale would be a cross-reference nobody maintains, and a
finding filed on the wrong one is a finding nobody sees. So security is folded
in by consequence — and it can legitimately land at `normal`, since a doc
falsely claiming a security property is a false claim, not a breach.

If ctxloom ever takes external vulnerability reports, revisit this: the reason
the big projects split the scales is disclosure, and that arrives with the
first outside reporter.

---

## Assigning it

**Read the code, not the task text.** A severity taken from a task's own prose
inherits that prose's staleness. Tasks in this log have described functions that
were deleted commits earlier, and a task tagged as a security escape on the
strength of its own description turned out to name a parser that no longer
exists.

The question to ask is always the same: **if we shipped without this, what would
actually happen?** Then find the tier that names that consequence.

---

## Locating the work — `touches:` and `sig:`

Severity says how bad it is. These say WHERE it is, and they answer two
different questions that need different granularity.

| Tag | Form | Answers |
|---|---|---|
| `touches:` | repo-relative **file** path | Can these two tasks run in parallel? |
| `sig:` | `package.Symbol` / `Type.Method` | What is open against the thing I am about to change? |

Both are repeatable; a task carries as many as it needs.

### `touches:` — files this task will EDIT

```
touches:"internal/agentcoord/coord/children.go"
```

**The quotes are REQUIRED.** tagma's tag grammar reserves `/`, so an unquoted
path is refused at write time — loudly, naming the fix, but refused. The same
applies to any `sig:` value containing a reserved character.

**"Will edit", not "concerns".** A task that only READS a file does not conflict
with one that writes it, and conflict prediction is the entire point. Two agents
editing one file collide in git no matter which symbols each of them touched —
which is why this is file-granular and not package-granular.

It has to be finer than `area:` to earn its place. `area:bus`, `area:config` and
their peers are already effectively package buckets, and every active task
carries exactly one. A package-level `touches:` would just be `area:` spelled
twice.

**Diffuse tasks carry only their two or three PRIMARY files.** A change that
rewrites 76 call sites gets no useful signal from 76 tags; it gets noise that
makes every query look like a collision. Where the work is genuinely spread,
tag the files that must be edited by hand and let `area:` carry the rest.

### `sig:` — the symbols whose contract changes

```
sig:coord.Coordinator.terminateRun
sig:remote.ParseRepoURL
sig:"justfile.test-mutation-cucumber"
```

**Not every addressable unit is a Go symbol.** A justfile recipe, a shell
function, or a named acceptance scenario is just as greppable and just as worth
naming. The form is `<file-or-package>.<unit>`; quote it if it carries a
reserved character. A task located in a justfile with no `sig:` at all has lost
the half of its address that survives a file move.

Name the function, method or type — never a line number. **A stale symbol fails
LOUDLY**: the moment anyone greps for it and finds nothing, they know the task
has drifted. A stale line number silently points at unrelated code and is
believed. That difference is why line numbers are banned from task bodies as
well as from these tags.

`sig:` is also what survives a file move, so the two tags degrade differently
and on purpose: rename a file and `touches:` goes quietly stale while `sig:`
still finds the work.

### Using them together

Before dispatching parallel work, intersect the `touches:` sets. A non-empty
intersection means those tasks take turns or share a worktree — it is not a
reason to skip either, only a reason not to run them at once.

This is not hypothetical. Two tasks in this log both edit
`internal/agentcoord/coord/children.go`, and with nothing recording that, the
collision had to be written into the task bodies as prose. Prose does not
survive a query.

---

## Four rulings the first tagging pass needed

These came out of applying the standard to a real batch. Each is a case where
two rules in this document could both be followed and gave different answers.

### A release-blocking `wishlist` is LEGAL

A task can be `triage:severity=wishlist` and `triage:blocks-release=...` at the
same time, and that is not a contradiction. Severity is the consequence if the
thing is missing; `blocks-release` is the promise we made about this release.
A capability we committed to ship is a `wishlist` severity we have decided to
be blocked by.

Do not inflate a capability's severity to express that it is wanted. Use the
release tag — that is the axis that carries "wanted", and it is the one that
gets cleared when the release ships.

### A ruling is preserved; the WORK is cut down

"Record only what is still to do" applies to WORK. It does not apply to a HUMAN
RULING, which is a decision, not a task step — and decisions exist nowhere else
once the plan file is gone.

When a ruling has partly landed: keep the live clauses verbatim, and move the
landed ones under a short CLOSED heading that says what landed and on what
evidence. Deleting a ruling clause because its work is done destroys the record
of the decision and invites someone to re-decide it the other way.

### A vacuous test is `serious`; a missing test is not

They are different faults and they rate differently:

- A test that PASSES while proving nothing is `serious`. It actively reports
  coverage that does not exist, so it is worse than no test — the shape this
  standard already rates as "succeeds without doing the thing".
- A test that is ABSENT is `minor`, because the gap is at least honest, and
  rises only if its absence is hiding a live defect.

"The defect is fixed, only the regression test is missing" is therefore
`minor`, not `serious`.

### Severity is rated on the REMAINDER

When part of a task has landed, rate what is left, not what the task originally
described. A `critical` defect that has been fixed but for a missing test is a
`minor` task, and its body should already have been cut down to match.
