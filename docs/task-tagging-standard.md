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

## The design document a task is governed by — `ctxloom:plan=`

    ctxloom:plan="/home/babbitt/.ctxloom/sessions/<harp>/persist/<name>.plan.md"

Associates a task with the plan or spec that governs it, as a STRUCTURED FIELD
rather than a sentence in the body. Quote the value: paths contain `/` and `.`,
which are not bare-value characters.

It is REPEATABLE, and that is load-bearing rather than incidental. A task is
often governed by a deep design plus a later document that CORRECTS it, and both
must be reachable — `tubby-grit` carries the ~970-line mux-terminal design and
the preflight that refutes one of its central claims. Carrying only the newer
one loses the reasoning; carrying only the older one sends a reader to build
something that already exists.

WHY A TAG AND NOT PROSE, which is the whole point: a path written in a body is
found only by regexing task text, and that scan is unreliable in a way that
fails SILENTLY. A path wrapped across a line break matches no single-line
pattern, so a citation audit reports "no plan referenced" for a task whose
reference is perfectly intact — and, worse, reports the same for one whose
reference is BROKEN. Measured on `tubby-grit` 2026-08-30: reference intact, file
present, scan said nothing was cited.

A tag has no line breaks and no surrounding prose, so:

```
taskloom list --tag-query 'ctxloom:plan="<path>"'   # what does this plan govern
```

is exact, and checking that every cited plan still exists becomes a loop over a
field instead of a regex over prose.

When a plan MOVES or is superseded, fix the tag. Nothing else will — a tag has
no compiler and no gate.

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

## Why a task needs a PERSON — `human:`

`queue:` says a row was ADMITTED. `human:` says it was WITHHELD, and names why.
They are complements, and a row can carry both — admitted on one night, found
human-gated on another.

    human:decision      human:voice         human:authority
    human:verify        human:prerequisite  human:action

The flat `human` tag it replaces recorded only WHERE a row went — "the one queue
a person actually works from" — never why. That omission has a measured cost. On
2026-09-10 a sweep stripped `human` from 28 rows by asking "does this row need a
DECISION from a person", which is a reasonable test and the wrong one: the tag's
net is wider than decisions. The removal was reverted within the hour and 16 of
those rows still carry the account of it. A tag that states its own test cannot
be stripped that way, because the test is no longer something a reader has to
infer.

An earlier attempt at the same split, `needs-decision`, reached 39 rows and zero
survivors. It failed for the reason this one is shaped to avoid: it carved off
only the largest category and left everything else under the undifferentiated
tag, so the ambiguity it was meant to remove stayed exactly where it was.

### What each one means

`human:decision` — a ruling only a person can make. Architecture, a public
contract, a wire or on-disk format, a security posture, an accepted risk, a
choice between designs. If the answer would be written in a design doc rather
than a diff, it is this.

`human:voice` — prose in the human's voice. Prompts, system messages, skills,
fragments, profile text, release notes. An agent may establish that such text is
WRONG, and should; it must not author the replacement.

The tag follows the PROSE, not the FILE. A profile or skill file also holds
structure — a ref, a list order, a key rename — and changing that shapes no
model's behaviour; it points at a different file. Those edits carry no voice
gate. The question to ask is "would a model read this and behave differently?",
which is a harder test than checking a path and a better one: gating every
mechanical edit to a voice file is how a human queue becomes unreadable, which
is the failure this vocabulary exists to undo.

`human:authority` — an act that carries the human's identity rather than their
judgment. Signing, publishing, pushing, cutting a release, granting trust. The
distinction from `human:action` is not capability: an agent can run the command.
It is that the act asserts who authorised it.

`human:verify` — the only evidence is a person looking. A rendering, an
interaction, a live multi-process run, a flake that reproduces only under real
load. The work may be entirely mechanical; what cannot be delegated is the
watching.

`human:prerequisite` — the work is ordinary, and something it needs is absent.
Another operating system, a credential, a published artifact, a CI lane that
does not exist yet, a row that has not landed. Unlike the others this one can be
DISCHARGED by the world changing, which makes it the natural companion to a
`Deferred` status and a revive trigger.

`human:action` — a person must do it, for none of the above reasons.
Deliberately residual and deliberately last. If it starts collecting rows, the
other five are not carving real joints and the vocabulary needs revisiting.

### Repeatable, and PROSE not enforced

A row may carry several. That is not an edge case: measured across the 52 open
rows on 2026-09-15, 24 of them — 46% — were human-gated for two or more
independent reasons, and several spanned exactly the boundaries above. A bundle
edit is `human:voice` AND `human:authority`, because the words are the human's
and the re-signature is theirs too.

So `human:` is REPEATABLE, and declaring it scalar would be actively harmful
rather than merely useless: taskloom's write seam collapses a scalar re-tag with
newest-wins, so the second reason would silently displace the first.

The value vocabulary is PROSE, not enforced, and the reason is the same one
recorded for `repo:` above — see that section. A misspelling stores cleanly and
is then reachable by no query. `taskloom tags` is the check: a value with a
count of one, sitting beside a near-identical spelling, is the tell.

### Where the authority lives

This section describes the vocabulary. It does not define what makes work
human-gated — that is the stop conditions in the `unattended` skill, and the
additional withholding reasons in `admit`. Read them there. They are not
restated here, because a rule kept in two places drifts and the stale copy goes
on being enforced while it lies.

Note also that the stop conditions are NOT a taxonomy of work and must not be
converted into one. Several of them — destructive git, another session's
worktree, system and toolchain changes, leaving daemons — are prohibitions on
how an AGENT BEHAVES. They never produce a tagged row, and a value set derived
mechanically from that list would carry permanently empty members.

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
touches:"internal/core/coord/children.go"
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
`internal/core/coord/children.go`, and with nothing recording that, the
collision had to be written into the task bodies as prose. Prose does not
survive a query.

---

## Retired vocabulary — what these tags were, and what replaced them

`taskloom tags` lists every tag ever used, with active and total counts. A tag
with a live total and zero active rows is not necessarily dead — it may just be
between uses — so the ones below are named explicitly. NOTHING IS STRIPPED: the
rows that carried them keep them, because a closed row's tags are part of its
record and rewriting them would edit history a later reader may want.

    needs-decision      ->  human:decision
    low-priority        ->  triage:level=4 or =5
    deferred            ->  the Deferred STATUS, which is real mechanism
    for-review, review  ->  code-review
    sdk1-blocked        ->  (obsolete; the SDK-1 work is finished)
    publish-blocker     ->  triage:blocks-release=

`needs-decision` deserves its own note, because it is the most likely to be
re-invented. It reached 39 rows and zero survivors. It was an earlier attempt at
the same split `human:` now makes, and it failed by carving off only the largest
category and leaving everything else under an undifferentiated `human` tag — so
the ambiguity it existed to remove stayed exactly where it was. It is also
defined in no file: it was convention, never standard, which is part of why it
could die without anyone noticing.

`deferred` is worth a second look too. It duplicated a STATUS, and a tag that
shadows real mechanism is worse than a redundant one: the status is what queries
and reports actually read, so a row could carry the tag and not the status and
look parked to a human while reading as live to every tool.

### Three spellings of one gate

`for-review`, `review` and `code-review` all mean the same thing and all exist.
That is the hazard the tags reference names directly — running `taskloom tags`
before coining a word is how you notice that a near-identical spelling is
already in use. Prefer `code-review`; it has the most rows and the clearest
name. The other two are left in place on the rows that carry them.

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
