@doc
Feature: Plain `session compact` drives a real transcript through the distiller and persists it

  A session ends. Its transcript sits on disk, and nothing about it is
  searchable, summarizable, or resumable until something reads it and writes
  down what happened. `ctxloom session compact <harp>` is that something: the
  plain, no-flag form of the command a developer runs by hand, to turn a raw
  transcript into the essence.md
  that `session show`, `session search`, and `run --session --compact` all
  read from afterward (see j001200_recall.feature, which owns that recall half).
  Without a working `compact`, none of recall's payoff exists — there is
  nothing for it to read.

  This journey proves the one claim that sits upstream of all of that and
  currently has zero executing coverage: that plain `compact` actually does
  its job. Two things have to both be true, and either one failing silently
  would leave a session that LOOKS compacted but carries nothing real —
  1. the session's own transcript is what gets sent to the compacting
     backend (not an empty prompt, not another session's content), and
  2. what the backend returns is what lands in essence.md (not discarded,
     not silently replaced by a stale or placeholder essence).

  # HONESTY NOTE. `session compact` also accepts --skill/--to-bundle for
  # extracting reusable lessons into a bundle (j001300_closeout.feature's @wip
  # rows) — that surface does not exist yet and is out of scope here. This
  # journey covers only the flagless command every other compaction path
  # (list --compact, --skill extraction once it lands)
  # ultimately funnels through: internal/adapters/cli/session_compact.go's
  # compactEntry -> internal/adapters/memory/compactor.go's Compact.
  #
  # THE SILENT-NO-OP TRAP THIS GUARDS AGAINST. Compact short-circuits an
  # EMPTY session (or one with an existing essence and nothing new to add)
  # to a plain dump that touches no LLM at all — correct behavior for that
  # case, but indistinguishable from a real compaction if a scenario only
  # checks "essence.md exists" or "the command printed `essence: <path>`"
  # (that line is the command's own report of its effect, not independent
  # evidence of it). This scenario seeds a genuinely non-empty transcript and
  # checks the mock backend's OWN recorded prompt and the essence's actual
  # persisted bytes, not the command's say-so.

  Background:
    Given a project whose mock engine is both its primary and its compaction backend
    And an earlier session "quiet-ember-forge" left a real, non-empty transcript on disk

  Scenario: Plain session compact sends the real transcript to the distiller and persists what comes back
    Given the mock distiller is configured to respond "J001100-COMPACTED-BODY-PERSISTED: the decision was to cache by ETag"
    When Alice compacts the session she just left:
      """
      ctxloom session compact quiet-ember-forge
      """
    Then the command succeeds
    And the mock recorded input contains "J001100-TRANSCRIPT-REACHED-COMPACTOR"
    And the persisted essence for "quiet-ember-forge" contains "J001100-COMPACTED-BODY-PERSISTED: the decision was to cache by ETag"
