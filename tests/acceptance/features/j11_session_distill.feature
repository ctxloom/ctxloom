@doc
Feature: Plain `session distill` drives a real transcript through the distiller and persists it

  A session ends. Its transcript sits on disk, and nothing about it is
  searchable, summarizable, or resumable until something reads it and writes
  down what happened. `ctxloom session distill <harp>` is that something: the
  plain, no-flag form of the command a developer runs by hand, or the resume
  picker runs on their behalf, to turn a raw transcript into the essence.md
  that `session show`, `session search`, and `run --session --distill` all
  read from afterward (see j12_recall.feature, which owns that recall half).
  Without a working `distill`, none of recall's payoff exists — there is
  nothing for it to read.

  This journey proves the one claim that sits upstream of all of that and
  currently has zero executing coverage: that plain `distill` actually does
  its job. Two things have to both be true, and either one failing silently
  would leave a session that LOOKS distilled but carries nothing real —
  1. the session's own transcript is what gets sent to the distilling
     backend (not an empty prompt, not another session's content), and
  2. what the backend returns is what lands in essence.md (not discarded,
     not silently replaced by a stale or placeholder essence).

  # HONESTY NOTE. `session distill` also accepts --skill/--to-bundle for
  # extracting reusable lessons into a bundle (j13_closeout.feature's @wip
  # rows) — that surface does not exist yet and is out of scope here. This
  # journey covers only the flagless command every other distillation path
  # (list --distill, the resume picker, --skill extraction once it lands)
  # ultimately funnels through: internal/cli/session_distill.go's
  # compactEntry -> internal/memory/compactor.go's Compact.
  #
  # THE SILENT-NO-OP TRAP THIS GUARDS AGAINST. Compact short-circuits an
  # EMPTY session (or one with an existing essence and nothing new to add)
  # to a plain dump that touches no LLM at all — correct behavior for that
  # case, but indistinguishable from a real distillation if a scenario only
  # checks "essence.md exists" or "the command printed `essence: <path>`"
  # (that line is the command's own report of its effect, not independent
  # evidence of it). This scenario seeds a genuinely non-empty transcript and
  # checks the mock backend's OWN recorded prompt and the essence's actual
  # persisted bytes, not the command's say-so.

  Background:
    Given a project whose mock engine is both its primary and its distillation backend
    And an earlier session "quiet-ember-forge" left a real, non-empty transcript on disk

  Scenario: Plain session distill sends the real transcript to the distiller and persists what comes back
    Given the mock distiller is configured to respond "J11-DISTILLED-BODY-PERSISTED: the decision was to cache by ETag"
    When I run "ctxloom session distill quiet-ember-forge"
    Then the command succeeds
    And the mock recorded input contains "J11-TRANSCRIPT-REACHED-DISTILLER"
    And the persisted essence for "quiet-ember-forge" contains "J11-DISTILLED-BODY-PERSISTED: the decision was to cache by ETag"
