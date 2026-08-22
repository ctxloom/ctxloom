# Task Severity Standard — the ctxloom task log

**Status:** proposed standard. **Scope:** every task in the taskloom log, via the
`triage:severity=` tag.

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
