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
    ctxloom session list [--state live|ended|unknown]
    ctxloom session show <harp>
    ctxloom session edit <harp> --name <new>
    ctxloom session remove <harp> [--yes]
    ctxloom session keep <harp> [--undo]
    ctxloom session adopt <harp> [--yes]
    ctxloom session search <word>... [--state live|ended|unknown]
    ctxloom session transcript              (bare: lists)
    ctxloom session transcript list [<harp>]
    ctxloom session transcript watch <harp>
    ctxloom session transcript purge <harp> [--yes]
    ctxloom session worktrees               (bare: lists)
    ctxloom session worktrees list [<harp>]
    ctxloom session worktrees purge <harp> [--yes]
    ctxloom session purge <harp> [--yes]
    ctxloom session approvals

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

    # Tabled by format: `session list` is wired to emit(), so off a terminal
    # (which this harness always is) the no-flag row now gets the JSON rows
    # array (empty here), not the "(no sessions)" text line. Reuses the
    # branch's shared empty-collection step rather than a new spelling.
    Scenario Outline: A fresh project has recorded nothing
      Given an initialized ctxloom project
      When I run "ctxloom <flags> session list"
      Then the command succeeds
      And the output reports "$" as empty, saying "no sessions"

      @json-default
      Examples: no --format at all takes the derived default off a terminal; an explicit one wins in both directions
        | flags         |
        |               |
        | --format json |
        | --format text |

    # A session's title is the first thing its user asked, read from its
    # recorded transcript: the listing, the search and `show` all carry it.
    Scenario: Showing a session prints its record, titled by its first prompt
      Given an initialized ctxloom project
      And a recorded session "amber-swift-owl"
      When I run "ctxloom session show amber-swift-owl --format text"
      Then the command succeeds
      And the output contains "Seeded prompt for amber-swift-owl."
      And the output contains "engine:"
      When I run "ctxloom session list --format json"
      Then the JSON output array "$" contains an object whose "harp" is "amber-swift-owl" and whose "summary" is "Seeded prompt for amber-swift-owl."

    # The index says ended; the lock says running. The lock wins in the
    # listing too: ended_at is when the LAST run ended and a resume keeps it,
    # so it is no answer to "is this running now". The state is the lock's
    # verdict — held is live, free is ended, no lock file is unknown — and a
    # live session's end is blank rather than a stamp from its previous run.
    Scenario: The listing takes each session's state from its lock, not its end stamp
      Given an initialized ctxloom project
      And a session "amber-swift-owl" with a transcript that is running again
      And a finished session "brisk-copper-moth" with a transcript
      And a finished session "calm-dusty-wren" with a transcript but no session lock
      When I run "ctxloom session list --all --format json"
      Then the command succeeds
      And the JSON output array "$" contains an object whose "harp" is "amber-swift-owl" and whose "state" is "live"
      And the JSON output array "$" contains an object whose "harp" is "amber-swift-owl" and that has no "end"
      And the JSON output array "$" contains an object whose "harp" is "brisk-copper-moth" and whose "state" is "ended"
      And the JSON output array "$" contains an object whose "harp" is "calm-dusty-wren" and whose "state" is "unknown"
      When I run "ctxloom session list --all --format text"
      Then the command succeeds
      And the output contains "STATE"

    Scenario Outline: --state keeps only the sessions in that state
      Given an initialized ctxloom project
      And a session "amber-swift-owl" with a transcript that is running again
      And a finished session "brisk-copper-moth" with a transcript
      And a finished session "calm-dusty-wren" with a transcript but no session lock
      When I run "ctxloom session <leaf> --all --state <state> --format json"
      Then the command succeeds
      And the JSON output array "$" contains an object whose "harp" is "<kept>" and whose "state" is "<state>"
      And the JSON output array "$" contains no object whose "harp" is "<dropped1>"
      And the JSON output array "$" contains no object whose "harp" is "<dropped2>"

      Examples: both listings filter the same way
        | leaf           | state   | kept              | dropped1          | dropped2          |
        | list           | live    | amber-swift-owl   | brisk-copper-moth | calm-dusty-wren   |
        | list           | ended   | brisk-copper-moth | amber-swift-owl   | calm-dusty-wren   |
        | list           | unknown | calm-dusty-wren   | amber-swift-owl   | brisk-copper-moth |
        | search seeded  | live    | amber-swift-owl   | brisk-copper-moth | calm-dusty-wren   |
        | search seeded  | ended   | brisk-copper-moth | amber-swift-owl   | calm-dusty-wren   |

    Scenario: --state refuses a word that is not a state
      Given an initialized ctxloom project
      When I run "ctxloom session list --state running"
      Then the command fails
      And the output contains "live, ended, unknown"

  Rule: Renaming is a field assignment, so it rides `edit`

    A session's name is a field on its record, not a thing with a verb of
    its own. `edit` is the one mutation verb for the noun, and `--name` is
    the assignment that renames the harp. The backend transcript is
    unaffected: the session keeps its bound session id and its transcript,
    and only the name it answers to moves. Its output folder
    moves with it, to the new name beside the old, and the record names
    where it went; a rename whose folder cannot move is refused whole.

    @json-default
    Scenario: --name renames the harp and moves its output folder
      Given an initialized ctxloom project
      And a finished session "amber-swift-owl" with a transcript and a summary an older ctxloom left
      When I run "ctxloom session edit amber-swift-owl --name bright-keen-hawk"
      Then the command succeeds
      And the output contains "bright-keen-hawk"
      And the output file "essence.md" of session "bright-keen-hawk" exists
      And the output file "essence.md" of session "amber-swift-owl" does not exist
      When I run "ctxloom session list --all"
      Then the JSON output array "$" contains an object whose "harp" is "bright-keen-hawk"
      And the JSON output array "$" contains no object whose "harp" is "amber-swift-owl"

    Scenario: The old rename verb is gone, not hidden
      Given an initialized ctxloom project
      And a recorded session "amber-swift-owl"
      When I run "ctxloom session rename amber-swift-owl bright-keen-hawk"
      Then the command fails
      And the output contains "unknown command"

    # A session has no authored document. Its index entry is machine-written
    # and its title is DERIVED from its transcript, so an editor round-trip
    # here would offer edits nothing keeps. The bare form says so outright and names the
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

  Rule: Adopt re-indexes orphaned vendor transcripts into a harp's lineage

    A rotation the index never recorded — chiefly a claude-code /clear
    before the rotation-lineage fix — can leave a vendor transcript on disk
    with no index row and no rotation record. `session adopt` scans the
    same claude project directory as the harp's current transcript for
    files like that, and reports which ones belong in this harp's lineage.

    Without --yes this only reports; nothing on disk or in the session
    index changes. --yes seeds the harp's Rotations through the session
    store.

    # Tabled by format: `session adopt` is wired to emit(), so off a terminal
    # (which this harness always is) the no-flag row now gets the JSON
    # sessionAdoptResult, not renderSessionAdopt's "would adopt" verb-tensed
    # summary line. "would adopt" is prose the text renderer builds from the
    # candidate's verdict ("adopt") plus the dry-run/--yes state; the JSON
    # row asserts that same underlying verdict directly instead.
    Scenario Outline: A dry run reports the orphan and changes nothing
      Given an initialized ctxloom project
      And a recorded session "amber-swift-owl" with a claude vendor transcript spanning "2026-04-10T00:00:00Z" to "2026-04-10T01:00:00Z"
      And an orphaned claude vendor transcript "orphan-one" for "amber-swift-owl" spanning "2026-04-05T00:00:00Z" to "2026-04-05T01:00:00Z"
      When I run "ctxloom <flags> session adopt amber-swift-owl"
      Then the command succeeds
      And the output reports "candidates[session_id=orphan-one].session_id" as "<names the orphan>"
      And the output reports "candidates[session_id=orphan-one].verdict" as "<names the verdict>"

      @json-default
      Examples: no --format at all takes the derived default off a terminal; an explicit one wins in both directions
        | flags         | names the orphan | names the verdict |
        |               | orphan-one        | adopt              |
        | --format json | orphan-one        | adopt              |
        | --format text | orphan-one        | would adopt        |

    Scenario: --yes seeds the lineage through the store
      Given an initialized ctxloom project
      And a recorded session "amber-swift-owl" with a claude vendor transcript spanning "2026-04-10T00:00:00Z" to "2026-04-10T01:00:00Z"
      And an orphaned claude vendor transcript "orphan-one" for "amber-swift-owl" spanning "2026-04-05T00:00:00Z" to "2026-04-05T01:00:00Z"
      When I run "ctxloom session adopt amber-swift-owl --yes"
      Then the command succeeds
      And the output contains "orphan-one"

    # The session's engine decides what adopt can read: one whose transcript
    # readers cannot say when their records were written is refused by that
    # missing capability, naming the engine, rather than silently scanning
    # nothing.
    Scenario: A backend without a readable store is refused, naming it
      Given an initialized ctxloom project
      And a recorded session "amber-swift-owl" for backend "mock"
      When I run "ctxloom session adopt amber-swift-owl"
      Then the command fails
      And the output contains "mock"
      And the output contains "supplies no transcript reader that can say when its records were written"

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
      And a recorded session "amber-swift-owl" for backend "claude-code"
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

    A session holds two separable populations: the conversation it recorded
    and the scratch git checkouts it left behind. Each is destroyed by the
    sub-noun that understands it, and `session purge` sweeps both. The
    session's output dir — its plans, and any summary an older ctxloom left
    there — is the human's, and no destroyer reaches it.

    None of them removes anything without `--yes`. A run without it prints
    the plan and then says outright that it removed nothing, naming the exact
    command that would — a plan and an outcome otherwise render identically,
    and the difference is the whole point.

    Scenario: Purging a transcript reports before it removes
      Given an initialized ctxloom project
      And a finished session "amber-swift-owl" with a transcript
      When I run "ctxloom session transcript purge amber-swift-owl"
      Then the command succeeds
      And the output contains "removed nothing"
      And the output contains "ctxloom session transcript purge amber-swift-owl --yes"
      And the home file ".ctxloom/sessions/amber-swift-owl/transcripts/transcript.jsonl" exists

    Scenario: --yes destroys the transcript and leaves the output dir
      Given an initialized ctxloom project
      And a finished session "amber-swift-owl" with a transcript and a summary an older ctxloom left
      When I run "ctxloom session transcript purge amber-swift-owl --yes"
      Then the command succeeds
      And the home file ".ctxloom/sessions/amber-swift-owl/transcripts/transcript.jsonl" does not exist
      And the output file "essence.md" of session "amber-swift-owl" exists

    Scenario: The sweep reports before it removes
      Given an initialized ctxloom project
      And a finished session "amber-swift-owl" with a transcript
      When I run "ctxloom session purge amber-swift-owl"
      Then the command succeeds
      And the output contains "removed nothing"
      And the output contains "ctxloom session purge amber-swift-owl --yes"
      And the home file ".ctxloom/sessions/amber-swift-owl/transcripts/transcript.jsonl" exists

    Scenario: The sweep empties a session but does not unlist it
      Given an initialized ctxloom project
      And a finished session "amber-swift-owl" with a transcript and a summary an older ctxloom left
      When I run "ctxloom session purge amber-swift-owl --yes"
      Then the command succeeds
      And the home file ".ctxloom/sessions/amber-swift-owl/transcripts/transcript.jsonl" does not exist
      And the output file "essence.md" of session "amber-swift-owl" exists
      When I run "ctxloom session list --all"
      Then the output contains "amber-swift-owl"

    # The session lock only ever REFUSES. A free lock proves the owner dead; a
    # held lock, or no lock at all, refuses — "cannot determine" is never
    # permission. Every session from before the lock existed has no lock, so
    # the refusal names the exact command that destroys it deliberately.
    Scenario: A session the lock cannot prove finished is refused, and the escape is named
      Given an initialized ctxloom project
      And a finished session "amber-swift-owl" with a transcript but no session lock
      When I run "ctxloom session purge amber-swift-owl --yes"
      Then the command fails
      And the output contains "cannot be proven dead"
      And the output contains "ctxloom session purge amber-swift-owl --yes --even-if-live"
      And the home file ".ctxloom/sessions/amber-swift-owl/transcripts/transcript.jsonl" exists

    Scenario: --even-if-live sweeps it anyway
      Given an initialized ctxloom project
      And a finished session "amber-swift-owl" with a transcript but no session lock
      When I run "ctxloom session purge amber-swift-owl --yes --even-if-live"
      Then the command succeeds
      And the home file ".ctxloom/sessions/amber-swift-owl/transcripts/transcript.jsonl" does not exist

    # The index says ended; the lock says running. The lock wins, on the leaf
    # as much as on the sweep.
    Scenario: A session running again is refused by the transcript's own destroyer
      Given an initialized ctxloom project
      And a session "amber-swift-owl" with a transcript that is running again
      When I run "ctxloom session transcript purge amber-swift-owl --yes"
      Then the command fails
      And the output contains "owner is alive"
      And the output contains "--even-if-live"
      And the home file ".ctxloom/sessions/amber-swift-owl/transcripts/transcript.jsonl" exists

    Scenario: The scratch worktrees are a population with their own listing
      Given an initialized ctxloom project
      And a finished session "amber-swift-owl" with a transcript
      When I run "ctxloom session worktrees"
      Then the command succeeds
      When I run "ctxloom session worktrees list"
      Then the command succeeds
      When I run "ctxloom session worktrees list amber-swift-owl"
      Then the command succeeds

    Scenario: Purging worktrees reports before it removes
      Given an initialized ctxloom project
      And a finished session "amber-swift-owl" with a transcript
      When I run "ctxloom session worktrees purge amber-swift-owl"
      Then the command succeeds
      And the output contains "removed nothing"
      And the output contains "ctxloom session worktrees purge amber-swift-owl --yes"

    Scenario: The retired reap switch is gone, not hidden
      Given an initialized ctxloom project
      When I run "ctxloom session worktrees --reap --yes"
      Then the command fails
      And the output contains "unknown flag"

  Rule: Delete removes a session, all of it

    Purge empties a session and leaves it listed. Delete removes it: the
    index entry and the transcript. Deleting only the index entry would leave
    every byte on disk and the session merely unfindable, which is a delete
    from the index's point of view and from nobody else's.

    Authored files are outside both verbs. They are never destroyed, and the
    report names them so a kept file is never silently kept. The output dir
    is the human's and is left where it is.

    Scenario: Delete reports before it removes
      Given an initialized ctxloom project
      And a finished session "amber-swift-owl" with a transcript
      When I run "ctxloom session remove amber-swift-owl"
      Then the command succeeds
      And the output contains "removed nothing"
      And the output contains "ctxloom session remove amber-swift-owl --yes"
      And the home file ".ctxloom/sessions/amber-swift-owl/transcripts/transcript.jsonl" exists
      When I run "ctxloom session list --all"
      Then the output contains "amber-swift-owl"

    Scenario: --yes removes the index entry and the transcript
      Given an initialized ctxloom project
      And a finished session "amber-swift-owl" with a transcript and a summary an older ctxloom left
      When I run "ctxloom session remove amber-swift-owl --yes"
      Then the command succeeds
      And the home file ".ctxloom/sessions/amber-swift-owl/transcripts/transcript.jsonl" does not exist
      And the output file "essence.md" of session "amber-swift-owl" exists
      When I run "ctxloom session list --all"
      Then the output does not contain "amber-swift-owl"

  Rule: A transcript is the session's record, kept until its retention passes

    Engines compact their own sessions, so a session's transcript is its only
    record. Every destroyer of transcripts refuses one whose session ended
    within session_transcript_retention (30 days unless configured), and one
    marked keep at any age. No flag overrides it: `ctxloom session keep
    <harp> --undo` releases a kept one, and time releases the rest.

    Scenario Outline: A transcript within its retention is refused by every destroyer
      Given an initialized ctxloom project
      And a session "brisk-copper-moth" with a transcript that finished an hour ago
      When I run "ctxloom <destroyer> brisk-copper-moth --yes"
      Then the command fails
      And the output contains "retention"
      And the home file ".ctxloom/sessions/brisk-copper-moth/transcripts/transcript.jsonl" exists

      Examples:
        | destroyer                  |
        | session transcript purge   |
        | session purge              |
        | session remove             |

    Scenario: A kept transcript is refused at any age, and the listing says it is kept
      Given an initialized ctxloom project
      And a finished session "amber-swift-owl" with a transcript
      When I run "ctxloom session keep amber-swift-owl"
      Then the command succeeds
      When I run "ctxloom session list --all --format json"
      Then the JSON output array "$" contains an object whose "harp" is "amber-swift-owl" and whose "kept" is "true"
      When I run "ctxloom session transcript purge amber-swift-owl --yes"
      Then the command fails
      And the output contains "ctxloom session keep amber-swift-owl --undo"
      And the home file ".ctxloom/sessions/amber-swift-owl/transcripts/transcript.jsonl" exists
      When I run "ctxloom session keep amber-swift-owl --undo"
      Then the command succeeds
      When I run "ctxloom session transcript purge amber-swift-owl --yes"
      Then the command succeeds
      And the home file ".ctxloom/sessions/amber-swift-owl/transcripts/transcript.jsonl" does not exist

  Rule: Pending approvals are readable from any terminal, and only readable

    # `session approvals` asks every live coordinator on this host, found by
    # the endpoint files coordinators write under the state root. With none
    # running nothing is parked anywhere, so that is a message naming where
    # discovery looked, not a failure; the structured form says how many
    # coordinators answered, which is what separates "none running" from
    # "running with nothing pending".
    Scenario: With no coordinator running, the text says so and where it looked
      Given an initialized ctxloom project
      When I run "ctxloom --format text session approvals"
      Then the command succeeds
      And the output contains "no coordinator is running"
      And the output contains "no live endpoint under"

    Scenario: With no coordinator running, the JSON reports none answered and nothing pending
      Given an initialized ctxloom project
      When I run "ctxloom --format json session approvals"
      Then the command succeeds
      And the output reports "coordinators" as "0"
      And the output reports "approvals" as empty, saying "no coordinator is running"

    # A SECOND PROCESS reaching a LIVE coordinator: the standing owner is a
    # real `ctxloom run` hosting one, and this command finds it by discovery
    # and asks it over RPC. "coordinators" is 1 only because that round trip
    # happened. The owner is interactive and routes no approvals, so nothing
    # is parked — a request parked by a headless child is not staged here:
    # nothing in this harness can wait on another process's queue without
    # polling it. That proof is the integration lane's
    # TestPermissionContract_AnotherProcessListsTheParkedAsk.
    Scenario: Another terminal reaches the running session's coordinator and finds nothing parked
      Given an initialized ctxloom project
      And a session owner is standing
      When I run "ctxloom --format json session approvals"
      Then the command succeeds
      And the output reports "coordinators" as "1"
      And the output reports "approvals" as empty, saying "no pending approvals"
      When I run "ctxloom --format text session approvals"
      Then the command succeeds
      And the output contains "no pending approvals"

    # The command only lists: there is no argument that names a request to
    # answer, so one passed is refused rather than acted on.
    Scenario: Naming a request to answer is refused
      Given an initialized ctxloom project
      When I run "ctxloom session approvals amber-swift-owl"
      Then the command fails
      And the output contains "unknown command"
