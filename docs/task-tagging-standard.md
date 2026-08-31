# Task Tagging Standard — the ctxloom task log

**Status:** proposed standard. **Scope:** every task in the taskloom log.

---

## TL;DR — five levels, and security is one of the outcomes, not a second scale

**The level names the CONSEQUENCE, not the category.** A trust-gate escape and
an unrecoverable data loss are the same level, because they cost the same. There
is no separate security scale to cross-reference.

The tag is `triage:level=<1..5>`, and **the number is the tag** — the words below
are not values you can write, they are the names of the rungs. This page is the
legend that makes the integers readable; nothing else in the system carries it,
so a rung assigned without reading this table is a rung assigned by vibe.

| `triage:level=` | Rung | Means | Blocks release |
|---|---|---|---|
| `1` | critical | Data loss or corruption, **or** a trust/isolation boundary breached | yes |
| `2` | serious | Major functionality broken with no workaround — **including succeeding without doing the thing** | yes |
| `3` | normal | Wrong, but a workaround exists; or documentation asserting behaviour the code lacks | no |
| `4` | minor | Low or no user impact — cosmetic, inconsistency, tidy-up | no |
| `5` | wishlist | Does not exist yet — new capability, or a refactor with no live defect | no |

One level per task — the tag is declared `arity=scalar`, so a second one does
not sit alongside the first, it replaces it. The value is a property of the
CONSEQUENCE if the task is not done, never of how interesting or how large the
work is. `taskloom lint` rejects anything outside 1–5: the ladder has five rungs
and there is nothing either side of them.

---

## Why an integer, and not the word

Because a number can be compared, and that is the whole payoff:

```
taskloom list --tag-query 'triage:level<=2'      # everything that blocks a release on consequence
taskloom list --sort priority
```

A word-valued tag can only be matched exactly, so "show me everything at least
as bad as serious" becomes an enumeration that goes stale the moment a rung is
added. With an integer the query states the intent directly.

The ranking reads the same number. `priority_fn` scores a task at
`2**(3-level)` — critical 4, serious 2, normal 1, minor 0.5, wishlist 0.25 — so
**each rung is worth exactly twice the one below it**, then multiplies by the
age curve, doubles it if the task blocks a release, and divides by declared
effort.

**An UNRATED task floors at 0.1, below even wishlist.** That is deliberate, not
an accident of the absent tag: untriaged work sinks to the bottom of
`--sort priority` and stays there until somebody rates it. The fix is to rate
the task. `taskloom list --sort priority` reports how many tasks are sitting on
that floor rather than leaving the ranking to look healthy.

---

## Level is not kind

Two independent axes, and conflating them is what makes a log unqueryable:

- **`triage:level=`** — how bad is it if we ship without this? Derived from
  consequence. It is the only hand-assigned input to the ranking.
- **`triage:kind=`** — `defect` / `chore` / `capability`. What SHAPE of work it
  is. A `level=5` task is usually `kind=capability`, but a large chore with no
  live defect is `level=5` too.

`kind` deliberately contributes nothing to the priority score — a kind weight
would only restate the level in a second, conflicting vocabulary. What it drives
instead is the ROT CURVE: an aging capability or chore loses urgency over time,
while an aging exposed defect escalates. How a task's urgency MOVES with age is
a property of what kind of work it is; how much it matters today is the level.

The factual flags — `triage:data-loss`, `triage:security`, `triage:crashes`,
`triage:no-workaround`, `triage:regression` — are **searchable labels, not
score inputs**. They record what is true about an issue so a `--tag-query` can
find it. The ranking consequence of those facts is exactly what the level is
for, and having them scored too would count the same fact twice.

---

## The levels, in detail

### `triage:level=1` — critical

The consequence is unrecoverable or crosses a boundary that is supposed to hold.

- Data destroyed or corrupted with no recovery path.
- A trust gate admits content it should withhold, or a rejection is dropped so
  the item is re-admitted on a publisher signature.
