@doc
Feature: Cross-engine transcript capture — every engine's native log becomes one transcript you own

  ctxloom lets you drive whichever engine you like — claude, codex, kiro,
  antigravity — through its own native TUI, and still keep a durable memory of
  what happened. Each engine writes its conversation to its OWN private,
  version-unstable session store (claude's project JSONL, codex's rollout
  JSONL, kiro's sqlite conversations_v2, antigravity's brain log). This journey
  proves the promise on top of that mess: whatever engine ran, its native log
  is converted into ONE canonical transcript ctxloom owns — the same on-disk
  schema (transcript.jsonl, one JSONL Record per line) a live structured/ACP
  session already tees out — so a single downstream reader (distill, resume,
  the VSCode companion) never has to special-case four vendor formats. The
  conversion fires automatically when an interactive run exits, and any prior
  session ctxloom never captured can be backfilled after the fact.

  WHY IT MATTERS: without this seam, an engine driven through its own pty is a
  black hole — the structured tee (transcript.Tee/TeeAndClose) never reaches a
  pty, so ctxloom would have zero memory of that work. WHAT BREAKS WITHOUT IT:
  every interactive session, and every session that ran before capture existed,
  is unresumable and undistillable — exactly the interactive-pty gap
  docs/transcript-schema.md §8 (task petty-green) exists to close.

  # WHAT THIS JOURNEY CAN AND CANNOT SEE (honesty, mirroring j6/j9's own
  # notes). Grounded by reading the writer-a wiring (anchor commit 6e63ae1)
  # and its readers, not guessed:
  #
  #   * The suite drives a real `ctxloom` SUBPROCESS and can seed the home
  #     session index directly, WITHOUT launching any backend — the existing
  #     `a recorded session "<harp>"` step already writes index.yaml by hand
  #     (tests/acceptance/steps_fixture.go's fixture step, backend:
  #     claude-code, no engine spawned). Every hermetic scenario below builds
  #     on exactly that: seed an index entry whose transcript_path points at a
  #     vendor-native fixture this repo ALREADY ships in-tree
  #     (internal/transcript/vendorreader/{claude,codex,antigravity}/testdata/
  #     *-fixture.{jsonl,json}), then run `ctxloom session backfill`. The
  #     conversion (reader.VendorAdapter.Convert, adapter.go) is a PURE
  #     file->file transform through a transcript.Recorder — it spawns no
  #     engine — so this is honestly green with no live binary.
  #
  #   * The built-in `mock` backend the suite CAN drive hermetically is a bare
  #     echo, and it has NO entry in vendorimport.go's vendorImportRegistry
  #     (only claude-code/codex/kiro/antigravity are registered). So a live
  #     hermetic run NEVER exercises the reader — mock has no vendor-native
  #     store to import from. That is not a gap we paper over: it is asserted
  #     as its own scenario below ("A mock-engine session has nothing to
  #     import").
  #
  #   * The canonical Engine field carries the BACKEND REGISTRY name, not the
  #     reader's short package name — ConvertVendorTranscript stamps
  #     transcript.NewRecorder(harp, e.Backend), and vendorimport.go's registry
  #     comment makes this load-bearing on purpose (a harp's oneshot and
  #     interactive lines must not disagree about who wrote them). So a
  #     backfilled CLAUDE transcript's engine reads "claude-code" (NOT the
  #     "claude" its package testdata golden happens to show); codex reads
  #     "codex", antigravity "antigravity". The assertions below pin the
  #     registry name deliberately.
  #
  #   * Canonical filename: transcript.jsonl (paths.CanonicalTranscriptFileName;
  #     renamed from transcript.acp.jsonl by 5f4d3b4c, already landed on this
  #     branch). Reads resolve via paths.ResolveHarpCanonicalTranscriptPath
  #     (internal/paths/paths.go:282), which falls back to the pre-rename leaf
  #     name only when the current one is absent — the step definitions below
  #     mirror that same fallback rather than hard-coding either leaf name, so
  #     they keep working regardless of which name a given harp's file landed
  #     under.
  #
  # DELIBERATELY OUT OF HERMETIC SCOPE (see the @live row and deferral notes):
  #   1. The ON-EXIT conversion for a REAL interactive-pty session (run.go's
  #      convertVendorTranscriptOnExit, gated on ExecutionMode_INTERACTIVE +
  #      a registered backend) needs a real engine binary AND a pty. The mock
  #      never reaches that branch. Proven only under @live, which self-skips
  #      without credentials — never faked here.
  #   2. KIRO backfill is real code (vendorimport_kiro.go's
  #      locateKiroConversation over the conversations_v2 sqlite store) but is
  #      NOT hermetically seeded below: kiroDBPath reads a fixed
  #      $XDG_DATA_HOME/kiro-cli/data.sqlite3 (not the index entry's
  #      transcript_path), and the no-bound-id path falls back to a
  #      best-effort WorkDir/UpdatedAt enumeration heuristic that two
  #      concurrent same-dir sessions defeat. Documented as a deferral, not
  #      written as a weak scenario. See the note above the Examples table.

  Background:
    Given an initialized ctxloom project

  # LOCKED — the crux claim, with the exact real payloads verified against
  # internal/transcript/vendorreader/claude/testdata/golden.transcript.acp.jsonl and
  # its MANIFEST.json (a real interrupted docs-audit session on this box). A
  # claude-code session's native project JSONL is converted, through the real
  # claudereader.VendorAdapter, into the canonical schema: its user turn, its
  # thinking block, its assistant reply, its Glob/Bash tool_use calls and the
  # is_error tool_result all land as ordered Records — and the Engine field is
  # the BACKEND name "claude-code", not the reader's "claude" shorthand.
  Scenario: A prior claude session's native log becomes the canonical transcript you own
    Given a recorded "claude-code" session "amber-claude-harp" bound to the shipped claude vendor-transcript fixture
    When I run "ctxloom session backfill amber-claude-harp"
    Then the canonical transcript for "amber-claude-harp" replays the fixture's real turns in order: the user goal, the assistant reply, and the Glob and Bash tool calls with their real inputs
    And the canonical transcript for "amber-claude-harp" preserves the tool_result marked is_error from the rejected call
    And every line of the canonical transcript for "amber-claude-harp" is stamped engine "claude-code"

  # ADDED (not in the original draft) — attribution, not just presence. A
  # transcript that merely CONTAINS the right text is not enough: claude's own
  # message queue lets a second prompt get typed and delivered WHILE the first
  # response is still generating (queue-operation type "enqueue"/"dequeue" —
  # confirmed against a real 2298-record session on this box: 115 enqueues, 36
  # dequeues, 77 removes — messages queued and never delivered). Naive
  # positional pairing ("the assistant text right after a user line answers
  # that user line") gets this wrong the moment a second prompt is delivered
  # mid-response: the real answer to the FIRST prompt can land in the vendor
  # file AFTER the interleaved second prompt's own "user" line. The reader
  # never does that kind of pairing — it streams user/assistant lines straight
  # through in the vendor file's own order (session.go's convertLines) — so
  # the real chronology survives instead of being silently reshuffled. This
  # also pins the OTHER half of the same honesty: a queued message that gets
  # REMOVED before delivery (claude's "remove" queue-operation) never gets a
  # "user" line of its own at all — confirmed by inspecting a real removed
  # entry's neighbors on this box — so it must never surface as a phantom turn
  # in the canonical transcript either.
  Scenario: A prompt delivered mid-response is not misattributed to the wrong answer, and a removed prompt never becomes a phantom turn
    Given a recorded "claude-code" session "queued-claude-harp" bound to a vendor log where a second prompt is delivered mid-response and a third is queued then removed
    When I run "ctxloom session backfill queued-claude-harp"
    Then the canonical transcript for "queued-claude-harp" places the real answer to the first prompt after the interleaved second prompt, exactly as the vendor log ordered them
    And the canonical transcript for "queued-claude-harp" contains no trace of the removed, never-delivered prompt

  # LOCKED — the SAME conversion holds across engines whose vendor store is a
  # bare per-session file (locateBoundTranscript in vendorimport.go, shared by
  # codex and antigravity). Each engine's shipped golden was checked: codex's
  # rollout carries a session line (model gpt-5.5) then user/tool_use/... turns;
  # antigravity's brain log carries no session id and no session line at all
  # (it has no StructuredChat capability — record.go), starting straight at a
  # user entry. The shared step asserts the fixture's own real turns replayed
  # in seq order, so one engine's expectation can never satisfy another's.
  Scenario Outline: A prior <engine> session's native log becomes the canonical transcript you own
    Given a recorded "<engine>" session "<harp>" bound to the shipped <engine> vendor-transcript fixture
    When I run "ctxloom session backfill <harp>"
    Then the canonical transcript for "<harp>" replays the <engine> fixture's real conversation turns in seq order
    And every line of the canonical transcript for "<harp>" is stamped engine "<engine>"

    # KIRO is intentionally absent from this table — see deferral (2) in the
    # honesty block: its conversations_v2 sqlite store is resolved via a fixed
    # $XDG_DATA_HOME path plus a best-effort enumeration heuristic, not the
    # index entry's transcript_path, so it cannot be seeded by the same
    # file-path fixture route these two use. Its reader path is real
    # (vendorimport_kiro.go) but needs a real sqlite fixture + @live-style
    # setup this journey does not yet own.
    Examples:
      | engine      | harp                    |
      | codex       | amber-codex-harp        |
      | antigravity | amber-antigravity-harp  |

  # LOCKED — "ONE canonical transcript you own": the import is idempotent BY
  # NON-REPETITION (vendorimport.go's hasCanonicalTranscript guard). Convert
  # re-reads the vendor source from the top every call and has no resume
  # concept, so a second run MUST be a no-op or it would duplicate every entry.
  # The second backfill reports the harp skipped, and the canonical transcript
  # is byte-identical to the first run's — same line count, same monotonic seq
  # with no gaps.
  Scenario: Backfilling the same session twice never doubles the transcript
    Given a recorded "codex" session "keen-codex-harp" bound to the shipped codex vendor-transcript fixture
    When I run "ctxloom session backfill keen-codex-harp"
    And I remember the canonical transcript for "keen-codex-harp" as "first-import"
    And I run "ctxloom session backfill keen-codex-harp"
    Then the backfill run reports "keen-codex-harp" as skipped, not converted
    And the canonical transcript for "keen-codex-harp" is unchanged from "first-import"

  # LOCKED — a live-captured transcript is never clobbered by a later import.
  # hasCanonicalTranscript short-circuits ConvertVendorTranscript the moment
  # ANY canonical transcript exists for a harp — whether written by the live
  # structured/ACP tee or a prior backfill. A session that already owns a
  # canonical transcript AND still has a resolvable vendor file is left exactly
  # as it was; the vendor log does not overwrite the captured truth.
  Scenario: A session already captured live is left untouched by backfill
    Given a recorded "claude-code" session "bright-claude-harp" that already has a canonical transcript and a shipped claude vendor fixture
    When I run "ctxloom session backfill bright-claude-harp"
    Then the backfill run reports "bright-claude-harp" as skipped, not converted
    And the canonical transcript for "bright-claude-harp" still holds its original captured content

  # LOCKED — the honest boundary, asserted as behavior, not just prose: the one
  # engine the hermetic suite can actually RUN (mock) has no vendor-native
  # store, so it is absent from vendorImportRegistry and backfill treats it as
  # "nothing to import" — skipped, never failed. This is the green counterpart
  # to the honesty block: it proves the registry gate exists rather than
  # asserting it in a comment alone.
  Scenario: A mock-engine session has nothing to import
    Given a recorded "mock" session "plain-mock-harp"
    When I run "ctxloom session backfill plain-mock-harp"
    Then the backfill run reports "plain-mock-harp" as skipped, not converted
    And no canonical transcript is written for "plain-mock-harp"

  # LOCKED — the bulk backfill (no harp argument) walks every indexed session
  # across every project (runSessionBackfill -> ListAllSessions) and never
  # stops early on one entry: BackfillVendorTranscripts warns-and-continues per
  # entry (vendorimport_backfill.go, mirroring distillMissingOrStale's fault
  # tolerance). A convertible session and a non-convertible one in the same
  # index both get their honest verdict — one converted, one skipped — and the
  # summary counts both, so a single dead session can't silently swallow the
  # run.
  Scenario: Bulk backfill converts what it can and accounts for the rest
    Given a recorded "codex" session "one-codex-harp" bound to the shipped codex vendor-transcript fixture
    And a recorded "mock" session "two-mock-harp"
    When I run "ctxloom session backfill"
    Then the canonical transcript for "one-codex-harp" replays the codex fixture's real conversation turns in seq order
    And the backfill summary reports 1 converted and at least 1 skipped
    And no canonical transcript is written for "two-mock-harp"

  # @live — claim #2, "conversion happens on exit for interactive runs", is
  # only real with a real engine driven through its own pty. run.go's
  # convertVendorTranscriptOnExit fires solely on ExecutionMode_INTERACTIVE
  # with a registered backend; the mock never reaches it. This row self-skips
  # when no credentials for <agent> are present (the same gate every @live
  # scenario opens with), so it is a documented, non-faked deferral of the
  # on-exit half — the backfill half above proves the identical conversion
  # from the other trigger.
  @live
  Scenario Outline: An interactive engine run's native log is captured when it exits
    Given a real <agent> agent is available
    When I run the <agent> agent interactively to completion with prompt "leave a mark"
    Then on exit the canonical transcript for that session holds its real conversation turns
    And every line of that canonical transcript is stamped engine "<engine>"

    Examples:
      | agent       | engine      |
      | Claude      | claude-code |
      | Codex       | codex       |
      | Antigravity | antigravity |
