@doc
Feature: Cross-engine transcript capture — every engine's native log becomes one transcript you own

  You drive whichever engine you like — claude, codex, kiro —
  through its own native TUI, and you still keep a durable memory of what
  happened. Each engine writes its conversation into its OWN private,
  version-unstable session store (claude's project JSONL, codex's rollout
  JSONL, kiro's sqlite conversations_v2). This journey
  is the promise on top of that mess: whatever engine ran, its native log
  becomes ONE canonical transcript ctxloom owns — the same on-disk schema
  (transcript.jsonl, one JSONL Record per line) a live structured/ACP session
  already tees out — so a single downstream reader (distill, resume, the VSCode
  companion) never special-cases three vendor formats.

  AND NOBODY RUNS AN IMPORT. The conversion fires at the two moments it is
  needed and nowhere else: when an interactive run exits, and when someone
  reaches for the memory of a session that was never captured. There is no
  import chore to remember, because the moment you need the memory back is the
  moment you have the least context to spare for one.

  COMMAND SURFACES THIS FILE COVERS: the recover_session and load_session MCP
  tools, and — under @live — an interactive `ctxloom run`.

  WHY IT MATTERS: without this seam, an engine driven through its own pty is a
  black hole — the structured tee (transcript.Tee/TeeAndClose) never reaches a
  pty, so ctxloom has zero memory of that work. WHAT BREAKS WITHOUT IT: every
  interactive session, and every session that ran before capture existed, is
  unresumable and undistillable — exactly the interactive-pty gap
  docs/transcript-schema.md §8 exists to close.

  # WHAT THIS JOURNEY CAN AND CANNOT SEE (honesty, mirroring j002100/j002200's
  # own notes). Grounded by reading the wiring and its readers, not guessed:
  #
  #   * operations.ConvertVendorTranscript is the ONE function both triggers
  #     call — the interactive-pty exit seam in cli.runRun and the
  #     recover_session tool — so a scenario driving either trigger exercises
  #     the same conversion. The hermetic scenarios drive the RECALL trigger
  #     because it needs no engine binary and no pty.
  #
  #   * The suite drives the real ctxloom MCP server as a subprocess inside an
  #     isolated HOME, and can seed the session index directly WITHOUT
  #     launching any backend. Every hermetic scenario below builds on exactly
  #     that: seed an index entry whose transcript_path points at a
  #     vendor-native fixture this repo ALREADY ships in-tree
  #     (internal/transcript/vendorreader/{claude,codex}/testdata/
  #     *-fixture.jsonl), then ask for that session's memory. The conversion
  #     (vendorreader.VendorAdapter.Convert) is a PURE file->file transform
  #     through a transcript.Recorder — it spawns no engine — so this is
  #     honestly green with no live binary.
  #
  #   * Each recall names its engine explicitly, so the read resolves through
  #     the canonical store rather than through whichever backend the fixture
  #     project happens to default to. The three reader engines are
  #     retired-scraper backends (grpc.IsRetiredScraperBackend), so a canonical
  #     miss is a clean "nothing captured" that triggers the conversion instead
  #     of a scrape attempt that would mask it.
  #
  #   * The mock backend the suite CAN drive hermetically has NO entry in
  #     vendorreader.go's vendorReaderRegistry (only claude-code/codex/kiro
  #     are registered), because it has no vendor-native store to
  #     read back. That boundary is asserted as its own scenario below rather
  #     than left in this comment.
  #
  #   * The canonical Engine field carries the BACKEND REGISTRY name, not the
  #     reader's short package name — the conversion stamps
  #     transcript.NewRecorder(harp, entry.Backend), and vendorreader.go's
  #     registry comment makes this load-bearing on purpose (a harp's oneshot
  #     and interactive lines must not disagree about who wrote them). So a
  #     converted CLAUDE transcript's engine reads "claude-code" (NOT the
  #     "claude" its package testdata golden happens to show); codex reads
  #     "codex". The assertions below pin the
  #     registry name deliberately.
  #
  #   * Canonical filename: transcript.jsonl
  #     (paths.CanonicalTranscriptFileName). Reads resolve via
  #     paths.ResolveHarpCanonicalTranscriptPath, which falls back to the
  #     pre-rename leaf name only when the current one is absent — the step
  #     definitions below mirror that same fallback rather than hard-coding
  #     either leaf name.
  #
  # DELIBERATELY OUT OF HERMETIC SCOPE (see the @live row and this note):
  #   1. The ON-EXIT conversion for a REAL interactive-pty session needs a real
  #      engine binary AND a pty; the mock never reaches that branch. Proven
  #      only under @live, which self-skips without credentials — never faked
  #      here. The recall trigger above proves the identical conversion from
  #      the other moment.
  #   2. KIRO's conversion is real code (vendorreader_kiro.go's
  #      locateKiroConversation over the conversations_v2 sqlite store) but is
  #      NOT hermetically seeded below: kiroDBPath reads a fixed
  #      $XDG_DATA_HOME/kiro-cli/data.sqlite3 rather than the index entry's
  #      transcript_path, and the no-bound-id path falls back to a best-effort
  #      WorkDir/UpdatedAt enumeration heuristic that two concurrent same-dir
  #      sessions defeat. Documented as a deferral, not written as a weak
  #      scenario. See the note above the Examples table.

  Background:
    Given an initialized ctxloom project

  # THE CRUX CLAIM, with the exact real payloads verified against
  # internal/transcript/vendorreader/claude/testdata/ and its MANIFEST.json (a
  # real interrupted docs-audit session on this box). A claude-code session's
  # native project JSONL becomes the canonical schema through the real
  # claudereader.VendorAdapter: its user turn, its assistant reply, its
  # Glob/Bash tool_use calls and the is_error tool_result all land as ordered
  # Records — and the Engine field is the BACKEND name "claude-code", not the
  # reader's "claude" shorthand.
  Scenario: A prior claude session's own log comes back as the transcript you own
    Given a recorded "claude-code" session "amber-claude-harp" bound to the shipped claude vendor-transcript fixture
    And the compaction LLM is a mock that never compresses
    When the assistant recovers the memory of session "amber-claude-harp"
    Then the tool call succeeds
    And the canonical transcript for "amber-claude-harp" replays the fixture's real turns in order: the user goal, the assistant reply, and the Glob and Bash tool calls with their real inputs
    And the canonical transcript for "amber-claude-harp" preserves the tool_result marked is_error from the rejected call
    And every line of the canonical transcript for "amber-claude-harp" is stamped engine "claude-code"

  # ATTRIBUTION, not just presence. A transcript that merely CONTAINS the right
  # text is not enough: claude's own message queue lets a second prompt be typed
  # and delivered WHILE the first response is still generating (queue-operation
  # type "enqueue"/"dequeue" — confirmed against a real 2298-record session on
  # this box: 115 enqueues, 36 dequeues, 77 removes, messages queued and never
  # delivered). Naive positional pairing ("the assistant text right after a user
  # line answers that user line") gets this wrong the moment a second prompt is
  # delivered mid-response: the real answer to the FIRST prompt can land in the
  # vendor file AFTER the interleaved second prompt's own "user" line. The
  # reader never does that kind of pairing — it streams user/assistant lines
  # straight through in the vendor file's own order — so the real chronology
  # survives instead of being silently reshuffled. This also pins the OTHER half
  # of the same honesty: a queued message REMOVED before delivery (claude's
  # "remove" queue-operation) never gets a "user" line of its own at all, so it
  # must never surface as a phantom turn either.
  Scenario: A prompt delivered mid-response is not misattributed to the wrong answer, and a removed prompt never becomes a phantom turn
    Given a recorded "claude-code" session "queued-claude-harp" bound to a vendor log where a second prompt is delivered mid-response and a third is queued then removed
    And the compaction LLM is a mock that never compresses
    When the assistant recovers the memory of session "queued-claude-harp"
    Then the canonical transcript for "queued-claude-harp" places the real answer to the first prompt after the interleaved second prompt, exactly as the vendor log ordered them
    And the canonical transcript for "queued-claude-harp" contains no trace of the removed, never-delivered prompt

  # The SAME conversion holds for an engine whose vendor store is a bare
  # per-session file (locateBoundTranscript). codex's shipped golden was
  # checked: its rollout carries a session line (model gpt-5.5) then
  # user/tool_use/... turns. The shared step asserts the fixture's own real
  # turns replayed in seq order.
  Scenario Outline: A prior <engine> session's own log comes back as the transcript you own
    Given a recorded "<engine>" session "<harp>" bound to the shipped <engine> vendor-transcript fixture
    And the compaction LLM is a mock that never compresses
    When the assistant recovers the memory of session "<harp>"
    Then the canonical transcript for "<harp>" replays the <engine> fixture's real conversation turns in seq order
    And every line of the canonical transcript for "<harp>" is stamped engine "<engine>"

    # KIRO is intentionally absent from this table — see deferral (2) in the
    # honesty block: its conversations_v2 sqlite store is resolved via a fixed
    # $XDG_DATA_HOME path plus a best-effort enumeration heuristic, not the
    # index entry's transcript_path, so it cannot be seeded by the same
    # file-path fixture route this one uses. Its reader path is real
    # (vendorreader_kiro.go) but needs a real sqlite fixture plus @live-style
    # setup this journey does not yet own.
    Examples:
      | engine      | harp                    |
      | codex       | amber-codex-harp        |

  # "ONE canonical transcript you own", asserted against the failure mode that
  # would break it. A recall of a session that is still being written re-reads
  # the engine's own store from the top, because no adapter can resume from an
  # offset — so the replacement is written to a temporary sibling and renamed
  # into place. Appending instead would leave a second full copy of the
  # conversation bolted onto the first, and every downstream reader would see
  # the session happen twice. The assertion is structural rather than
  # byte-for-byte on purpose: each conversion stamps its own record timestamps,
  # so identical BYTES would be the wrong invariant to demand, while a doubled
  # record count or a repeated seq is exactly the damage worth catching.
  Scenario: Recovering the same memory twice never doubles the conversation
    Given a recorded "codex" session "keen-codex-harp" bound to the shipped codex vendor-transcript fixture
    And the compaction LLM is a mock that never compresses
    When the assistant recovers the memory of session "keen-codex-harp"
    And I note the conversation the canonical transcript for "keen-codex-harp" holds
    And the assistant recovers the memory of session "keen-codex-harp"
    Then the canonical transcript for "keen-codex-harp" still holds exactly that conversation, not a second copy of it
    And the canonical transcript for "keen-codex-harp" replays the codex fixture's real conversation turns in seq order

  # THE CAPTURED TRUTH WINS. Loading a FINISHED session serves what ctxloom
  # already captured — the live structured/ACP tee's own record — and never
  # re-derives it from the engine's private copy, even when that copy is right
  # there and readable. A finished transcript cannot change, so re-reading the
  # vendor store buys nothing and risks replacing a first-party record with a
  # vendor-shaped approximation of it.
  #
  # This is the archived door specifically. Recovering a session that is still
  # BEING WRITTEN deliberately does the opposite (the scenario above), because
  # its source keeps growing and a transcript frozen at first-read is worse
  # than no transcript: it is indistinguishable from a complete one.
  Scenario: A session already captured live is served from its own record, not the engine's
    Given a recorded "claude-code" session "bright-claude-harp" that already has a canonical transcript and a shipped claude vendor fixture
    And the compaction LLM is a mock that never compresses
    When the assistant loads the finished session "bright-claude-harp"
    Then the canonical transcript for "bright-claude-harp" still holds its original captured content

  # THE HONEST BOUNDARY, asserted as behaviour rather than as a comment. An
  # engine with no vendor-native store has no reader in vendorReaderRegistry,
  # so there is nothing to convert. ctxloom says the session did not load
  # instead of inventing a transcript for it — a fabricated empty transcript
  # would be indistinguishable from a real capture to every downstream reader,
  # and would permanently satisfy the "already captured" guard.
  Scenario: A session run on an engine ctxloom cannot read back says so, and invents nothing
    Given a recorded "mock" session "plain-mock-harp"
    When the assistant recovers the memory of session "plain-mock-harp"
    Then the tool call succeeds
    And the tool result field "loaded" equals "false"
    And no canonical transcript is written for "plain-mock-harp"

  # @live — the ON-EXIT trigger is only real with a real engine driven through
  # its own pty. The conversion fires solely for an interactive execution mode
  # with a registered backend; the mock never reaches it. This row self-skips
  # when no credentials for <agent> are present (the same gate every @live
  # scenario opens with), so it is a documented, non-faked deferral of the
  # on-exit half — the recall scenarios above prove the identical conversion
  # from the other trigger.
  @live
  Scenario Outline: An interactive engine run's own log is captured when it exits
    Given a real <agent> agent is available
    When I run the <agent> agent interactively to completion with prompt "leave a mark"
    Then on exit the canonical transcript for that session holds its real conversation turns
    And every line of that canonical transcript is stamped engine "<engine>"

    Examples:
      | agent       | engine      |
      | Claude      | claude-code |
      | Codex       | codex       |