- A credential becomes readable where it was not, or a workspace confinement is
  escaped.

Security lands here **by consequence**. Ask what an attacker or an accident
GETS, not whether the word "security" appears.

### `triage:level=2` — serious

The thing is unusable, or — the shape this project actually produces — it
reports success and did not do the work.

- Exit 0, a success message, and zero bytes written.
- A gate that is green while measuring nothing: a test that passes with the
  subject neutered, coverage credited to code that never ran.
- Major functionality broken with no satisfactory workaround.

**The silent no-op is level 2, not level 3**, and this is the one place this
standard departs from a plain reading of its sources. A loud failure is a
smaller problem than a quiet one: the loud one is discovered by whoever hit it,
while the quiet one is discovered by nobody and is indistinguishable from
working. A green gate that measures nothing is the same defect wearing a
lab coat.

### `triage:level=3` — normal

Wrong, and a user could act on the wrongness, but there is a way around it.

- Incorrect output, a crash, or a race, where a workaround exists.
- **Documentation, help text or a comment asserting behaviour the code does not
  have.** This sits here rather than lower because a wrong doc is what the next
  reader trusts INSTEAD of re-deriving the thing — silence would at least have
  made them look.

### `triage:level=4` — minor

Real, but nobody is materially worse off: cosmetic issues, naming
inconsistencies, tidy-ups, dead code with no live consequence.

### `triage:level=5` — wishlist

The capability does not exist yet. New commands and surfaces, extensions,
speculative work, and refactors undertaken to enable future work rather than to
fix a present defect.

A refactor whose absence causes no wrong behaviour today is level 5, however
much it is wanted.

---

## Where this comes from, and where it deliberately diverges

