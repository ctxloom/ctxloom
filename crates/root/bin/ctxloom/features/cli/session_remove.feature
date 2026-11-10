# world: base
# Ported from cli/session.feature
# Ported from journeys/j001300_closeout.feature
# NOT ported: Go's other session destroyers. `session purge` (owner ruling
#   98: one deleting verb), `session transcript purge` and `session worktrees
#   purge` are retired: `session remove` is the one destroyer, and a removed
#   session is gone, from the store and from disk. The age form (`--older-than`,
#   a configured `session_purge_age`) is `session sweep`'s, not here. The
#   earlier design of this file (a `--force` override; a session holding a
#   person's files refused whole) is dropped (owner ruling 104): the built
#   verb's one override is `--even-if-live`, and a person's files go with the
#   session after a warning that names each. `session remove` is built only
#   with the `session-destroy` feature, which the acceptance tests turn on.
Feature: session remove — the one destroyer, and a removed session is gone

  `ctxloom session remove <harp>` destroys one session: its events and its
  runs' in the store, its directory and its lock. Afterwards it is not in
  `session list`, and its name is never given to another session. The
  session's output dir (its plans, any summary) is the human's and is never
  touched.

  Without `--yes` remove only reports: it changes nothing and names the exact
  command that would.

  Background:
    # new: Rust's own Background, stating the Given each scenario shares.
    Given an initialized ctxloom project

  Rule: Remove reports first, and with --yes the session is gone

    Scenario: Remove reports before it destroys
      # new: the report-only run of the built verb; it changes nothing and names the command that applies it.
      Given a finished session "amber-swift-owl" with a transcript
      When I run "ctxloom session remove amber-swift-owl"
      Then the command succeeds
      And the output contains "removed nothing"
      And the output contains "ctxloom session remove amber-swift-owl --yes"
      When I run "ctxloom session list --all"
      Then the output contains "amber-swift-owl"

    Scenario: --yes destroys the session and leaves its output dir
      # new: the destroy (owner rulings 94b, 98): its lock goes with it, the output dir is the human's.
      Given a finished session "amber-swift-owl" with a transcript and a summary an older ctxloom left
      When I run "ctxloom session remove amber-swift-owl --yes"
      Then the command succeeds
      And the file ".ctxloom/sessions/amber-swift-owl.lock" does not exist
      And the output file "essence.md" of session "amber-swift-owl" exists
      When I run "ctxloom session list --all"
      Then the output does not contain "amber-swift-owl"

  Rule: Only a session whose lock proves it ended is removed, unless --even-if-live

    The session lock only ever refuses. A free lock proves the owner dead; a
    held lock, or no lock at all, refuses: "cannot determine" is never
    permission. The refusal names the exact command that removes it
    deliberately, `--even-if-live`.

    Scenario: A session the lock cannot prove finished is refused, and the escape is named
      # new: the built refusal; `--even-if-live` is the one override (owner ruling 104).
      Given a finished session "amber-swift-owl" with a transcript but no session lock
      When I run "ctxloom session remove amber-swift-owl --yes"
      Then the command exits with code 2
      And the output contains "cannot be proven dead"
      And the output contains "ctxloom session remove amber-swift-owl --yes --even-if-live"
      When I run "ctxloom session list --all"
      Then the output contains "amber-swift-owl"

    Scenario: --even-if-live removes it anyway
      # new: the override, applied.
      Given a finished session "amber-swift-owl" with a transcript but no session lock
      When I run "ctxloom session remove amber-swift-owl --yes --even-if-live"
      Then the command succeeds
      When I run "ctxloom session list --all"
      Then the output does not contain "amber-swift-owl"

    Scenario: A session running again is refused
      # new: the index says ended; the lock says running, and the lock wins.
      Given a session "amber-swift-owl" with a transcript that is running again
      When I run "ctxloom session remove amber-swift-owl --yes"
      Then the command exits with code 2
      And the output contains "owner is alive"
      And the output contains "--even-if-live"

  Rule: A transcript is the session's record, kept until its retention passes

    Engines compact their own sessions, so a session's transcript is its only
    record. Remove refuses a session that ended within
    session_transcript_retention (30 days unless configured), and one marked
    keep at any age. No flag overrides it: `ctxloom session keep <harp> --undo`
    releases a kept one, and time releases the rest.

    Scenario: A session within its retention is refused
      # new: the built retention refusal.
      Given a session "brisk-copper-moth" with a transcript that finished an hour ago
      When I run "ctxloom session remove brisk-copper-moth --yes"
      Then the command exits with code 2
      And the output contains "retention"
      When I run "ctxloom session list --all"
      Then the output contains "brisk-copper-moth"

    Scenario: A kept session is refused at any age, and --undo releases it
      # new: keep and its release, then the removal.
      Given a finished session "amber-swift-owl" marked keep
      When I run "ctxloom session remove amber-swift-owl --yes"
      Then the command exits with code 2
      And the output contains "ctxloom session keep amber-swift-owl --undo"
      When I run "ctxloom session keep amber-swift-owl --undo"
      Then the command succeeds
      When I run "ctxloom session remove amber-swift-owl --yes"
      Then the command succeeds

  Rule: A person's files go with the session, and remove names each first

    A session's directory may hold files a person wrote (notes nobody filed).
    Remove takes them with the session, so it warns first, naming each: in the
    report, on --yes, and with --even-if-live alike. The warning is a notice,
    never a refusal (owner ruling 104).

    Scenario: Removing a session that holds a person's file warns, naming it
      # new: owner ruling 104.
      Given a finished session "amber-quiet-heron" with a transcript
      And session "amber-quiet-heron" holds a file "design-notes.md" a person wrote
      When I run "ctxloom session remove amber-quiet-heron --yes"
      Then the command succeeds
      And the output contains "a person wrote"
      And the output contains "design-notes.md"
      And the file ".ctxloom/sessions/amber-quiet-heron/design-notes.md" does not exist

    Scenario: The report warns of them too, and destroys nothing
      # new: owner ruling 104, report-only.
      Given a finished session "amber-quiet-heron" with a transcript
      And session "amber-quiet-heron" holds a file "design-notes.md" a person wrote
      When I run "ctxloom session remove amber-quiet-heron"
      Then the command succeeds
      And the output contains "a person wrote"
      And the file ".ctxloom/sessions/amber-quiet-heron/design-notes.md" exists

    Scenario: --even-if-live warns the same before it destroys them
      # new: owner ruling 104: the override does not silence the warning.
      Given a finished session "amber-quiet-heron" with a transcript but no session lock
      And session "amber-quiet-heron" holds a file "design-notes.md" a person wrote
      When I run "ctxloom session remove amber-quiet-heron --yes --even-if-live"
      Then the command succeeds
      And the output contains "a person wrote"
      And the output contains "design-notes.md"
      And the file ".ctxloom/sessions/amber-quiet-heron/design-notes.md" does not exist
