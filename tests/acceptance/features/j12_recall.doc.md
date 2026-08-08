<!--
J12 narration companion (j12_recall.feature) — FLOWS-UNIFIED.md's U9.

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
produced it. J10 proves all of that, thoroughly, per engine.

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
is exactly the case `session backfill` exists to rescue, and it was not
resumable from the copy backfill just made.

Fixed 2026-08-05: `RecordedSessionEntries` now prefers
`entry.CanonicalTranscriptPath` (populated only when `transcript.jsonl` exists
on disk) via `transcript.ParseTranscriptFile`, falling back to the backend
reader only for a session that predates canonical capture. Nothing about this
was hard to fix and nothing about it was visible without asking; it had
presumably been true the whole time, and closing it also closed a measured
blind spot in the retention scenario below (a consuming resume placed right
after the canonical read is now on the path the guard actually watches).

## The assistant's own side of recall

`load_session` is the model reaching for its own memory mid-conversation
rather than a human running `session show`. It returns the harp's distilled
ESSENCE, not the raw conversation — confirmed by `mcp_tools.feature`'s own
"Load a prior session's essence over MCP" scenario, which names it that way in
its title. That is the deliberate design, not a gap: on-demand distillation
(`load_session`, `recover_session`, `get_previous_session`,
`list_sessions(distill_missing)`) is the surface the project has committed to
keeping (task `close-ducky` removes only the automatic *post-session* distill
pass; this on-demand path is explicitly unaffected).

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
the ACP resume path and the coordinator's ended-child resume, so the blast
radius was wider than one flag on one command. Fixed at the shared primitive;
this scenario now proves the unresolvable half degrades the same way.

It is filed (task `diffusive-dazzler`) and reported in the CLI package's own
characterization tests. This journey reproduces it at the surface a person
actually touches, which is the difference between a known defect and a felt one.

## The two resume modes

`--session` and `--session --distill` are supposed to differ in what reaches
the model: the whole conversation versus the conclusion. The scenario asserts
that as a three-way discrimination — essence present, raw absent — so neither
"carried everything" nor "carried nothing" can pass.

It still fails on the third branch: neither marker appears, and this one stays
`@wip`. The distilled path rides `CTXLOOM_RESUMED_FROM/PARTS` and a
SessionStart hook, both applied in `openSession`/`applyResumeEnv` — which
`--dry-run` never reaches at all (it returns before `openSession` is even
called). So this is not a rendering gap in an otherwise-complete assembly; the
distilled-resume mechanism simply never runs under `--dry-run`, on-demand
distill included. Making it visible is a real design decision (preview the
essence for display only, vs. folding it into the assembled context the way
full resume does — which would change what a REAL `--distill` run delivers,
redundantly with the hook it already rides), not something this journey
decides unilaterally. A user who cannot see what a resume brought in has no
way to tell a working resume from a silent one, which is the same problem J19
spends twelve scenarios on at a different layer — so the finding stands even
though the row stays red.

<!-- doc:outro -->
As of 2026-08-05, nine of these ten scenarios are untagged: verified to pass
AND to catch a real mutation to the production code they name. The pattern
across the three that were genuinely red was that recall was built as plumbing
and not as a surface anyone could inspect — resume folded a transcript in from
the wrong archive, a step table used the wrong argument name, and a resume
mode's delivery mechanism was invisible to `--dry-run` by construction. Two of
those are now fixed (`RecordedSessionEntries` reads ctxloom's own canonical
transcript first; the MCP argument name and marker were corrected to match
`load_session`'s real, deliberate essence-only behaviour). The third —
`--distill --dry-run` showing nothing — remains open pending a design decision
on where the preview lives; see its own scenario comment for the two shapes
considered and why neither was picked unilaterally.

The capture side of this story is genuinely good, and it is the expensive side.
It would have been a shame to own four vendors' history in a portable schema,
and then resume from the vendor — the resume path no longer does.
<!-- /doc:outro -->