Rungs 1–4 are [Mozilla's defect severity
ladder](https://firefox-source-docs.mozilla.org/bug-mgmt/guides/severity.html)
(S1 catastrophic / S2 serious / S3 normal / S4 trivial), which already puts data
loss at the top. Rung 5 is Debian's `wishlist`, the one tier that survives in
almost every tracker.

**The divergence is folding security in.** Mozilla, Chromium and Debian all keep
a separate security scale — Chromium's `S4` even means "lack of a security
severity". They separate it because security triage has a different assessor, a
different SLA, and an embargo and disclosure workflow.

ctxloom has none of those: no external reporters, no embargo, one person
triaging. A second scale would be a cross-reference nobody maintains, and a
finding filed on the wrong one is a finding nobody sees. So security is folded
in by consequence — and it can legitimately land at level 3, since a doc
falsely claiming a security property is a false claim, not a breach.

If ctxloom ever takes external vulnerability reports, revisit this: the reason
the big projects split the scales is disclosure, and that arrives with the
first outside reporter.

---

## Assigning it

**Read the code, not the task text.** A level taken from a task's own prose
inherits that prose's staleness. Tasks in this log have described functions that
were deleted commits earlier, and a task tagged as a security escape on the
strength of its own description turned out to name a parser that no longer
exists.

The question to ask is always the same: **if we shipped without this, what would
actually happen?** Then find the tier that names that consequence.

---

## Work that lives in ANOTHER REPO — `repo:`

`touches:` is repo-relative and exists to predict git collisions *here*. A task
whose edit target is in a sibling checkout therefore cannot be located at all:
writing a path anyway invents a conflict that cannot happen, and writing nothing
leaves the task unaddressable.

    repo:tagma          repo:reprise        repo:ctxloom-vscode
    repo:ctxloom-personal                   repo:ctxloom-default

`repo:` names the foreign checkout; `sig:` still carries the address inside it,
because a symbol or recipe name is meaningful in any repo. Omit `touches:`
entirely for those tasks — its absence is now MEANINGFUL rather than missing,
and `repo:` is what says so.

One task, one foreign repo — a task spanning two is two tasks, because it cannot
land atomically. That rule is PROSE, not enforced, and the reason is worth
knowing: `tagma.arity:"..."=scalar` only binds a namespaced key=value target
like `triage:kind`. `repo:` is shaped like `area:` — a bare namespace:key with
no value — so there is nothing for the arity enforcer to bind to, and a
declaration against it is silently a no-op. `area:` has always had the same
prose-only "exactly one per task" rule for the same reason. Verified by writing
two values of each: `triage:kind` collapsed, `repo:` did not.

This was not designed up front. Four independent auditors hit the same wall on
the same day and each invented a different workaround — one wrote a
ctxloom-relative path that would have been a lie, two omitted `touches:` with a
prose note, one tagged only the local half of a split task. That is the signal
that a vocabulary is missing a word.

A SPLIT task — part here, part elsewhere — carries both `repo:` and the local
`touches:`. Say in the body which half is which, because a reader querying
`touches:` sees only the local half and will otherwise under-scope it.

---

## Admitted to a dated work queue — `queue:`

    queue:20260830

`queue:<YYYYMMDD>` marks a row as ADMITTED to the work queue for that date —
typically an overnight or unattended run. It is a statement of FACT about what
was taken on, not an aspiration about what might get done, which is why the
value is the date the queue ran rather than a target or a due date.

It exists to make one question answerable in a single query, after the fact and
by someone who was not there:

```
taskloom list --tag-query 'queue:20260830'
```

Deliberately NOT the things it resembles:

- not `landed:` — `landed:` says work is finished and waiting on a merge.
  `queue:` says only that it was ADMITTED. A queued row may end the night done,
  partly done, or untouched because something earlier ate the time.
- not `triage:blocks-release=` — that is a milestone the work is aimed at, and
  it survives being missed. A `queue:` date is history and never moves; if work
  slips to another night it gains a SECOND `queue:` tag rather than editing the
  first.

It is therefore REPEATABLE, and a row carrying several `queue:` dates is the
useful signal that something keeps being admitted and keeps not landing.

---

## Work that has LANDED but is not merged — `landed:`

There is a state between "still to do" and "done", and it is where most work
sits for a while: written, committed, gated, and waiting on a human to merge it.

    landed:"integration/overnight-0822"

`landed:` names the branch carrying the work. It is deliberately NOT any of the
things it resembles:

- not `Done` — nothing is merged, pushed, or reviewed. Marking it Done makes
  the log claim a guarantee no one gave.
- not `triage:verdict=phantom` — phantom means the defect was never real or was
  already gone. Tagging your own finished work phantom erases the fact that
  somebody fixed it, and six months later the record says nothing happened.
- not `In Progress` — nobody is working on it. It is finished and parked.

It is REPEATABLE rather than scalar, because a task can carry the branch plus
`landed:partial`, meaning that branch fixed part of it and knowingly left the
rest. A `partial` row still has live work in it and must not be closed on the
strength of the branch alone; cut it down to what remains at merge instead.

The queryable payoff is the merge checklist, and the audit exclusion:

```
taskloom list --tag-query 'landed:"integration/overnight-0822"'   # merge these
taskloom list --tag-query 'landed:partial'                        # cut these down
```

A backlog audit skips anything carrying `landed:` — it is already dispositioned,
and re-auditing it wastes an agent and risks laundering finished work into a
`phantom`.

---

## Recording an audit verdict — `triage:verdict:` and `triage:audited:`

A backlog audit asks two questions of an old task: is the claim still TRUE, and
does it still MATTER. Those are different, and the second is the one that gets
missed — a task can be perfectly accurate about a subsystem nobody ships any
more. The answer is a tag so it can be QUERIED; the reasoning goes in the task
body, exactly as the level is a number and this document is its legend.

| `triage:verdict=` | Means |
|---|---|
| `holds` | still true AND still matters |
| `phantom` | already fixed, or the code it names no longer exists |
| `obsolete` | still literally true, but no longer matters — the component was retired, the decision reversed, the approach superseded |
| `partial` | some clauses landed, some remain |
| `unclear` | could not be settled; says what would settle it |

`triage:audited=<YYYYMMDD>` records WHEN. It is a plain integer for the same
reason the level is: it compares. `--tag-query 'triage:audited<20260801'` finds
verdicts old enough to distrust, which a date string could never answer. A
verdict without a date is a claim with no expiry.

The pairing is what makes the close list a query rather than a reading exercise:

```
taskloom list --tag-query 'triage:verdict=phantom'    # what can be closed
taskloom list --tag-query 'triage:verdict=obsolete'   # what stopped mattering
taskloom list --tag-query 'triage:verdict=unclear'    # what needs a human
```

Both are declared, so both are enforced at WRITE time: a misspelled verdict or
an impossible date is refused when you tag, naming the declared values. Neither
is rated — do not put a `triage:level` on a `phantom` or an `obsolete` task,
because rating a dead row launders it into a live-looking one.

---

## Locating the work — `touches:` and `sig:`

The level says how bad it is. These say WHERE it is, and they answer two
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

## Linking one task to another — `relates:`

`relates:<harp-id>`, on BOTH rows. Repeatable.

Prose that says "see `<harp>`" is invisible to a query: the connection exists
only for whoever happens to read that paragraph, and the second row does not
know it has a sibling at all. `relates:` makes the link findable from either
end — `--tag-query 'relates:<harp>'`.

Use it where two rows describe one situation from different sides: a task split
in two, a root cause and the symptom filed against it, a decision and the work
it gates, a premise correction and the ruling it suspends.

It is deliberately NOT a dependency or an ordering. It says these belong
together, nothing more. If one genuinely blocks another, say so in the body
where you can explain what unblocks it — a tag cannot carry that.

Unlike `touches:`/`sig:`, nothing recomputes this: a harp is a stable
identifier, so the tag does not rot when code moves, but it also will not
appear on its own. Add it when you file the second row, while you still
remember the first.

## Four rulings the first tagging pass needed

These came out of applying the standard to a real batch. Each is a case where
two rules in this document could both be followed and gave different answers.

### A release-blocking `triage:level=5` is LEGAL

A task can be `triage:level=5` and `triage:blocks-release=...` at the same
time, and that is not a contradiction. The level is the consequence if the
thing is missing; `blocks-release` is the promise we made about this release.
A capability we committed to ship is a wishlist-level task we have decided to
be blocked by.

Do not inflate a capability's level to express that it is wanted. Use the
release tag — that is the axis that carries "wanted", and it is the one that
gets cleared when the release ships. The ranking already reads both: a release
blocker's score is doubled whatever its level.

### A ruling is preserved; the WORK is cut down

"Record only what is still to do" applies to WORK. It does not apply to a HUMAN
RULING, which is a decision, not a task step — and decisions exist nowhere else
once the plan file is gone.

When a ruling has partly landed: keep the live clauses verbatim, and move the
landed ones under a short CLOSED heading that says what landed and on what
evidence. Deleting a ruling clause because its work is done destroys the record
of the decision and invites someone to re-decide it the other way.

### A vacuous test is level 2; a missing test is not

They are different faults and they rate differently:

- A test that PASSES while proving nothing is level 2. It actively reports
  coverage that does not exist, so it is worse than no test — the shape this
  standard already rates as "succeeds without doing the thing".
- A test that is ABSENT is level 4, because the gap is at least honest, and
  rises only if its absence is hiding a live defect.

"The defect is fixed, only the regression test is missing" is therefore level
4, not level 2.

### The level is rated on the REMAINDER

When part of a task has landed, rate what is left, not what the task originally
described. A level 1 defect that has been fixed but for a missing test is a
level 4 task, and its body should already have been cut down to match.
