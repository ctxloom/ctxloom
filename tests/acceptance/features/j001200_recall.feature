@doc
Feature: The archaeologist — what did we decide in March?

  Somebody asks why the worktree layout is the way it is. Three people remember
  the conversation. Nobody remembers the reason, and the one person who might
  have written it down has left. The decision was made in a session, in March,
  and the session is still on disk — which is only useful if anyone can find it
  and put it back in front of a model.

  ctxloom captures a great deal. It tees every structured run into a canonical
  transcript it owns, and converts four different vendors' private stores into
  that same format the moment someone reaches for a session the tee never
  covered, so history survives an engine switch and outlives the tool that
  produced it. That is the expensive half, and it is built.

  This journey is the other half: the payoff. Search finds the session, show
  prints what it concluded, and `run --session` folds it back into a live run's
  context. History that can be captured but not recalled is an archive, not a
  memory — the disk fills up and nothing gets easier.

  # NOTE ON SCOPE. J001000 (j001000_transcript_capture.feature) owns CAPTURE and
  # proves it thoroughly: per-engine vendor-log conversion, a second recall that
  # never doubles the conversation, and a live-captured session served from its
  # own record. Nothing here re-drives that conversion. This journey owns RECALL,
  # which is the unproven half.
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
  # NOTE ON TAGS. Every scenario carried its own @wip untag condition,
  # including the ones believed to pass at the time they were written. As of
  # 2026-08-05, nine of the ten are untagged (verified pass + a killing
  # mutation, per-scenario comment below); one stays @wip pending a design
  # decision — see its own comment.

  Background:
    Given a design question everyone remembers deciding and nobody remembers why

  # Essence content is the field that matters most and is least likely to be
  # searched: it is the only place a DECISION is written down in prose, as
  # opposed to a harp name (three random words) or a summary (one line). If
  # search does not reach it, the archive is keyed by everything except its
  # content.
  #
  # UNTAGGED 2026-08-05, confirmed to pass as written AND to bite. Mutation:
  # dropping the essence fallback from cli.sessionMatchesQuery (return false as
  # soon as the metadata haystack misses) turns this red. The field really is
  # searched, and this row is what proves it — the essence is the only place a
  # DECISION is written down in prose.
  Scenario: A phrase from the decision itself finds the session that made it
    When I run "ctxloom session search J001200-ESSENCE-WORKTREE-NAMING-DECISION"
    Then the search names the March session by a phrase that appears only in its distilled essence

  # The summary field, proven separately for the same reason: one scenario per
  # field, so a regression narrows to a field rather than to "search broke".
  #
  # UNTAGGED 2026-08-05, confirmed to pass as written AND to bite. Mutation:
  # dropping e.Summary from cli.sessionMetadataHaystack turns this red and
  # leaves the essence row above green — which is exactly the per-field
  # narrowing this file splits its three markers to get.
  Scenario: A phrase from the session summary finds it too
    When I run "ctxloom session search J001200-SUMMARY-ONLY-MARKER"
    Then the search names the March session by a phrase that appears only in its index summary

  # The empty result, which is the one an archivist hits most and the one most
  # likely to be silent. "Nothing matched" and "the search did not run" must
  # not look the same — and a command that answers with zero bytes and exit 0
  # is this codebase's characteristic bug, not a stylistic quibble.
  #
  # UNTAGGED 2026-08-05, confirmed to pass as written AND to bite. Mutation:
  # deleting `ew.Println("(no sessions)")` from cli.renderSessionRows' empty
  # branch turns this red. That is the codebase's characteristic bug staged
  # deliberately — exit 0, zero bytes — and the assertion catches it, which is
  # the whole reason this row asserts on the BYTES and not the exit code.
  Scenario: A query that matches nothing says so rather than printing nothing
    When I run "ctxloom session search J001200-MATCHES-ABSOLUTELY-NOTHING"
    Then ctxloom says plainly that nothing matched

  # UNTAGGED 2026-08-05, confirmed to pass as written AND to bite. Mutation:
  # making runSessionShow's text branch write "" instead of the essence bytes
  # turns this red while the command still exits 0 — which is the distinction
  # this row was written for, and one session.feature's shape-only coverage
  # cannot make.
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
  # UNTAGGED 2026-08-05, condition met and confirmed to bite. The command does
  # name the remedy: "no essence for %q (run `ctxloom session distill %s` to
  # compact this session first)". Mutation: dropping the parenthetical from
  # cli.undistilledSessionError so it reads "(compact this session first)"
  # turns this red — the row asserts the REMEDY, not merely that an error
  # happened, which is what keeps a user from concluding the capture failed.
  Scenario: A session nobody ever distilled says so, and says what to do about it
    When I run "ctxloom session show brisk-copper-moth"
    Then ctxloom says the session was never distilled and names how to distill it

  # THE PAYOFF. Everything upstream — the tee, the readers, the canonical
  # schema, four vendors' conversion — exists to make this one line work.
  #
  # UNTAGGED 2026-08-05, confirmed to go red on the described defect and green
  # on the fix. It WAS red exactly as predicted: operations.RecordedSessionEntries
  # resumed by asking the BACKEND's own session reader for entry.SessionID
  # rather than reading the canonical transcript ctxloom itself captured — so
  # ctxloom maintained two archives (its own canonical copy, and the vendor's
  # native store) and resumed from the wrong one. Fixed by making
  # RecordedSessionEntries prefer entry.CanonicalTranscriptPath (populated
  # only when transcript.jsonl exists on disk) via transcript.ParseTranscriptFile,
  # falling back to the backend reader only when no canonical transcript was
  # ever captured for the harp — so a session predating canonical capture still
  # resumes exactly as before. Mutation: disabling the canonical-transcript
  # branch (falling straight through to the backend reader, the OLD behaviour)
  # turns this red again.
  Scenario: Resuming the March session puts that conversation back in front of a model
    When I run "ctxloom run --session amber-quiet-heron --dry-run -p default"
    Then the assembled context carries the conversation she had in March

  # The two resume modes must differ in what reaches the model, or --distill is
  # a flag that costs a distillation and buys nothing. Asserted as a three-way
  # discrimination — essence present, raw absent — so neither "carried
  # everything" nor "carried nothing" can pass.
  #
  # STILL @wip 2026-08-05, after investigation, deliberately not force-fixed.
  # Confirmed red: `run --session <harp> --distill --dry-run` carries NEITHER
  # marker. Root cause is structural, not a bug in the usual sense —
  # cli.runState.emitDryRun (the --dry-run early return) fires in runRun
  # BEFORE cli.runState.openSession, and openSession is the only place
  # applyResumeEnv/resumeDistillEnv run. So the entire distilled-resume
  # mechanism — including the on-demand distill it may trigger — never
  # executes under --dry-run; there is no essence content anywhere for
  # --dry-run to show, not merely a rendering gap. Making --dry-run exercise
  # it is a real design decision with two shapes, not a scoped fix:
  #   (a) preview-only — read/on-demand-distill the essence for DISPLAY in
  #       emitDryRun without running the rest of openSession (no session-index
  #       writes, no AssignSession) — a visibility fix, but a new code path;
  #   (b) fold the essence into ctxResult.Context the same way full-resume
  #       folds the transcript in prepareRequestInputs — changes what a REAL
  #       (non-dry-run) --distill run actually delivers into the assembled
  #       context, on top of (redundant with) the existing
  #       CTXLOOM_RESUMED_FROM/PARTS + SessionStart hook delivery. That is a
  #       production behaviour/contract change, not this row's to make
  #       unilaterally.
  # UNTAG WHEN: a human picks (a), (b), or a third shape, and --distill
  # --dry-run demonstrably shows the essence content somewhere in its output.
  @wip
  Scenario: Resuming through the essence carries the conclusion, not the whole conversation
    When I run "ctxloom run --session amber-quiet-heron --distill --dry-run -p default"
    Then the assembled context carries the distilled essence and not the raw conversation

  # The payoff row above proves the transcript reaches the ASSEMBLED CONTEXT.
  # This one proves it reaches the MODEL, which is a different claim: a context
  # that assembles correctly and is then dropped on the way to the engine looks
  # identical from the preview. The assertion reads what the engine actually
  # received, not what the CLI reported having sent.
  #
  # Asserted in both directions. "Carries the transcript" alone is satisfied by a
  # mode that carries everything, so the essence must be absent for the claim to
  # mean "resumed via the transcript" rather than "resumed via something". The
  # negative half requires the recording to be non-empty, so a launch that never
  # happened cannot pass by having no marker in it.
  #
  # NO --distill TWIN HERE, deliberately. The two modes reach the model by
  # structurally different routes: full resume folds into the assembled context,
  # while --distill rides CTXLOOM_RESUMED_FROM/PARTS and a SessionStart hook and
  # never passes through the launch payload at all. So the mock's recorded input
  # — which captures that payload — is the wrong instrument for the distilled
  # half, and a row asserting it there fails for a reason that says nothing about
  # --distill. The distilled half needs `the hook's additionalContext contains`
  # (session_hooks.feature) driven with those env vars set, which no step can do
  # yet.
  Scenario: Resuming without --distill puts the conversation in front of the model, not the conclusion
    Given the mock LLM responds "MOCK-REPLY"
    When I run "ctxloom run --one-shot --session amber-quiet-heron -p default Remind me what we concluded."
    Then the command succeeds
    And the mock recorded input contains "J001200-TRANSCRIPT-ONLY-MARKER"
    And the mock recorded input does not contain "J001200-ESSENCE-WORKTREE-NAMING-DECISION"

  # A FILED DEFECT (task diffusive-dazzler), reproduced here at the surface a
  # user actually touches. A harp is three random words; mistyping one is the
  # single most ordinary error available on this command. An unknown harp used
  # to take the process down with a nil dereference — operations.GetSession
  # returns (nil, nil) for an absent harp and RecordedSessionEntries
  # dereferenced entry.SessionID without checking — while the UNBOUND case
  # (a harp that exists with no transcript) degraded correctly. The contract
  # resumeFullContext documents for itself covers both halves; only one was
  # honoured.
  #
  # UNTAGGED: RecordedSessionEntries now returns an error naming the harp and
  # pointing at `session list`, and resumeFullContext's existing warn-and-carry-
  # on path does the rest. Fixed at the shared primitive, so the ACP resume path
  # and the coordinator's ended-child resume — which had the same hole — are
  # covered by the same change.
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
  # UNTAGGED 2026-08-05. Two findings on the way to green, both fixed here
  # rather than deferred, neither a production change:
  #   1. The step table's key was wrong: `harp` instead of the
  #      loadSessionInput field's actual JSON name `harp_name`. The MCP
  #      server's schema has additionalProperties:false, so this failed loud
  #      (a validation error, not a silent miss) — caught immediately, not a
  #      months-later discovery, but still worth naming since it's exactly the
  #      "unasserted payload" risk this comment used to warn about.
  #   2. The deliberate answer IS "load_session returns the ESSENCE, not the
  #      raw conversation" — confirmed by mcp_tools.feature's own
  #      "Load a prior session's essence over MCP" scenario, which names it
  #      "essence" in its title and asserts essence content. cli.loadHarpEssence
  #      reads ~/.ctxloom/sessions/<harp>/essence.md directly; it never touches
  #      the raw transcript. So per this scenario's own standing guidance, the
  #      marker changed (J001200-TRANSCRIPT-ONLY-MARKER -> the essence marker)
  #      rather than the scenario being deleted: the point was always that
  #      SOME session content comes back, and it does.
  # Mutation: truncating the essence bytes before they reach loadSessionResult
  # (cli.loadHarpEssence's Content field) turns this red.
  Scenario: The assistant can reach the March session without being told where to look
    When the agent calls tool "load_session" with:
      | harp_name | amber-quiet-heron |
    Then the tool result contains "J001200-ESSENCE-WORKTREE-NAMING-DECISION"

  # Recall is a READ. This is the scenario that would catch a resume
  # implementation that consumes, truncates, compacts or re-writes the record it
  # read — a memory that erases itself on use, which is worse than no memory
  # because it fails on the SECOND person to ask the question.
  #
  # UNTAGGED 2026-08-05, confirmed to pass as written AND to bite — with a
  # measured blind spot recorded here rather than left to be rediscovered.
  #
  # Mutation that kills it: deleting the canonical transcript at the TOP of
  # operations.RecordedSessionEntries, before anything reads it. So the subject
  # genuinely runs and the assertion genuinely reads the bytes on disk; this is
  # not a row satisfied by nothing happening.
  #
  # THE BLIND SPOT, RE-MEASURED 2026-08-05 AND NOW CLOSED: it used to be true
  # that a deletion placed AFTER the (then-only) backend session read
  # succeeded would NOT turn this red, because in this fixture that read FAILS
  # (the mock backend has no `seeded-amber-quiet-heron`) and the resume warned
  # and gave up before reaching any code downstream of it. That was inherited
  # from "resuming the March session" two rows up, which has since been fixed:
  # RecordedSessionEntries now reads entry.CanonicalTranscriptPath FIRST (the
  # path that actually runs in this fixture) and only falls back to the
  # backend when no canonical transcript exists. Re-measured with an
  # os.Remove(entry.CanonicalTranscriptPath) placed immediately after a
  # successful transcript.ParseTranscriptFile — the position a consuming
  # resume would actually occupy now — and it DOES turn this row red. The gap
  # is closed, not merely narrowed: this guard now watches the path every
  # normal resume of a canonical-backed session actually takes.
  Scenario: Recalling a session does not consume it
    When I run "ctxloom run --session amber-quiet-heron --dry-run -p default"
    Then the canonical transcript she recalled from is still on disk, untouched
