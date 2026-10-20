<!--
J001200 narration companion (j001200_recall.feature) — FLOWS-UNIFIED.md's U9.

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
structured run is teed into a canonical transcript ctxloom owns, and an
interactive session the tee never reached is converted out of its engine's own
private store into that same format when the run exits (or when `session
adopt` records an older one) — so a conversation outlives both the session and
the engine that produced it.
J001000 proves all of that, thoroughly, per engine.

This journey is the other half, and it is the half nobody had asserted. Capture
without recall is not memory, it is disk usage. The question is not "did we
record March" — we did — but whether anyone can find the decision in it and put
it back in front of a model.
<!-- /doc:intro -->

## What works

Search works, and it works on the one thing an archivist half-remembers: the
question. A session's title is its first user prompt, read from its
transcript, and `session search` matches it alongside the harp name (three
random words nobody remembers) and the session's times. The rest of the
conversation is not searched; it is what a resume brings back.

The title and the conversation each carry their own marker, so "found by its
title" and "resumed with its conversation" are distinguishable outcomes rather
than one assertion either would satisfy. A second, unrelated session sits in
the fixture throughout, so every hit proves something was discriminated rather
than that everything was returned.

`session show` prints the question the session was opened with. An empty query
says so rather than answering with zero bytes. And recall does not consume what it reads —
cheap to assert, easy to break during a resume refactor, and catastrophic in a
quiet way if it ever does break, because it would fail on the *second* person
to ask the question.

## The archive and the archive

The resume scenario went red, and the reason was more interesting than the
failure — and it is now fixed.

`run --session <harp>` is the payoff the whole capture apparatus exists for. It
used not to read the canonical transcript. `operations.RecordedSessionEntries`
resumed by asking the BACKEND's own session reader for the entry's native
session id — so what got folded back into a run came from the vendor's
private store, not from ctxloom's copy.

Which meant ctxloom maintained two archives and read back from the wrong one.
The canonical transcript is the artifact it owns, converts, schema-versions and
promises portability for. The vendor store was the artifact it actually resumed
from, and the vendor store is precisely the thing that goes away — when a
vendor rotates its format, when a session ages out, when the team switches
engines, when someone clears their cache. A session whose vendor store is gone
is exactly the case the vendor-store conversion exists to rescue, and it was
not resumable from the copy that conversion had just made.

Fixed 2026-08-05: `RecordedSessionEntries` now prefers
`entry.CanonicalTranscriptPath` (populated only when `transcript.jsonl` exists
on disk) via `transcript.ParseTranscriptFile`; that transcript is now its only
source. Nothing about this
was hard to fix and nothing about it was visible without asking; it had
presumably been true the whole time, and closing it also closed a measured
blind spot in the retention scenario below (a consuming resume placed right
after the canonical read is now on the path the guard actually watches).

## The model's side of recall

A context that assembles correctly and is then dropped on the way to the
engine looks identical from `--dry-run`. So the payoff is asserted twice: once
on the assembled context, and once on what the engine actually received — a
one-shot resume against the mock records its input, and the March
conversation's marker must be in it.

## The mistyped harp

A harp is three random words. Mistyping one is the single most ordinary error
available on this command, and it used to take the process down with a nil
dereference: `operations.GetSession` returns `(nil, nil)` for an absent harp
and `RecordedSessionEntries` dereferenced the entry without checking.

The detail that made it worth a scenario rather than a one-line fix is that
`resumeFullContext` documents a contract covering both halves of the problem —
an unresolvable harp and an unbound one — and only the unbound half was
honoured. The unbound case degraded correctly with a warning. The unresolvable
case never reached the warn that was written for it. The same call is shared by
every resume path with the same hole, the coordinator's ended-child resume included, so the blast
radius was wider than one flag on one command. Fixed at the shared primitive;
this scenario now proves the unresolvable half degrades the same way.

It is filed (task `diffusive-dazzler`) and reported in the CLI package's own
characterization tests. This journey reproduces it at the surface a person
actually touches, which is the difference between a known defect and a felt one.

<!-- doc:outro -->
The pattern across the rows that were once red was that recall was built as
plumbing and not as a surface anyone could inspect: resume folded a transcript
in from the wrong archive, and a mistyped harp took the process down instead of
reaching the warning written for it. Both are fixed at the shared primitive
(`RecordedSessionEntries` reads ctxloom's own canonical transcript, and refuses
an unknown harp by name), and most rows name, in their scenario comment, the
mutation that turns them red.

The capture side of this story is genuinely good, and it is the expensive side.
It would have been a shame to own four vendors' history in a portable schema,
and then resume from the vendor — the resume path no longer does.
<!-- /doc:outro -->
