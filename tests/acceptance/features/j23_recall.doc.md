<!--
J23 narration companion (j23_recall.feature) — FLOWS-UNIFIED.md's U9.

Every claim below traces to a named scenario in the sibling feature file. The
architectural finding in "The archive and the archive" was measured while
wiring this journey, not read out of a design document, and it is the reason
this file is worth reading before implementing anything in the recall path.
-->

<!-- doc:intro -->
Somebody asks why the worktree layout is the way it is. Three people remember
the conversation. Nobody remembers the reason. The one person who might have
written it down has left, and what everyone actually says is "we decided this
in March, I think".

The session from March is still on disk. So are four thousand others.

ctxloom captures a great deal, and the capture half is genuinely built: every
structured run is teed into a canonical transcript ctxloom owns, and
`session backfill` converts four different vendors' private stores into that
same format, so a conversation outlives both the session and the engine that
produced it. J12 proves all of that, thoroughly, per engine.

This journey is the other half, and it is the half nobody had asserted. Capture
without recall is not memory, it is disk usage. The question is not "did we
record March" — we did — but whether anyone can find the decision in it and put
it back in front of a model.
<!-- /doc:intro -->

## What works

Search works, and it works across all three of the places a session's text
lives. That is worth stating precisely, because it is the sort of thing that
looks fine and is half-broken: a session is findable by its harp name (three
random words nobody remembers), by its one-line index summary, and — the one
that matters — by the prose of its distilled essence, which is the only place a
DECISION is ever written down. If search reached the first two and not the
third, the archive would be keyed by everything except its content, and it
would still look like a working search.

Each of those is a separate scenario against a separate marker, so a regression
narrows to a field rather than to "search broke". A second, unrelated session
sits in the fixture throughout, so every hit proves something was discriminated
rather than that everything was returned.

`session show` prints what the session concluded. An empty query says so rather
than answering with zero bytes. And recall does not consume what it reads —
cheap to assert, easy to break during a resume refactor, and catastrophic in a
quiet way if it ever does break, because it would fail on the *second* person
to ask the question.

## The archive and the archive

Then the resume scenario goes red, and the reason is more interesting than the
failure.

`run --session <harp>` is the payoff the whole capture apparatus exists for. It
does not read the canonical transcript. `operations.RecordedSessionEntries`
resumes by asking the BACKEND's own session reader for the entry's native
session id — so what gets folded back into a run comes from the vendor's
private store, not from ctxloom's copy.

Which means ctxloom maintains two archives and reads back from the wrong one.
The canonical transcript is the artifact it owns, converts, schema-versions and
promises portability for. The vendor store is the artifact it actually resumes
from, and the vendor store is precisely the thing that goes away — when a
vendor rotates its format, when a session ages out, when the team switches
engines, when someone clears their cache. A session whose vendor store is gone
is exactly the case `session backfill` exists to rescue, and it may not be
resumable from the copy backfill just made.

Nothing about this is hard to fix and nothing about it is visible without
asking. It has presumably been true the whole time.

## The mistyped harp

A harp is three random words. Mistyping one is the single most ordinary error
available on this command, and today it takes the process down with a nil
dereference: `operations.GetSession` returns `(nil, nil)` for an absent harp
and `RecordedSessionEntries` dereferences the entry without checking.

The detail that makes it worth a scenario rather than a one-line fix is that
`resumeFullContext` documents a contract covering both halves of the problem —
an unresolvable harp and an unbound one — and only the unbound half is
honoured. The unbound case degrades correctly with a warning. The unresolvable
case never reaches the warn that was written for it. The same call is shared by
the ACP resume path and the coordinator's ended-child resume, so the blast
radius is wider than one flag on one command.

It is filed (task `diffusive-dazzler`) and reported in the CLI package's own
characterization tests. This journey reproduces it at the surface a person
actually touches, which is the difference between a known defect and a felt one.

## The two resume modes

`--session` and `--session --distill` are supposed to differ in what reaches
the model: the whole conversation versus the conclusion. The scenario asserts
that as a three-way discrimination — essence present, raw absent — so neither
"carried everything" nor "carried nothing" can pass.

It fails on the third branch: neither marker appears. The distilled path rides
`CTXLOOM_RESUMED_FROM/PARTS` and a SessionStart hook rather than the assembled
context, so a `--dry-run` shows nothing of what was resumed. That may be the
intended plumbing. It is still a finding, and the finding is about visibility
rather than mechanism: a user who cannot see what a resume brought in has no
way to tell a working resume from a silent one, which is the same problem J21
spends twelve scenarios on at a different layer.

<!-- doc:outro -->
The pattern across these four reds is that recall is built as plumbing and not
as a surface anyone can inspect. Resume folds something in from somewhere, the
distilled mode folds something else in through a different channel, the memory
tool returns an envelope — and at no point can the person relying on it see
what arrived. That is survivable for a feature nobody depends on and corrosive
for a memory system, because the failure mode of a memory system is not an
error, it is a confident answer assembled from nothing.

The capture side of this story is genuinely good, and it is the expensive side.
It would be a shame to own four vendors' history in a portable schema, and then
resume from the vendor.

All ten scenarios are `@wip` with their untag conditions in the feature file.
Six pass today.
<!-- /doc:outro -->
