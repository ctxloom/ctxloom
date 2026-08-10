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
    ctxloom session delete <harp>
    ctxloom session distill <harp>
    ctxloom session search <word>...
    ctxloom session transcript              (bare: lists)
    ctxloom session transcript list [<harp>]
    ctxloom session transcript watch <harp>

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

  Rule: Dropping a session from the index

    Scenario: Delete removes the entry
      Given an initialized ctxloom project
      And a recorded session "amber-swift-owl"
      When I run "ctxloom session delete amber-swift-owl"
      Then the command succeeds
      When I run "ctxloom session list --all"
      Then the output does not contain "amber-swift-owl"
