@doc
Feature: session — the record of what your assistant did, and the tools to prune it

  Every `ctxloom run` mints a harp-named session and writes down what
  happened. The `session` noun is how you read that record back and how you
  throw parts of it away. Reading is free; every destroyer reports first and
  changes nothing until you say `--yes`.

  This is the comprehensive per-noun spec: what the noun DOES, leaf by leaf,
  hermetically against a seeded session index. The narrative versions — Alice
  recalling a conversation from March, Alice closing a shipped workstream out
  — are journeys/j001200_recall.feature and j001300_closeout.feature, which
  assert what a PERSON sees.

  Surfaces covered here:

    ctxloom session                     (bare: lists)
    ctxloom session list
    ctxloom session show <harp>
    ctxloom session edit <harp> --name <new>
    ctxloom session delete <harp> [--yes]
    ctxloom session distill <harp>
    ctxloom session search <word>...
    ctxloom session transcript              (bare: lists)
    ctxloom session transcript list [<harp>]
    ctxloom session transcript watch <harp>
    ctxloom session transcript purge <harp> [--undistilled] [--yes]
    ctxloom session artifacts               (bare: lists)
    ctxloom session artifacts list [<harp>]
    ctxloom session artifacts purge <harp> [--yes]
    ctxloom session worktrees               (bare: lists)
    ctxloom session worktrees list [<harp>]
    ctxloom session worktrees purge <harp> [--yes]
    ctxloom session purge <harp> [--yes]

  Rule: Reading the record touches nothing

    Scenario: The bare noun lists what has been recorded
      Given an initialized ctxloom project
      And a recorded session "amber-swift-owl"
      When I run "ctxloom session"
      Then the command succeeds
      And the output contains "amber-swift-owl"

    Scenario: --all reaches past the current project
      Given an initialized ctxloom project
      And a recorded session "amber-swift-owl"
      When I run "ctxloom session list --all"
      Then the command succeeds
      And the output contains "amber-swift-owl"

    Scenario: A fresh project has recorded nothing
      Given an initialized ctxloom project
      When I run "ctxloom session list"
      Then the command succeeds
      And the output contains "no sessions"

    Scenario: Showing a session prints its distilled essence
      Given an initialized ctxloom project
      And a recorded session "amber-swift-owl"
      When I run "ctxloom session show amber-swift-owl"
      Then the command succeeds
      And the output contains "Seeded essence"

  Rule: Renaming is a field assignment, so it rides `edit`

    A session's name is a field on its index entry, not a thing with a verb
    of its own. `edit` is the one mutation verb for the noun, and `--name`
    is the assignment that renames the harp. The backend transcript is
    unaffected: the entry keeps its bound session id, its transcript and its
    essence, and only the name it answers to moves.

    Scenario: --name renames the harp in the index
      Given an initialized ctxloom project
      And a recorded session "amber-swift-owl"
      When I run "ctxloom session edit amber-swift-owl --name bright-keen-hawk"
      Then the command succeeds
      And the output contains "bright-keen-hawk"
      When I run "ctxloom session list --all"
      Then the output contains "bright-keen-hawk"
      And the output does not contain "amber-swift-owl"

    Scenario: The old rename verb is gone, not hidden
      Given an initialized ctxloom project
      And a recorded session "amber-swift-owl"
      When I run "ctxloom session rename amber-swift-owl bright-keen-hawk"
      Then the command fails
      And the output contains "unknown command"

    # A session has no authored document. Its index entry is machine-written
    # and its essence is DERIVED — `session distill` rewrites that file whole —
    # so an editor round-trip here would offer edits the next distillation
    # silently discards. The bare form says so outright and names the
    # assignment it does take, rather than exiting 0 having changed nothing.
    Scenario: A bare edit refuses, because there is no document to open
      Given an initialized ctxloom project
      And a recorded session "amber-swift-owl"
      When I run "ctxloom session edit amber-swift-owl"
      Then the command fails
      And the output contains "no editable document"
      And the output contains "--name"

    Scenario: Editing a session nobody recorded fails
      Given an initialized ctxloom project
      When I run "ctxloom session edit no-such-harp --name bright-keen-hawk"
      Then the command fails
      And the output contains "no-such-harp"

  Rule: The transcript is a population of its own

    A session's recorded conversation has its own listing, its own stream and
    its own destroyer, so it gets its own sub-noun. `session transcript`
    named alone lists — the same bare-noun answer `remote` gives.

    Scenario: The bare sub-noun lists which sessions have a transcript
      Given an initialized ctxloom project
      And a recorded session "amber-swift-owl"
      When I run "ctxloom session transcript"
      Then the command succeeds
      And the output contains "amber-swift-owl"

    # A session whose transcript was never captured is listed SAYING SO.
    # Omitting it would make "nothing was captured" read identically to
    # "there are no sessions".
    Scenario: A session with no captured transcript is still named
      Given an initialized ctxloom project
      And a recorded session "amber-swift-owl"
      When I run "ctxloom session transcript list"
      Then the command succeeds
      And the output contains "amber-swift-owl"
      And the output contains "false"

    Scenario: The harp is a positional, and an unknown one fails
      Given an initialized ctxloom project
      When I run "ctxloom session transcript list no-such-harp"
      Then the command fails
      And the output contains "no-such-harp"

    Scenario: Watching moved under the transcript it watches
      Given an initialized ctxloom project
      And a recorded session "amber-swift-owl"
      When I run "ctxloom session watch amber-swift-owl"
      Then the command fails
      And the output contains "unknown command"

  Rule: Each population owns its destroyer, and every one reports first

    A session holds three separable populations: the conversation it
    recorded, the essence ctxloom derived from it, and the scratch git
    checkouts it left behind. Each is destroyed by the sub-noun that
    understands it, and `session purge` sweeps all three.

    None of them removes anything without `--yes`. A run without it prints
    the plan and then says outright that it removed nothing, naming the exact
    command that would — a plan and an outcome otherwise render identically,
    and the difference is the whole point.

    Scenario: Purging a transcript reports before it removes
      Given an initialized ctxloom project
      And a finished session "amber-swift-owl" with a transcript and an essence
      When I run "ctxloom session transcript purge amber-swift-owl"
      Then the command succeeds
      And the output contains "removed nothing"
      And the output contains "ctxloom session transcript purge amber-swift-owl --yes"
      And the home file ".ctxloom/sessions/amber-swift-owl/persist/transcript.jsonl" exists

    Scenario: --yes destroys the transcript and leaves the essence
      Given an initialized ctxloom project
      And a finished session "amber-swift-owl" with a transcript and an essence
      When I run "ctxloom session transcript purge amber-swift-owl --yes"
      Then the command succeeds
      And the home file ".ctxloom/sessions/amber-swift-owl/persist/transcript.jsonl" does not exist
      And the home file ".ctxloom/sessions/amber-swift-owl/essence.md" exists

    # With no essence, the transcript is the only record of what happened.
    # The refusal names the flag that permits it deliberately.
    Scenario: The only record of an undistilled session takes an extra flag
      Given an initialized ctxloom project
      And a finished session "brisk-copper-moth" with a transcript and no essence
      When I run "ctxloom session transcript purge brisk-copper-moth --yes"
      Then the command fails
      And the output contains "never distilled"
      And the output contains "--undistilled"
      And the home file ".ctxloom/sessions/brisk-copper-moth/persist/transcript.jsonl" exists

    Scenario: --undistilled destroys it anyway
      Given an initialized ctxloom project
      And a finished session "brisk-copper-moth" with a transcript and no essence
      When I run "ctxloom session transcript purge brisk-copper-moth --undistilled --yes"
      Then the command succeeds
      And the home file ".ctxloom/sessions/brisk-copper-moth/persist/transcript.jsonl" does not exist

    Scenario: Purging artifacts reports before it removes
      Given an initialized ctxloom project
      And a finished session "amber-swift-owl" with a transcript and an essence
      When I run "ctxloom session artifacts purge amber-swift-owl"
      Then the command succeeds
      And the output contains "removed nothing"
      And the output contains "ctxloom session artifacts purge amber-swift-owl --yes"
      And the home file ".ctxloom/sessions/amber-swift-owl/essence.md" exists

    Scenario: --yes destroys the essence and leaves the transcript
      Given an initialized ctxloom project
      And a finished session "amber-swift-owl" with a transcript and an essence
      When I run "ctxloom session artifacts purge amber-swift-owl --yes"
      Then the command succeeds
      And the home file ".ctxloom/sessions/amber-swift-owl/essence.md" does not exist
      And the home file ".ctxloom/sessions/amber-swift-owl/persist/transcript.jsonl" exists

    Scenario: The sweep reports before it removes
      Given an initialized ctxloom project
      And a finished session "amber-swift-owl" with a transcript and an essence
      When I run "ctxloom session purge amber-swift-owl"
      Then the command succeeds
      And the output contains "removed nothing"
      And the output contains "ctxloom session purge amber-swift-owl --yes"
      And the home file ".ctxloom/sessions/amber-swift-owl/persist/transcript.jsonl" exists
      And the home file ".ctxloom/sessions/amber-swift-owl/essence.md" exists

    Scenario: The sweep empties a session but does not unlist it
      Given an initialized ctxloom project
      And a finished session "amber-swift-owl" with a transcript and an essence
      When I run "ctxloom session purge amber-swift-owl --yes"
      Then the command succeeds
      And the home file ".ctxloom/sessions/amber-swift-owl/persist/transcript.jsonl" does not exist
      And the home file ".ctxloom/sessions/amber-swift-owl/essence.md" does not exist
      When I run "ctxloom session list --all"
      Then the output contains "amber-swift-owl"

    # Selection flags live on the leaf that understands them. The sweep has
    # no --undistilled to give, so it refuses and names the leaf that has one.
    Scenario: The sweep refuses an undistilled session and names the leaf that can
      Given an initialized ctxloom project
      And a finished session "brisk-copper-moth" with a transcript and no essence
      When I run "ctxloom session purge brisk-copper-moth --yes"
      Then the command fails
      And the output contains "ctxloom session transcript purge brisk-copper-moth --undistilled --yes"
      And the home file ".ctxloom/sessions/brisk-copper-moth/persist/transcript.jsonl" exists

    Scenario: The scratch worktrees are a population with their own listing
      Given an initialized ctxloom project
      And a finished session "amber-swift-owl" with a transcript and an essence
      When I run "ctxloom session worktrees"
      Then the command succeeds

    Scenario: The retired reap switch is gone, not hidden
      Given an initialized ctxloom project
      When I run "ctxloom session worktrees --reap --yes"
      Then the command fails
      And the output contains "unknown flag"

  Rule: Delete removes a session, all of it

    Purge empties a session and leaves it listed. Delete removes it: the
    index entry, the transcript AND the essence. Deleting only the index
    entry would leave every byte on disk and the session merely unfindable,
    which is a delete from the index's point of view and from nobody else's.

    Authored files are outside both verbs. They are never destroyed, and the
    report names them so a kept file is never silently kept.

    Scenario: Delete reports before it removes
      Given an initialized ctxloom project
      And a finished session "amber-swift-owl" with a transcript and an essence
      When I run "ctxloom session delete amber-swift-owl"
      Then the command succeeds
      And the output contains "removed nothing"
      And the output contains "ctxloom session delete amber-swift-owl --yes"
      And the home file ".ctxloom/sessions/amber-swift-owl/persist/transcript.jsonl" exists
      And the home file ".ctxloom/sessions/amber-swift-owl/essence.md" exists
      When I run "ctxloom session list --all"
      Then the output contains "amber-swift-owl"

    Scenario: --yes removes the index entry, the transcript and the essence
      Given an initialized ctxloom project
      And a finished session "amber-swift-owl" with a transcript and an essence
      When I run "ctxloom session delete amber-swift-owl --yes"
      Then the command succeeds
      And the home file ".ctxloom/sessions/amber-swift-owl/persist/transcript.jsonl" does not exist
      And the home file ".ctxloom/sessions/amber-swift-owl/essence.md" does not exist
      When I run "ctxloom session list --all"
      Then the output does not contain "amber-swift-owl"

    Scenario: Deleting a session that was never distilled is refused
      Given an initialized ctxloom project
      And a finished session "brisk-copper-moth" with a transcript and no essence
      When I run "ctxloom session delete brisk-copper-moth --yes"
      Then the command fails
      And the output contains "--undistilled"
      And the home file ".ctxloom/sessions/brisk-copper-moth/persist/transcript.jsonl" exists
      When I run "ctxloom session list --all"
      Then the output contains "brisk-copper-moth"
