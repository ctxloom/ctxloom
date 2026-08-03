@doc
Feature: The archaeologist — what did we decide in March?

  Somebody asks why the worktree layout is the way it is. Three people remember
  the conversation. Nobody remembers the reason, and the one person who might
  have written it down has left. The decision was made in a session, in March,
  and the session is still on disk — which is only useful if anyone can find it
  and put it back in front of a model.

  ctxloom captures a great deal. It tees every structured run into a canonical
  transcript it owns, and `session backfill` converts four different vendors'
  private stores into that same format, so history survives an engine switch
  and outlives the tool that produced it. That is the expensive half, and it is
  built.

  This journey is the other half: the payoff. Search finds the session, show
  prints what it concluded, and `run --session` folds it back into a live run's
  context. History that can be captured but not recalled is an archive, not a
  memory — the disk fills up and nothing gets easier.

  # NOTE ON SCOPE. J12 (j12_transcript_capture.feature) owns CAPTURE and proves
  # it thoroughly: per-engine vendor-log conversion, idempotent re-backfill,
  # live-captured sessions left untouched, bulk conversion accounting. Nothing
  # here re-drives `session backfill`. This journey owns RECALL, which is the
  # unproven half.
  #
  # NOTE ON THE THREE MARKERS. A session's searchable text lives in three
  # places — harp name, index summary, distilled essence — and a search
  # matching only one of them would look identical to a working one against a
  # fixture where all three say the same thing. Each carries its OWN marker, so
  # each scenario proves a specific field participates. The same split is
  # load-bearing in the resume scenarios: transcript and essence carry
  # different markers, so "resumed via the transcript" and "resumed via the
  # essence" are distinguishable outcomes rather than one assertion that either
  # would satisfy.
  #
  # NOTE ON THE FIXTURE'S SECOND SESSION. Every search assertion has a wrong
  # answer available to give: an unrelated, never-distilled session sits beside
  # the March one. A search returning one hit proves nothing if there was
  # nothing else it could have returned.
  #
  # NOTE ON TAGS. Every scenario is @wip with its own untag condition, including
  # the ones believed to pass.

  Background:
    Given a design question everyone remembers deciding and nobody remembers why

  # Essence content is the field that matters most and is least likely to be
  # searched: it is the only place a DECISION is written down in prose, as
  # opposed to a harp name (three random words) or a summary (one line). If
  # search does not reach it, the archive is keyed by everything except its
  # content.
  #
  # UNTAG WHEN: confirmed to pass as written. Believed to pass today —
  # `session search`'s own help says it searches harp, summary and essence.
  @wip
  Scenario: A phrase from the decision itself finds the session that made it
    When I run "ctxloom session search J23-ESSENCE-WORKTREE-NAMING-DECISION"
    Then the search names the March session by a phrase that appears only in its distilled essence

  # The summary field, proven separately for the same reason: one scenario per
  # field, so a regression narrows to a field rather than to "search broke".
  #
  # UNTAG WHEN: confirmed to pass as written. Believed to pass today.
  @wip
  Scenario: A phrase from the session summary finds it too
    When I run "ctxloom session search J23-SUMMARY-ONLY-MARKER"
    Then the search names the March session by a phrase that appears only in its index summary

  # The empty result, which is the one an archivist hits most and the one most
  # likely to be silent. "Nothing matched" and "the search did not run" must
  # not look the same — and a command that answers with zero bytes and exit 0
  # is this codebase's characteristic bug, not a stylistic quibble.
  #
  # UNTAG WHEN: confirmed to pass as written. Believed to pass; the assertion
  # is deliberately about the BYTES printed, not the exit code.
  @wip
  Scenario: A query that matches nothing says so rather than printing nothing
    When I run "ctxloom session search J23-MATCHES-ABSOLUTELY-NOTHING"
    Then ctxloom says plainly that nothing matched

  # UNTAG WHEN: confirmed to pass as written. Believed to pass today;
  # session.feature already covers the basic shape, and this asserts the
  # ESSENCE PAYLOAD reaches stdout rather than that the command exits 0.
  @wip
  Scenario: Show prints what the session actually concluded
    When I run "ctxloom session show amber-quiet-heron"
    Then ctxloom prints the decision the session reached

  # BOUNDARY B9, and the product states the gap itself: `session distill`'s own
  # help says "Distillation is on-demand: nothing distills a session
  # automatically when it ends." So the UNDISTILLED session is the default case,
  # not the exception — most of the archive is in this state — and the error a
  # user hits has to distinguish "this session has no summary yet" from "this
  # session was never recorded", or they will conclude the capture failed and
  # stop trusting the archive.
  #
  # UNTAG WHEN: `session show` on an undistilled harp names `session distill`
  # as the fix. Uncertain today: the command errors, but whether it names the
  # remedy is exactly what this asserts.
  @wip
  Scenario: A session nobody ever distilled says so, and says what to do about it
    When I run "ctxloom session show brisk-copper-moth"
    Then ctxloom says the session was never distilled and names how to distill it

  # THE PAYOFF. Everything upstream — the tee, the importers, the canonical
  # schema, four vendors' conversion — exists to make this one line work.
  #
  # UNTAG WHEN: `run --session <harp> --dry-run` shows the recorded conversation
  # in the assembled context. Expected RED, and worth understanding before
  # fixing: operations.RecordedSessionEntries resumes by asking the BACKEND's
  # own session reader for entry.SessionID — it does not read the canonical
  # transcript ctxloom captured. So the archive ctxloom owns, converts and
  # promises portability for is not the archive `--session` reads back, and a
  # session whose vendor store is gone (the exact case backfill exists to
  # rescue) may not be resumable from ctxloom's own copy of it.
  @wip
  Scenario: Resuming the March session puts that conversation back in front of a model
    When I run "ctxloom run --session amber-quiet-heron --dry-run -p default"
    Then the assembled context carries the conversation she had in March

  # The two resume modes must differ in what reaches the model, or --distill is
  # a flag that costs a distillation and buys nothing. Asserted as a three-way
  # discrimination — essence present, raw absent — so neither "carried
  # everything" nor "carried nothing" can pass.
  #
  # UNTAG WHEN: --distill demonstrably resumes via the essence. Note the
  # distilled path rides CTXLOOM_RESUMED_FROM/PARTS and a SessionStart hook
  # rather than the assembled context itself; if that is the intended design,
  # this scenario is the record that a user cannot SEE what was resumed, and
  # the fix is a visibility one rather than a plumbing one.
  @wip
  Scenario: Resuming through the essence carries the conclusion, not the whole conversation
    When I run "ctxloom run --session amber-quiet-heron --distill --dry-run -p default"
    Then the assembled context carries the distilled essence and not the raw conversation

  # A FILED DEFECT (task diffusive-dazzler), reproduced here at the surface a
  # user actually touches. A harp is three random words; mistyping one is the
  # single most ordinary error available on this command. Today an unknown harp
  # takes the process down with a nil dereference — operations.GetSession
  # returns (nil, nil) for an absent harp and RecordedSessionEntries
  # dereferences entry.SessionID without checking — while the UNBOUND case
  # (a harp that exists with no transcript) degrades correctly. The contract
  # resumeFullContext documents for itself covers both halves; only one is
  # honoured.
  #
  # UNTAG WHEN: an unknown harp warns and runs. The same call is shared by the
  # ACP resume path and the coordinator's ended-child resume, so the blast
  # radius is wider than this one flag.
  @wip
  Scenario: A mistyped harp warns instead of taking the process down
    When I run "ctxloom run --session no-such-harp-anywhere --dry-run -p default"
    Then ctxloom warns and runs anyway, naming the harp it could not find

  # The model's own side of recall. A human running `session show` is one route;
  # the assistant reaching for its own memory mid-conversation is the other, and
  # it is the one that actually gets used, because it needs nobody to remember
  # that March was where the decision happened.
  #
  # The assertion reuses the shared `the tool result contains` step
  # deliberately: load_session answers in TEXT, not JSON, so a bespoke
  # assertion that unwrapped the envelope as an object failed on the envelope
  # rather than on the content — measured while writing this.
  #
  # UNTAG WHEN: load_session returns the named session's recorded conversation.
  # Uncertain today — the tool is registered and its payload is unasserted,
  # which is precisely the state that lets a memory tool return an empty
  # envelope for months without anyone noticing. If the deliberate answer is
  # that load_session returns the ESSENCE rather than the conversation, change
  # the marker rather than deleting the scenario: the point is that SOME
  # session content comes back.
  @wip
  Scenario: The assistant can reach the March session without being told where to look
    When the agent calls tool "load_session" with:
      | harp | amber-quiet-heron |
    Then the tool result contains "J23-TRANSCRIPT-ONLY-MARKER"

  # Recall is a READ. This is the scenario that would catch a resume
  # implementation that consumes, truncates, compacts or re-writes the record it
  # read — a memory that erases itself on use, which is worse than no memory
  # because it fails on the SECOND person to ask the question.
  #
  # UNTAG WHEN: confirmed to pass as written. Believed to pass today; it is
  # cheap and pins an invariant that is easy to break during a resume refactor.
  @wip
  Scenario: Recalling a session does not consume it
    When I run "ctxloom run --session amber-quiet-heron --dry-run -p default"
    Then the canonical transcript she recalled from is still on disk, untouched
