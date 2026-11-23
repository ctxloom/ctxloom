# world: base
# NOT ported: nothing; the noun is new, and Go had no daemon.

Feature: top — what the user's daemon hosts, live: a table of sessions, and the asks and log of the one selected

  # DRAFT: this whole file, and every name, code and text in it, is a draft
  # for the owner's review. Its steps are new and undefined, so each scenario
  # is red until `ctxloom top` is built. The two proto drafts it implies
  # (`TopEvent`, `WatchTop`, `WatchSessionLog`, and `Session`'s new fields)
  # are named below and in the report that came with it; no proto exists yet.

  `ctxloom top` is the management view of what the user's daemon hosts. It is
  a client of the daemon's `ctxloom.v1` service like every other verb, and in
  v1 it READS, and it ANSWERS permission asks as the user, as `ctxloom session
  approvals answer` does. It sends, stops, restarts and closes nothing:
  session controls are not v1.

    ctxloom top [<harp>|<session name> [--tree]] [--all] [--format json|text]
                [--state <state>]... [--agent <agent>]... [--profile <profile>]...
                [--tag <tag>]... [--engine <engine>]... [--owner <harp>]...
                [--sort <column>[:asc|desc]] [--log <harp>]

  THE MODEL (the owner's): an email client whose messages are the sessions.
  The top part is the INBOX: a list, one row per session, sortable and
  filterable like a mail list, the selected row highlighted and moved with
  the keys. The bottom part is the READING PANE of the selected session: its
  waiting permission asks, each answerable there, and its log.

  SCOPE. One API serves three watches; the CLI names the scope, the daemon
  applies it (`WatchTopRequest.scope`, DRAFT):

    ctxloom top                     the whole list: every session of this
                                    project (`--all`: of every project)
    ctxloom top <harp>              ONE session: its row, its asks, its log
    ctxloom top <harp> --tree       a SUBTREE: that session and every
                                    descendant, with their asks

  A one-session watch carries that session's log without `--log` (it is the
  point of watching one); the other scopes carry a log only for the session
  `--log` names, which must be in scope. Filters and sort apply within the
  scope. `<harp>` is a session of this project; `--all` with a `<harp>` is a
  usage error. A scoped watch is the same `WatchTop` call as the whole list:
  nothing but the scope in its request differs, so a client such as an
  editor's plugin follows one session without the whole list.

  TWO FORMS, ONE NEWS. At a terminal it is a full-screen UI (below). With
  `--format json`, which is also the default off a terminal, it writes a
  STREAM: one proto3 JSON message per line, until it is stopped, for an
  editor's plugin to follow. `--format text` off a terminal is refused: a
  full screen needs a terminal.

  THE STREAM'S MESSAGE is `ctxloom.v1.TopEvent` (DRAFT), exactly ONE of its
  fields set per line, printed as the message's generated proto3 JSON (proto
  field names, a default left out, an enum by its name, a Timestamp RFC 3339):

    waiting           (only when no daemon is serving) said once, then again
                      whenever the connection is lost: `reason`; no daemon
                      has been reached, so no snapshot is possible
    snapshot          the first line once a daemon is reached: every session
                      that matches, and every ask waiting under them
    session_changed   a session now matching, whole (new, or any field changed)
    session_removed   a session no longer in view: `name`, `reason`
                      (`LEFT_FILTER`, `DESTROYED`)
    approval_asked    an ask now waiting: the `Approval`, with its options
    approval_settled  an ask no longer waiting: the `Approval` with its
                      `state` (`answered`, `expired`, `withdrawn`) and `option`
    log               one entry of the log asked for with `--log`: `session`,
                      `time`, `kind` (`TRANSCRIPT`, `DAEMON`), and for a
                      transcript entry its `line` as the transcript has it,
                      for a daemon entry its `event` (a word) and `text`

  A connection to a daemon always begins with a snapshot; there is no resuming. A client
  that reconnects takes a new snapshot as the truth.

  WHERE EACH THING IS DECIDED (DRAFT; the owner's to rule):
  FILTERING is the daemon's, a field of the request (`WatchTopRequest`):
  a session list is unbounded (the store only grows, ended sessions pile
  up), a client filtering would be sent every session and ask of every
  project, and with the daemon deciding, a session that stops matching or
  starts matching is one well-defined event (`session_removed` with
  `LEFT_FILTER`, `session_changed`) rather than every client re-deriving it.
  The screen's filter keys therefore re-open the watch with a new filter, and
  take a new snapshot. SORTING is the client's: the stream is a set of
  changes keyed by name, whose order is a matter of presentation. `--sort`
  sets the order of the snapshot's sessions and of the screen's table.

  FILTERS. Flags of different kinds AND; a flag given more than once is an OR
  among its values. `--state` takes `live`, `ended` or `unknown` (as `session
  list`) and, unless given, is `live` (DRAFT: a management view of what is
  hosted; ended sessions are asked for). `--owner <harp>` keeps the
  session's direct children (a subtree is a SCOPE, not a filter, above). SORT COLUMNS: `name`, `state`, `agent`, `profile`, `engine`,
  `owner`, `started` (default `started:desc`).

  SCOPE is the project the command runs in; `--all` is every project's.
  `<harp>` may also be spelled as the session's full name,
  `projects/{p}/sessions/{s}`, which names its project, so a session of
  another project is watched without `--all`.

  NO DAEMON, NO PROJECT (owner rulings, DRAFT texts). `top` never starts a
  daemon, and does not refuse when none is serving: it WAITS for one. While
  it waits, the JSON stream says it once, as a `waiting` line, and prints
  nothing more; the screen shows an empty inbox and a status line `waiting
  for a ctxloom daemon`. When a daemon is reached it streams as usual: the
  snapshot, then changes. A daemon lost under the stream puts it back to
  waiting; the next daemon's snapshot is the truth. Outside a project `top`
  lists nothing, as `session list` does, and stays open: it is a watch on the
  project of its directory, and receives what is pushed as that project's
  sessions are added, change or are removed.

  PUSHED, NEVER POLLED (the general flow, CLAUDE.md). The watch is served
  from a read model a PROJECTOR keeps (DRAFT: `top-view`, a read-model
  crate beside `session-index` and `pending-approvals`, registered in the
  daemon's kernel and caught up like them): it folds the sessions' streams
  (mint with its agent, profiles and loadout bundles; incarnations and
  their ends; close; owner and children), the approvals' streams (asked,
  answered, expired, withdrawn) and the capture facts (the transcript
  lines) into each session's row, its waiting asks and its log, and keeps a
  change feed that every open watch is woken by from the fold itself, as
  `WatchLibrary` is. The events the log and the rows need come from the
  policies that already run (the approvals' expiry through the scheduler;
  the re-host, restart and drain policies; the removal sequence's recorded
  steps), so a session leaving, a state change and an expiry are events the
  projector folds, not states anything polls. A watch whose scope names no
  project yet (a directory outside any) is woken when the project it
  resolves to is named. The only thing that is looked for on a timer is a
  daemon to wait for, since nothing can push before one exists.

  WHAT THE SESSION MESSAGE NEEDS (DRAFT): `Session` has no `agent`,
  `profiles` or `tags` today, and the table, the filters and the sort need
  them. This spec requires `Session.agent`, `Session.profiles` (the profile
  names the launch planned with) and `Session.tags` (owner ruling: the NAMES
  OF THE BUNDLES ITS PROFILES' LOADOUT LOADED; neither the launch's `--tag`s
  nor a profile's `select_tags`), recorded at its mint and carried on every
  read of it. `--tag <bundle>` and the TAGS column are those names.

  WHAT THE LOG NEEDS THAT THE DAEMON DOES NOT SERVE TODAY (DRAFT):
  `WatchTranscript` serves a session's transcript lines, but whole from its
  start and with no tail. Nothing serves the daemon's own events for a
  session (started, exited with its cause, re-hosted, restart owed, closed,
  a turn failed, an ask asked and how it settled): they are folded into
  several streams (session, run, approvals) and `Session` keeps only the
  incarnation count and the last failure. The log wants one RPC, `WatchSessionLog`
  (DRAFT), that answers the last N entries of both kinds, in the order they
  happened, then follows.

  Rule: The JSON stream begins with a snapshot, then says what changes

    Scenario: The first line is a snapshot of this project's live sessions, with their agent, profiles, tags, engine and owner
      # new: `ctxloom top --format json`, the snapshot; DRAFT.
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" of "amber-quiet-heron" launched on agent "reviewer" with profile "review", whose loadout loaded the bundle "team-rules"
      When I start "ctxloom top --format json" and read its output as it streams
      Then its first line has "snapshot" set, and no other field
      And the first line reports "snapshot.sessions[name$=/sessions/brisk-copper-moth].state" as "LIVE"
      And the first line reports "snapshot.sessions[name$=/sessions/brisk-copper-moth].owner" containing "/sessions/amber-quiet-heron"
      And the first line reports "snapshot.sessions[name$=/sessions/brisk-copper-moth].agent" as "reviewer"
      And the first line reports "snapshot.sessions[name$=/sessions/brisk-copper-moth].profiles[0]" as "review"
      And the first line reports "snapshot.sessions[name$=/sessions/brisk-copper-moth].tags[0]" as "team-rules"
      And the first line reports "snapshot.sessions[name$=/sessions/brisk-copper-moth].engine" as "claude-code"

    Scenario: With nothing hosted the snapshot is empty, and the stream stays open
      # new: an empty snapshot is `{"snapshot":{}}`, a default left out; DRAFT.
      Given an initialized ctxloom project
      And a ctxloom daemon is serving for this home, hosting nothing
      When I start "ctxloom top --format json" and read its output as it streams
      Then its first line is exactly '{"snapshot":{}}'
      And the command is still running a second later

    Scenario: Each line is one whole message, and a line with one field set
      # new: the stream is line-delimited JSON, never pretty-printed; DRAFT.
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" of "amber-quiet-heron" that is open and idle
      When I start "ctxloom top --format json" and read its output as it streams
      And a child "quiet-silver-fern" of "amber-quiet-heron" is launched
      Then every line read is one JSON object on one line
      And each line has exactly one of "snapshot", "session_changed", "session_removed", "approval_asked", "approval_settled", "log", "waiting" set

    Scenario: Off a terminal the format is json without being asked
      # new: the default format rule of the vocabulary (§4, `--format`); DRAFT.
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" of "amber-quiet-heron" that is open and idle
      When I start "ctxloom top" with its output not a terminal and read it as it streams
      Then its first line has "snapshot" set, and no other field

    Scenario: A session that starts arrives as session_changed
      # new: a change after the snapshot; DRAFT.
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" of "amber-quiet-heron" that is open and idle
      And I start "ctxloom top --format json" and read its output as it streams
      When a child "quiet-silver-fern" of "amber-quiet-heron" is launched
      Then within 10 seconds a line has "session_changed.session.name" containing "/sessions/quiet-silver-fern"
      And that line reports "session_changed.session.state" as "LIVE"

    Scenario: A session that ends leaves a live view, and stays in a view that asked for ended ones
      # new: state changes against the default and an explicit --state; DRAFT.
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" of "amber-quiet-heron" that is open and idle
      And I start "ctxloom top --format json" and read its output as it streams
      And I start "ctxloom top --format json --state live --state ended" and read its output as it streams, calling it "the wide one"
      When "brisk-copper-moth"'s run ends
      Then within 10 seconds a line has "session_removed.name" containing "/sessions/brisk-copper-moth"
      And that line reports "session_removed.reason" as "LEFT_FILTER"
      And within 10 seconds a line of "the wide one" has "session_changed.session.name" containing "/sessions/brisk-copper-moth"
      And that line reports "session_changed.session.state" as "ENDED"

    Scenario: A destroyed session is removed from every view, with the reason
      # new: DESTROYED against LEFT_FILTER; DRAFT.
      Given an initialized ctxloom project
      And an ended session "calm-dusty-wren" that nothing keeps
      And I start "ctxloom top --format json --state ended" and read its output as it streams
      When I run "ctxloom session remove calm-dusty-wren --yes"
      Then within 10 seconds a line has "session_removed.name" containing "/sessions/calm-dusty-wren"
      And that line reports "session_removed.reason" as "DESTROYED"

  Rule: A watch is scoped: the whole list, one session, or a session's subtree, all one API

    Scenario: A one-session watch holds that session's row, its asks and its log, and nothing else
      # new: DRAFT; `ctxloom top <harp>`.
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" of "amber-quiet-heron" whose engine has asked to run "Bash" with "rm -rf build/"
      And a child "quiet-silver-fern" of "amber-quiet-heron" whose engine has asked to run "Bash" with "make release"
      When I start "ctxloom top brisk-copper-moth --format json" and read its output as it streams
      Then its first line has "snapshot.sessions" holding 1 entry
      And its first line reports "snapshot.sessions[0].name" containing "/sessions/brisk-copper-moth"
      And its first line has "snapshot.approvals" holding 1 entry
      And its first line reports "snapshot.approvals[0].child" containing "/sessions/brisk-copper-moth"
      And a later line reports "log.session" containing "/sessions/brisk-copper-moth"

    Scenario: A one-session watch hears only that session's changes, asks and log
      # new: DRAFT.
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" of "amber-quiet-heron" that is open and idle
      And a child "quiet-silver-fern" of "amber-quiet-heron" that is open and idle
      And I start "ctxloom top brisk-copper-moth --format json" and read its output as it streams
      When the engine of "quiet-silver-fern" asks to run "Bash" with "make release"
      And "quiet-silver-fern" says "hello"
      And the engine of "brisk-copper-moth" asks to run "Bash" with "rm -rf build/"
      Then within 10 seconds a line has "approval_asked.approval.child" containing "/sessions/brisk-copper-moth"
      And no line read names "/sessions/quiet-silver-fern"

    Scenario: A subtree watch holds the session and every descendant, with their asks, and no sibling
      # new: DRAFT; `ctxloom top <harp> --tree`.
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" of "amber-quiet-heron"
      And a child "quiet-silver-fern" of "brisk-copper-moth" whose engine has asked to run "Bash" with "rm -rf build/"
      And a child "steady-lunar-pike" of "amber-quiet-heron"
      When I start "ctxloom top brisk-copper-moth --tree --format json" and read its output as it streams
      Then its first line has "snapshot.sessions" holding 2 entries
      And its first line reports "snapshot.approvals[0].child" containing "/sessions/quiet-silver-fern"
      And its first line reports no "snapshot.sessions[name$=/sessions/steady-lunar-pike]"
      And its first line reports no "snapshot.sessions[name$=/sessions/amber-quiet-heron]"
      When a child "calm-dusty-wren" of "quiet-silver-fern" is launched
      Then within 10 seconds a line has "session_changed.session.name" containing "/sessions/calm-dusty-wren"

    Scenario: Filters apply within a scope
      # new: DRAFT.
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" of "amber-quiet-heron" launched on agent "reviewer"
      And a child "quiet-silver-fern" of "brisk-copper-moth" launched on agent "fixer"
      When I start "ctxloom top amber-quiet-heron --tree --agent fixer --format json" and read its output as it streams
      Then its first line has "snapshot.sessions" holding 1 entry
      And its first line reports "snapshot.sessions[0].name" containing "/sessions/quiet-silver-fern"

    Scenario: The whole list, one session and a subtree are three calls of one RPC
      # new: DRAFT; the daemon's test-support log says `served <path>` for each service call.
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" of "amber-quiet-heron" that is open and idle
      When I start "ctxloom top --format json", "ctxloom top brisk-copper-moth --format json" and "ctxloom top amber-quiet-heron --tree --format json", and read their first lines
      Then the daemon's log says it served "WatchTop" three times
      And it served no other watch RPC for them but "WatchSessionLog" for the one-session watch

    Scenario: A scope naming a session this project does not have is refused
      # new: DRAFT code `session-cli-top-session-unknown`.
      Given an initialized ctxloom project
      And a ctxloom daemon is serving for this home, hosting nothing
      When I run "ctxloom top nobody-here-today --format json"
      Then the command exits with code 20
      And the refusal is "session-cli-top-session-unknown"

    Scenario: --all with a session to watch is a usage error
      # new: DRAFT code `session-cli-top-all-with-session`; a harp names one project's session.
      Given an initialized ctxloom project
      When I run "ctxloom top brisk-copper-moth --all --format json"
      Then the command exits with code 20
      And the refusal is "session-cli-top-all-with-session"

    Scenario: --tree without a session to root it is a usage error
      # new: DRAFT code `session-cli-top-tree-without-session`.
      Given an initialized ctxloom project
      When I run "ctxloom top --tree --format json"
      Then the command exits with code 20
      And the refusal is "session-cli-top-tree-without-session"

    Scenario: A one-session watch ends when its session is destroyed
      # new: DRAFT; the scope is gone, so the stream says so and ends, exit 0.
      Given an initialized ctxloom project
      And an ended session "calm-dusty-wren" that nothing keeps
      And I start "ctxloom top calm-dusty-wren --format json --state ended" and read its output as it streams
      When I run "ctxloom session remove calm-dusty-wren --yes"
      Then within 10 seconds a line has "session_removed.reason" as "DESTROYED"
      And the stream then ends and the command exits with code 0

    Scenario: A subtree watch whose root is destroyed ends the same way
      # new: DRAFT.
      Given an initialized ctxloom project
      And an ended session "calm-dusty-wren" that nothing keeps
      And I start "ctxloom top calm-dusty-wren --tree --format json --state ended" and read its output as it streams
      When I run "ctxloom session remove calm-dusty-wren --yes"
      Then within 10 seconds a line has "session_removed.reason" as "DESTROYED"
      And the stream then ends and the command exits with code 0

  Rule: Filtering is the daemon's, so the snapshot and the changes agree on what is in view

    Scenario Outline: A filter keeps only the sessions that match it
      # new: one filter per field, as the daemon decides it; DRAFT.
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" of "amber-quiet-heron" launched on agent "reviewer" with profile "review", whose loadout loaded the bundle "team-rules", on engine "claude-code"
      And a child "quiet-silver-fern" of "amber-quiet-heron" launched on agent "fixer" with profile "fix", whose loadout loaded the bundle "fix-kit", on engine "opencode"
      When I start "ctxloom top --format json <flag>" and read its output as it streams
      Then its first line reports "snapshot.sessions[name$=/sessions/<kept>]" as present
      And its first line reports no "snapshot.sessions[name$=/sessions/<dropped>]"

      Examples:
        | flag                        | kept              | dropped           |
        | --agent reviewer            | brisk-copper-moth | quiet-silver-fern |
        | --profile fix               | quiet-silver-fern | brisk-copper-moth |
        | --tag team-rules            | brisk-copper-moth | quiet-silver-fern |
        | --engine opencode           | quiet-silver-fern | brisk-copper-moth |
        | --owner amber-quiet-heron   | quiet-silver-fern | amber-quiet-heron |

    Scenario: Flags of different kinds are an AND, and a flag given twice is an OR
      # new: the combination rule; DRAFT.
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" of "amber-quiet-heron" launched on agent "reviewer" with profile "review", whose loadout loaded the bundle "team-rules", on engine "claude-code"
      And a child "quiet-silver-fern" of "amber-quiet-heron" launched on agent "fixer" with profile "fix", whose loadout loaded the bundle "fix-kit", on engine "opencode"
      When I start "ctxloom top --format json --agent reviewer --agent fixer --engine opencode" and read its output as it streams
      Then its first line has "snapshot.sessions" holding 1 entry
      And its first line reports "snapshot.sessions[0].name" containing "/sessions/quiet-silver-fern"

    Scenario: --owner keeps a session's children, the subtree scope its whole tree
      # new: the owner tree; DRAFT.
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" of "amber-quiet-heron"
      And a child "quiet-silver-fern" of "brisk-copper-moth"
      When I start "ctxloom top brisk-copper-moth --tree --format json" and read its output as it streams
      Then its first line has "snapshot.sessions" holding 2 entries
      And its first line reports "snapshot.sessions[name$=/sessions/brisk-copper-moth]" as present
      And its first line reports "snapshot.sessions[name$=/sessions/quiet-silver-fern]" as present
      When I start "ctxloom top --format json --owner brisk-copper-moth" and read its output as it streams
      Then its first line has "snapshot.sessions" holding 1 entry
      And its first line reports "snapshot.sessions[0].name" containing "/sessions/quiet-silver-fern"

    Scenario: The filter applies to changes too: a grandchild never arrives under --owner, a child does
      # new: DRAFT.
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" of "amber-quiet-heron" that is open and idle
      And I start "ctxloom top --format json --owner amber-quiet-heron" and read its output as it streams
      When a child "quiet-silver-fern" of "brisk-copper-moth" is launched
      Then within 10 seconds no line names "/sessions/quiet-silver-fern"
      When a child "steady-lunar-pike" of "amber-quiet-heron" is launched
      Then within 10 seconds a line has "session_changed.session.name" containing "/sessions/steady-lunar-pike"

    Scenario: An ask outside the filter is not sent
      # new: asks follow their sessions into and out of view; DRAFT.
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" of "amber-quiet-heron" launched on agent "reviewer"
      And a child "quiet-silver-fern" of "amber-quiet-heron" launched on agent "fixer"
      And I start "ctxloom top --format json --agent reviewer" and read its output as it streams
      When the engine of "quiet-silver-fern" asks to run "Bash" with "make release"
      And the engine of "brisk-copper-moth" asks to run "Bash" with "rm -rf build/"
      Then within 10 seconds a line has "approval_asked.approval.child" containing "/sessions/brisk-copper-moth"
      And no line read has "approval_asked.approval.child" containing "/sessions/quiet-silver-fern"

    Scenario: --state refuses a word that is not a state, before any daemon is reached
      # new: as `session list --state`; DRAFT code.
      Given an initialized ctxloom project
      And no ctxloom daemon is serving for this home
      When I run "ctxloom top --format json --state running"
      Then the command exits with code 20
      And the output contains "live, ended, unknown"
      And no ctxloom daemon was started

    Scenario: --owner names a session this project has, or is refused
      # new: DRAFT code `session-cli-top-session-unknown`.
      Given an initialized ctxloom project
      And a ctxloom daemon is serving for this home, hosting nothing
      When I run "ctxloom top --format json --owner nobody-here-today"
      Then the command exits with code 20
      And the refusal is "session-cli-top-session-unknown"
      And the output contains "nobody-here-today"

  Rule: Sorting is the client's, and orders the snapshot

    Scenario Outline: --sort orders the snapshot's sessions by a column
      # new: DRAFT.
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" of "amber-quiet-heron" launched on agent "reviewer" on engine "claude-code", started first
      And a child "quiet-silver-fern" of "amber-quiet-heron" launched on agent "fixer" on engine "opencode", started second
      When I start "ctxloom top --format json --owner amber-quiet-heron --sort <sort>" and read its output as it streams
      Then its first line reports "snapshot.sessions[0].name" containing "<first>"

      Examples:
        | sort          | first             |
        | started       | quiet-silver-fern |
        | started:asc   | brisk-copper-moth |
        | name          | brisk-copper-moth |
        | name:desc     | quiet-silver-fern |
        | agent         | quiet-silver-fern |
        | engine:desc   | quiet-silver-fern |

    Scenario: With no --sort the snapshot is the most recently started first
      # new: DRAFT; the listing's own order (`ListSessions`).
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" of "amber-quiet-heron", started first
      And a child "quiet-silver-fern" of "amber-quiet-heron", started second
      When I start "ctxloom top --format json --owner amber-quiet-heron" and read its output as it streams
      Then its first line reports "snapshot.sessions[0].name" containing "/sessions/quiet-silver-fern"

    Scenario: A sort column that is not one is refused, naming the columns
      # new: DRAFT code `session-cli-top-unknown-sort-column`.
      Given an initialized ctxloom project
      When I run "ctxloom top --format json --sort mood"
      Then the command exits with code 20
      And the refusal is "session-cli-top-unknown-sort-column"
      And the output contains "name, state, agent, profile, engine, owner, started"

  Rule: Asks arrive, settle and expire in the stream, and are answered as the user

    Scenario: The snapshot holds the asks already waiting, each with its options
      # new: DRAFT.
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" of "amber-quiet-heron" whose engine has asked to run "Bash" with "rm -rf build/"
      When I start "ctxloom top --format json" and read its output as it streams
      Then its first line has "snapshot.approvals" holding 1 entry
      And its first line reports "snapshot.approvals[0].name" containing "/approvals/ask-"
      And its first line reports "snapshot.approvals[0].child" containing "/sessions/brisk-copper-moth"
      And its first line reports "snapshot.approvals[0].tool_name" as "Bash"
      And its first line reports "snapshot.approvals[0].options" holding 4 entries

    Scenario: An ask made while it streams arrives as approval_asked
      # new: DRAFT.
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" of "amber-quiet-heron" that is open and idle
      And I start "ctxloom top --format json" and read its output as it streams
      When the engine of "brisk-copper-moth" asks to run "Bash" with "rm -rf build/"
      Then within 10 seconds a line has "approval_asked.approval.name" containing "/approvals/ask-"
      And that line reports "approval_asked.approval.input_excerpt" containing "rm -rf build/"
      And that line reports "approval_asked.approval.options[0].id" as "allow-once"
      And that line reports "approval_asked.approval.options[3].id" as "deny"

    Scenario: An answer made elsewhere arrives as approval_settled, naming the option
      # new: DRAFT; the answer is `session approvals answer`'s, unchanged.
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" of "amber-quiet-heron" whose engine has asked to run "Bash" with "rm -rf build/"
      And I start "ctxloom top --format json" and read its output as it streams
      And I note the ask named by the first line's "snapshot.approvals[0].name" as "the ask"
      When I run "ctxloom session approvals answer {the ask} allow-once"
      Then within 10 seconds a line has "approval_settled.approval.name" as "{the ask}"
      And that line reports "approval_settled.approval.state" as "answered"
      And that line reports "approval_settled.approval.option" as "allow-once"

    Scenario: An ask nobody answers arrives as approval_settled, expired
      # new: DRAFT; the time is the engine's `approval_timeout`.
      Given an initialized ctxloom project whose project config says "engines.claude-code.approval_timeout: 2s"
      And a child "brisk-copper-moth" of "amber-quiet-heron" whose engine has asked to run "Bash" with "rm -rf build/"
      And I start "ctxloom top --format json" and read its output as it streams
      When the ask's time runs out
      Then within 10 seconds a line has "approval_settled.approval.state" as "expired"

    Scenario: An ask withdrawn by a steer arrives as approval_settled, withdrawn
      # new: DRAFT.
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" of "amber-quiet-heron" whose engine has asked to run "Bash" with "rm -rf build/"
      And I start "ctxloom top --format json" and read its output as it streams
      When I run 'ctxloom session send brisk-copper-moth steer "stop and look at main.rs"'
      Then within 10 seconds a line has "approval_settled.approval.state" as "withdrawn"

    Scenario: The stream is read-only: it has no way to answer, and an answer is the session verb's
      # new: DRAFT; the plugin answers by running the verb, which any process of the user can.
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" of "amber-quiet-heron" whose engine has asked to run "Bash" with "rm -rf build/"
      And I start "ctxloom top --format json" and read its output as it streams
      When I write "allow-once" to its input
      Then the ask is still waiting under "amber-quiet-heron"

  Rule: --log follows one session's log: its transcript lines and its daemon's events, in the order they happened

    Scenario: The log starts with what is already there, then follows
      # new: DRAFT; needs `WatchSessionLog`, which nothing serves today.
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" of "amber-quiet-heron" whose hooks delivered the prompt "review the migration"
      When I start "ctxloom top --format json --log brisk-copper-moth" and read its output as it streams
      Then its first line has "snapshot" set
      And the next line reports "log.session" containing "/sessions/brisk-copper-moth"
      And that line reports "log.kind" as "DAEMON"
      And that line reports "log.event" as "started"
      And a later line reports "log.kind" as "TRANSCRIPT"
      And that line's "log.line" holds "review the migration"
      When "brisk-copper-moth" says "the migration is reversible"
      Then within 10 seconds a line reports "log.kind" as "TRANSCRIPT"
      And that line's "log.line" holds "the migration is reversible"

    Scenario Outline: The daemon's events for a session are in its log
      # new: DRAFT; the events the owner named: started, exited, re-hosted, an ask arriving or settling.
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" of "amber-quiet-heron" that is open and idle
      And I start "ctxloom top --format json --log brisk-copper-moth" and read its output as it streams
      When <cause>
      Then within 10 seconds a line reports "log.kind" as "DAEMON"
      And that line reports "log.event" as "<event>"

      Examples:
        | cause                                                                | event          |
        | "brisk-copper-moth"'s run ends                                       | exited         |
        | the daemon is stopped and the next daemon re-hosts "brisk-copper-moth" | rehosted     |
        | the engine of "brisk-copper-moth" asks to run "Bash" with "make test" | approval_asked |
        | the ask is answered "allow-once"                                     | approval_settled |

    Scenario: Entries are in the order they happened, whichever kind they are
      # new: DRAFT; one timeline, not two lists.
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" of "amber-quiet-heron" that is open and idle
      And I start "ctxloom top --format json --log brisk-copper-moth" and read its output as it streams
      When "brisk-copper-moth" says "done" and its run then ends
      Then within 10 seconds the log lines read are, in order, a transcript line holding "done", then a daemon entry "exited"
      And the log lines' "time" never go back

    Scenario: A log asked for a session nobody has is refused
      # new: DRAFT code `session-cli-top-session-unknown`.
      Given an initialized ctxloom project
      And a ctxloom daemon is serving for this home, hosting nothing
      When I run "ctxloom top --format json --log nobody-here-today"
      Then the command exits with code 20
      And the refusal is "session-cli-top-session-unknown"

    Scenario: Without --log the stream carries no log entries
      # new: the log is for the one session looked at, not every session's, DRAFT.
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" of "amber-quiet-heron" that is open and idle
      And I start "ctxloom top --format json" and read its output as it streams
      When "brisk-copper-moth" says "hello"
      Then within 3 seconds no line has "log" set

  Rule: Scope is this project's, or every project's

    Scenario: --all reaches past the current project
      # new: DRAFT; `--all` as `session list --all`.
      Given an initialized ctxloom project
      And a second project "api" with a session "steady-lunar-pike" running
      And a child "brisk-copper-moth" of "amber-quiet-heron" that is open and idle
      When I start "ctxloom top --format json --all" and read its output as it streams
      Then its first line reports "snapshot.sessions[name$=/sessions/steady-lunar-pike]" as present
      And its first line reports "snapshot.sessions[name$=/sessions/brisk-copper-moth]" as present

    Scenario: Without --all another project's sessions are not seen
      # new: DRAFT.
      Given an initialized ctxloom project
      And a second project "api" with a session "steady-lunar-pike" running
      When I start "ctxloom top --format json" and read its output as it streams
      Then its first line reports no "snapshot.sessions[name$=/sessions/steady-lunar-pike]"

    Scenario: Outside a ctxloom project top lists nothing and stays open, and what is pushed afterwards arrives
      # new: DRAFT; as `session list` lists nothing there, and the watch is pushed from the projection.
      Given a directory that is in no ctxloom project
      And a ctxloom daemon is serving for this home, hosting nothing
      When I start "ctxloom top --format json" and read its output as it streams
      Then its first line is exactly '{"snapshot":{}}'
      And the command is still running a second later
      When Alice sets ctxloom up in that directory and a session "amber-quiet-heron" starts there
      Then within 10 seconds a line has "session_changed.session.name" containing "/sessions/amber-quiet-heron"

    Scenario: Outside a ctxloom project, --all runs as anywhere
      # new: DRAFT.
      Given a directory that is in no ctxloom project
      And a second project "api" with a session "steady-lunar-pike" running
      When I start "ctxloom top --format json --all" and read its output as it streams
      Then its first line reports "snapshot.sessions[name$=/sessions/steady-lunar-pike]" as present

    Scenario: Outside a ctxloom project a session named by its harp alone is refused, as `session show` refuses it
      # new: DRAFT.
      Given a directory that is in no ctxloom project
      When I run "ctxloom top brisk-copper-moth --format json"
      Then the command exits with code 20
      And the refusal is "daemon-api-project-unknown"

    Scenario: A session of another project is watched by its full name
      # new: DRAFT; `projects/{p}/sessions/{s}` is a spelling of the scope.
      Given an initialized ctxloom project
      And a second project "api" with a session "steady-lunar-pike" running
      When I start "ctxloom top projects/{api}/sessions/steady-lunar-pike --format json" and read its output as it streams
      Then its first line has "snapshot.sessions" holding 1 entry
      And its first line reports "snapshot.sessions[0].name" containing "/sessions/steady-lunar-pike"

  Rule: The daemon is the user's; top waits for one, never starts one, and is pushed from its projection

    Scenario: With no daemon serving, top waits and starts none, saying so once
      # new: DRAFT; owner ruling: it waits.
      Given an initialized ctxloom project
      And no ctxloom daemon is serving for this home
      When I start "ctxloom top --format json" and read its output as it streams
      Then its first line has "waiting" set, and no other field
      And that line reports "waiting.reason" as "no ctxloom daemon is serving"
      And no ctxloom daemon was started
      And the command is still running two seconds later, and has printed no other line

    Scenario: When a daemon is reached, the stream goes on as usual with its snapshot
      # new: DRAFT.
      Given an initialized ctxloom project
      And no ctxloom daemon is serving for this home
      And I start "ctxloom top --format json" and read its output as it streams
      When a ctxloom daemon starts serving for this home, hosting a child "brisk-copper-moth" of "amber-quiet-heron"
      Then within 10 seconds a line has "snapshot" set
      And that line reports "snapshot.sessions[name$=/sessions/brisk-copper-moth].state" as "LIVE"

    Scenario: On the screen, waiting is an empty inbox and a status line, then the inbox
      # new: DRAFT texts.
      Given an initialized ctxloom project
      And no ctxloom daemon is serving for this home
      When I start "ctxloom top --format text" in a terminal and read its frames
      Then the first frame has no row, and a line "waiting for a ctxloom daemon"
      When a ctxloom daemon starts serving for this home, hosting a child "brisk-copper-moth" of "amber-quiet-heron"
      Then a later frame has a row for "brisk-copper-moth" and no line "waiting for a ctxloom daemon"

    Scenario: A draining daemon is not top's to use: it waits for the next one
      # new: DRAFT; a draining daemon refuses every new call, so top waits as if none served.
      Given a ctxloom daemon is serving for this home, draining, and hosting a child "brisk-copper-moth" that does not end its turn
      When I start "ctxloom top --format json" and read its output as it streams
      Then its first line has "waiting" set, and no other field

    Scenario: A daemon that dies under the stream puts it back to waiting, and the next daemon's snapshot is the truth
      # new: DRAFT; replaces the stream's failure, exit 10.
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" of "amber-quiet-heron" that is open and idle
      And I start "ctxloom top --format json" and read its output as it streams
      When the daemon is killed outright
      Then within 10 seconds a line has "waiting" set
      And every line it printed is one whole JSON object
      When a ctxloom daemon starts serving for this home
      Then within 10 seconds a line has "snapshot" set

    Scenario: A change is pushed to the stream, not found by a round
      # new: DRAFT; the watch is woken by the projector's fold. Under test-support the daemon's round period is set to an hour (DRAFT step), so only a push can deliver it.
      Given an initialized ctxloom project
      And the daemon's round period is an hour
      And a child "brisk-copper-moth" of "amber-quiet-heron" that is open and idle
      And I start "ctxloom top --format json" and read its output as it streams
      When a child "quiet-silver-fern" of "amber-quiet-heron" is launched
      Then within 5 seconds a line has "session_changed.session.name" containing "/sessions/quiet-silver-fern"

    Scenario: An ask, its answer and a session's removal are pushed the same way
      # new: DRAFT.
      Given an initialized ctxloom project
      And the daemon's round period is an hour
      And a child "brisk-copper-moth" of "amber-quiet-heron" that is open and idle
      And I start "ctxloom top --format json" and read its output as it streams
      When the engine of "brisk-copper-moth" asks to run "Bash" with "rm -rf build/"
      Then within 5 seconds a line has "approval_asked.approval.child" containing "/sessions/brisk-copper-moth"
      When I run "ctxloom session approvals answer brisk-copper-moth allow-once"
      Then within 5 seconds a line has "approval_settled.approval.state" as "answered"

    Scenario: Stopping the stream with SIGINT ends it, exit 128 plus the signal
      # new: DRAFT; ruling 114's signal exit.
      Given an initialized ctxloom project
      And I start "ctxloom top --format json" and read its output as it streams
      When I send it SIGINT
      Then the command exits with code 130
      And the daemon is still serving

  Rule: At a terminal, the screen is an inbox of sessions above the reading pane of the selected one

    The model is an email client's: the sessions are the messages. The INBOX
    (the top) is one row per session, sortable and filterable like a mail
    list, the selected row highlighted and moved with the keys; the READING
    PANE (the bottom) is the selected session's waiting asks, each answerable
    there, and its log. `ctxloom top <harp>` and `<harp> --tree` draw the same
    screen over their scope: an inbox of that session alone, or of its subtree.

    Everything below is what a TEST can observe. A real full-screen UI needs
    a pseudo-terminal, which these scenarios do not use (DRAFT; the owner's to
    rule): under `test-support`, with `CTXLOOM_TEST_TERMINAL` set, `ctxloom
    top --format text` draws each FRAME as plain text on stdout (no screen
    control codes), a frame ended by a line holding only `---`, and takes its
    keys from stdin, one key name per line: `down`, `up`, `tab` (moves
    between the table and the asks), `1`..`9` (choose the nth option of the
    ask in focus), `y` and `n` (answer a confirmation), `s` (sort by the next
    column), `r` (reverse the sort), `f` (opens the filter prompt: the next
    line is `<field>=<value>`, an empty one clears), `q` (quit). A frame is:
    a header line naming the scope, the filter and the sort, an inbox table (its
    header row `NAME STATE AGENT PROFILES TAGS ENGINE OWNER ASKS` and one row
    per session, the selected one marked `>`), a line `--- asks of <harp>`,
    its asks, a line `--- log of <harp>`, and its log entries oldest first.
    A real terminal's drawing, its alternate screen and its restoring on
    exit are not tested here (DEFERRED: needs a pty harness).

    Scenario: The screen is an inbox above, and the selected row's reading pane below
      # new: the layout, as the owner ruled it (an email client's); DRAFT texts.
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" of "amber-quiet-heron" launched on agent "reviewer"
      When I start "ctxloom top --format text" in a terminal and read its frames
      Then the first frame's header row is "NAME STATE AGENT PROFILES TAGS ENGINE OWNER ASKS"
      And the first frame has a row for "amber-quiet-heron" and a row for "brisk-copper-moth"
      And exactly one row is marked "> ", the first row until a key moves it
      And the table comes before the line "--- asks of <the selected row's harp>", which comes before the line "--- log of <the selected row's harp>"
      And nothing but the table's rows, those two sections and the header is in the frame

    Scenario: A scoped screen is an inbox of its scope alone
      # new: DRAFT.
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" of "amber-quiet-heron"
      And a child "quiet-silver-fern" of "brisk-copper-moth"
      And a child "steady-lunar-pike" of "amber-quiet-heron"
      When I start "ctxloom top brisk-copper-moth --tree --format text" in a terminal and read its frames
      Then the first frame has a row for "brisk-copper-moth" and a row for "quiet-silver-fern"
      And the first frame has no row for "steady-lunar-pike" and none for "amber-quiet-heron"
      When I start "ctxloom top steady-lunar-pike --format text" in a terminal and read its frames
      Then the first frame has one row, "steady-lunar-pike", marked "> "

    Scenario: Children are shown under their owner, indented, until a sort says otherwise
      # new: the owner tree in the table; DRAFT.
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" of "amber-quiet-heron"
      And a child "quiet-silver-fern" of "brisk-copper-moth"
      When I start "ctxloom top --format text" in a terminal and read its frames
      Then the first frame's rows are, in order, "amber-quiet-heron", "  brisk-copper-moth", "    quiet-silver-fern"
      When I press "s"
      Then the next frame's rows are in the sort's order, none indented

    Scenario: Moving the selection changes what the bottom part shows
      # new: the details are the selected row's alone; DRAFT.
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" of "amber-quiet-heron" whose engine has asked to run "Bash" with "rm -rf build/"
      And a child "quiet-silver-fern" of "amber-quiet-heron" that is open and idle
      When I start "ctxloom top --format text" in a terminal and read its frames
      And I press "down" until "brisk-copper-moth" is marked
      Then the frame has "--- asks of brisk-copper-moth" and its asks section holds "rm -rf build/"
      When I press "down"
      Then the frame has "--- asks of quiet-silver-fern" and its asks section holds no ask

    Scenario: A row shows how many asks wait on it
      # new: the ASKS column; DRAFT.
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" of "amber-quiet-heron" whose engine has asked to run "Bash" with "rm -rf build/"
      When I start "ctxloom top --format text" in a terminal and read its frames
      Then the frame's row for "brisk-copper-moth" has "1" in its ASKS column
      And the frame's row for "amber-quiet-heron" has "0"

    Scenario: The asks section lists each ask with its options, numbered
      # new: DRAFT.
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" of "amber-quiet-heron" whose engine has asked to run "Bash" with "rm -rf build/"
      When I start "ctxloom top --format text" in a terminal and read its frames
      And I press "down" until "brisk-copper-moth" is marked
      Then the asks section holds the ask's name, "Bash", "rm -rf build/" and the options "1 allow-once", "2 allow-session", "3 allow-always", "4 deny"

    Scenario: Choosing an option answers the ask as the user, and the screen shows it settled
      # new: the same answer as `session approvals answer`; DRAFT.
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" of "amber-quiet-heron" whose engine has asked to run "Bash" with "rm -rf build/"
      When I start "ctxloom top --format text" in a terminal and read its frames
      And I press "down" until "brisk-copper-moth" is marked
      And I press "tab"
      And I press "1"
      Then the child's engine was told to run the tool
      And a later frame's asks section for "brisk-copper-moth" says the ask was answered "allow-once"
      And the frame's row for "brisk-copper-moth" has "0" in its ASKS column

    Scenario: allow-always shows the config it would write and asks first, as the CLI does
      # new: DRAFT; the preview and the [y/N] are `session approvals answer`'s.
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" of "amber-quiet-heron" whose engine has asked to run "Bash" with "rm -rf build/"
      When I start "ctxloom top --format text" in a terminal and read its frames
      And I press "down" until "brisk-copper-moth" is marked
      And I press "tab"
      And I press "3"
      Then the frame shows ".ctxloom/config.yaml" and "Bash(rm -rf build/)" and "[y/N]"
      And the file ".ctxloom/config.yaml" does not contain "Bash(rm -rf build/)"
      When I press "n"
      Then an ask is still waiting under "amber-quiet-heron"
      When I press "3"
      And I press "y"
      Then the file ".ctxloom/config.yaml" contains "Bash(rm -rf build/)"
      And the child's engine was told to run the tool, and to allow "Bash(rm -rf build/)" for the rest of its session

    Scenario: An answer to an ask that was settled meanwhile is said on the screen, and nothing else changes
      # new: DRAFT; the refusal is `approvals-approval-settled`'s.
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" of "amber-quiet-heron" whose engine has asked to run "Bash" with "rm -rf build/"
      When I start "ctxloom top --format text" in a terminal and read its frames
      And I press "down" until "brisk-copper-moth" is marked
      And I press "tab"
      And the ask is answered from another terminal with "deny"
      And I press "1"
      Then the frame says the ask was already settled, answered "deny"
      And the child's engine was not told to run the tool

    Scenario: The log section shows the selected session's transcript lines and its daemon's events, oldest first
      # new: DRAFT.
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" of "amber-quiet-heron" whose hooks delivered the prompt "review the migration"
      When I start "ctxloom top --format text" in a terminal and read its frames
      And I press "down" until "brisk-copper-moth" is marked
      Then the log section has "started" and then "review the migration"
      When "brisk-copper-moth" says "the migration is reversible"
      Then a later frame's log section ends with "the migration is reversible"

    Scenario: A filter typed on the screen narrows the table, and clearing it widens it again
      # new: DRAFT; the filter re-opens the daemon's watch.
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" of "amber-quiet-heron" launched on agent "reviewer"
      And a child "quiet-silver-fern" of "amber-quiet-heron" launched on agent "fixer"
      When I start "ctxloom top --format text" in a terminal and read its frames
      And I press "f" and type "agent=reviewer"
      Then the frame's header names the filter "agent=reviewer"
      And the frame has a row for "brisk-copper-moth" and no row for "quiet-silver-fern"
      When I press "f" and type nothing
      Then the frame has a row for "quiet-silver-fern" again

    Scenario: Sorting on the screen follows the columns, and r reverses it
      # new: DRAFT.
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" of "amber-quiet-heron" launched on agent "reviewer"
      And a child "quiet-silver-fern" of "amber-quiet-heron" launched on agent "fixer"
      When I start "ctxloom top --format text --sort agent" in a terminal and read its frames
      Then the frame's header names the sort "agent:asc"
      And the row for "quiet-silver-fern" is above the row for "brisk-copper-moth"
      When I press "r"
      Then the row for "brisk-copper-moth" is above the row for "quiet-silver-fern"

    Scenario: q ends the screen, exit 0, and the daemon is left as it was
      # new: DRAFT.
      Given an initialized ctxloom project
      When I start "ctxloom top --format text" in a terminal and read its frames
      And I press "q"
      Then the command exits with code 0
      And the daemon is still serving

    Scenario: --format text off a terminal is refused, naming json
      # new: DRAFT code `session-cli-top-needs-terminal`; its fix names `--format json`.
      Given an initialized ctxloom project
      When I run "ctxloom top --format text" with its output not a terminal
      Then the command exits with code 20
      And the refusal is "session-cli-top-needs-terminal"
      And the output contains "--format json"

  Rule: One test of the real binary under a pseudo-terminal

    The text-frame scenarios above stand a terminal in; this one does not.
    It runs the real binary on a pty (unix only; DRAFT: ruled by the owner as
    a smoke test beside the seam).

    Scenario: On a real pseudo-terminal, top opens, draws, quits on q, and leaves the terminal as it found it
      # new: DRAFT; the pty smoke test.
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" of "amber-quiet-heron" that is open and idle
      And a unix host with a pseudo-terminal
      When I start "ctxloom top" on a pseudo-terminal of 100 columns and 30 rows
      Then within 10 seconds the terminal shows the row "brisk-copper-moth" and the line "--- asks of"
      And the terminal is on its alternate screen
      When I type "q" on it
      Then the command exits with code 0
      And the terminal is back on its main screen, its modes as they were before
